package gateway

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

// HTTPS for other computers without a certificate of a server's own
// (zhangly1 on X: Claude on another computer on the LAN needed magpie at
// https://). With Settings → Network and sharing → HTTPS on (settings
// HTTPS, `magpie https on`) the listener serves other computers TLS as it
// does with --tls-cert or mTLS (tlsListener), with:
//
//   - the certificate and key imported in Settings or by `magpie https
//     import` (kept in <dir>/https, read again when they change), else
//   - one magpie's own CA (the mTLS one, made on first use) signs in
//     memory for this computer's names, its addresses and the name the
//     client dialled (ownServerCert).
//
// --tls-cert comes before both. Another computer trusts magpie's CA once:
// Settings saves it to Downloads, and the gateway serves it at CAPath to
// any address it lets in, without a key (a CA's certificate is public;
// its fingerprint is shown beside it to check the copy by). This computer,
// and a WSL distro's agent, may still call plain http://.

// HTTPSOn: other computers reach magpie over https:// (the setting).
func HTTPSOn() bool { return settings.Shared().HTTPS }

// CAPath is where the gateway serves magpie's CA, for another computer to
// trust: .crt, which Windows opens to install and Debian's
// update-ca-certificates reads, as PEM, which every system takes.
const CAPath = "/magpie-ca.crt"

// FromThisComputer: r came from this computer, over loopback or to one of
// its own addresses, not through a proxy.
func FromThisComputer(r *http.Request) bool { return local(r) || ownAddress(r) }

func httpsDir() string { return filepath.Join(settings.Dir(), "https") }

// ImportedFiles are the certificate and key Settings imported, "" for
// none (both must be there).
func ImportedFiles() (cert, key string) {
	cert, key = filepath.Join(httpsDir(), "cert.pem"), filepath.Join(httpsDir(), "key.pem")
	if _, err := os.Stat(cert); err != nil {
		return "", ""
	}
	if _, err := os.Stat(key); err != nil {
		return "", ""
	}
	return cert, key
}

var imported struct {
	sync.Mutex
	files *certFiles
}

// importedCert serves the imported pair, read again when it changes; nil
// with none imported.
func importedCert() *certFiles {
	c, k := ImportedFiles()
	if c == "" {
		return nil
	}
	imported.Lock()
	defer imported.Unlock()
	if imported.files == nil || imported.files.cert != c || imported.files.key != k {
		imported.files = &certFiles{cert: c, key: k}
	}
	return imported.files
}

// settingsCert is the server certificate without --tls-cert: the one
// imported, else one magpie's CA signs. An imported pair that doesn't
// load (its files removed by hand, say) gives way to magpie's own rather
// than ending every handshake.
func settingsCert(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if f := importedCert(); f != nil {
		c, err := f.get(hello)
		if err == nil {
			return c, nil
		}
		log.Printf("TLS: the imported certificate in %s doesn't load (%v): serving one magpie's CA signs", httpsDir(), err)
	}
	return ownServerCert(hello)
}

// ImportCert keeps a certificate (its chain after it) and its key for
// HTTPS, from PEM files given in any order and split any way: one file
// holding both, or a certificate, a chain and a key apart. The
// certificate the key belongs to goes first. Refused: no certificate, no
// key, an encrypted key, a key that belongs to none of them, or a
// certificate that has run out.
func ImportCert(files ...[]byte) error {
	var certs [][]byte
	var key *pem.Block
	for _, b := range files {
		for {
			var blk *pem.Block
			blk, b = pem.Decode(b)
			if blk == nil {
				break
			}
			switch {
			case blk.Type == "CERTIFICATE":
				certs = append(certs, blk.Bytes)
			case blk.Type == "ENCRYPTED PRIVATE KEY" || blk.Headers["Proc-Type"] != "":
				return errors.New("the private key has a passphrase: give it without one (openssl pkey -in key.pem -out key-open.pem)")
			case strings.HasSuffix(blk.Type, "PRIVATE KEY"):
				if key == nil {
					key = blk
				}
			}
		}
	}
	switch {
	case len(certs) == 0 && key == nil:
		return errors.New("no PEM certificate or key in the files given")
	case len(certs) == 0:
		return errors.New("a private key but no certificate: give the certificate's file too")
	case key == nil:
		return errors.New("a certificate but no private key: give the key's file too")
	}
	keyPEM := pem.EncodeToMemory(key)
	leaf := -1
	for i, der := range certs {
		if _, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM); err == nil {
			leaf = i
			break
		}
	}
	if leaf < 0 {
		return errors.New("the private key belongs to none of the certificates given")
	}
	certs[0], certs[leaf] = certs[leaf], certs[0]
	var chain []byte
	for _, der := range certs {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	pair, err := tls.X509KeyPair(chain, keyPEM)
	if err != nil {
		return fmt.Errorf("the certificate and key can't be served: %w", err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return err
		}
	}
	if time.Now().After(pair.Leaf.NotAfter) {
		return fmt.Errorf("the certificate ran out on %s", pair.Leaf.NotAfter.Format("2006-01-02"))
	}
	if err := os.MkdirAll(httpsDir(), 0o700); err != nil {
		return err
	}
	if err := mtls.WriteFile(filepath.Join(httpsDir(), "key.pem"), keyPEM, 0o600); err != nil {
		return err
	}
	return mtls.WriteFile(filepath.Join(httpsDir(), "cert.pem"), chain, 0o644)
}

// RemoveImportedCert goes back to the certificate magpie's CA signs.
func RemoveImportedCert() error {
	for _, f := range []string{"cert.pem", "key.pem"} {
		if err := os.Remove(filepath.Join(httpsDir(), f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// CertInfo is what the certificate other computers are served says.
type CertInfo struct {
	// From: "files" (--tls-cert, File naming it), "imported" or "magpie"
	// (its CA's)
	From   string    `json:"from"`
	File   string    `json:"file,omitempty"`
	Names  []string  `json:"names,omitempty"`
	Issuer string    `json:"issuer,omitempty"`
	Until  time.Time `json:"until"`
}

// ServedCert says which certificate other computers get over https://.
func ServedCert() (CertInfo, error) {
	var c *tls.Certificate
	var info CertInfo
	var err error
	if cf, kf := TLSFiles(); cf != "" && kf != "" {
		info.From, info.File = "files", cf
		var pair tls.Certificate
		pair, err = tls.LoadX509KeyPair(cf, kf)
		c = &pair
	} else if f := importedCert(); f != nil {
		info.From = "imported"
		c, err = f.get(nil)
	} else {
		info.From = "magpie"
		c, err = ownServerCert(nil)
	}
	if err != nil {
		return info, err
	}
	if c == nil || len(c.Certificate) == 0 {
		return info, errors.New("no certificate")
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return info, err
	}
	info.Names = append(info.Names, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		info.Names = append(info.Names, ip.String())
	}
	if len(info.Names) == 0 && leaf.Subject.CommonName != "" {
		info.Names = []string{leaf.Subject.CommonName}
	}
	info.Issuer, info.Until = leaf.Issuer.CommonName, leaf.NotAfter
	return info, nil
}

// CA is magpie's CA certificate as PEM, made on first use, and its
// SHA-256 fingerprint (AB:CD:…), which a copy fetched over the network
// is checked by.
func CA() (pemBytes []byte, fingerprint string, err error) {
	ca, _, err := MTLSStore().CA()
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), Fingerprint(ca.Raw), nil
}

// Fingerprint is der's SHA-256 as colon-separated hex pairs, as Keychain
// Access, certmgr and openssl x509 -fingerprint show it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// caFileOpen: r asks for magpie's CA, which is answered without a key.
func caFileOpen(r *http.Request) bool {
	return r.URL.Path == CAPath && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// caFile serves magpie's CA. One isn't made for a request: until HTTPS or
// mTLS made it, there is none to trust.
func (s *Server) caFile(w http.ResponseWriter, r *http.Request) {
	st := MTLSStore()
	if !st.HasCA() {
		http.Error(w, "this magpie has no CA yet: turn on HTTPS in Settings → Network and sharing (magpie https on)", http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(st.CAFile())
	if err != nil {
		http.Error(w, "magpie's CA can't be read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-x509-ca-cert")
	h.Set("Content-Disposition", `attachment; filename="magpie-ca.crt"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	w.Write(b)
}
