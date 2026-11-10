// Package mtls is magpie's own certificate authority for mutual TLS
// (hhdaxia on X: "希望能支持 mtls，避免暴露端口到公网被 compromise";
// #5596). With it on (settings' MTLS, `magpie mtls on`), the gateway and
// `magpie web`'s page serve other computers over HTTPS with a server
// certificate this CA signs (or the user's own, --tls-cert), and a
// connection from another computer is let in only with a client
// certificate this CA issued and nobody revoked. Client certificates are
// issued for a device by name (`magpie mtls issue <name>`, Settings →
// Network and sharing) and exported as PEM files beside the CA's own
// certificate, which the device trusts the server by: Remote magpie takes
// all three (provider.ClientCert, ClientKey and ServerCA), as curl does
// (--cert, --key, --cacert).
//
// The CA's key never leaves this computer: it is kept in magpie's folder
// (0600) and is neither synced nor backed up, as the files here are no
// part of those.
package mtls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is the CA kept in Dir: ca.pem, ca-key.pem and clients.json, the
// certificates issued.
type Store struct{ Dir string }

// Client is a client certificate issued: Name the device's, as the user
// gave it; Serial the certificate's, in hex; Revoked when it was taken
// back, zero while it is good.
type Client struct {
	Name    string    `json:"name"`
	Serial  string    `json:"serial"`
	Issued  time.Time `json:"issued"`
	Expires time.Time `json:"expires"`
	Revoked time.Time `json:"revoked,omitzero"`
}

// Good: issued, not revoked and not run out at now.
func (c Client) Good(now time.Time) bool { return c.Revoked.IsZero() && now.Before(c.Expires) }

// Validity of what the CA signs: the CA ten years, a client certificate
// two, a server certificate (made again at each start) one.
const (
	caLife     = 10 * 365 * 24 * time.Hour
	clientLife = 2 * 365 * 24 * time.Hour
	serverLife = 365 * 24 * time.Hour
)

var mu sync.Mutex // one change to a store's files at a time, in this process

func (s Store) caFile() string      { return filepath.Join(s.Dir, "ca.pem") }
func (s Store) caKeyFile() string   { return filepath.Join(s.Dir, "ca-key.pem") }
func (s Store) clientsFile() string { return filepath.Join(s.Dir, "clients.json") }

// CAFile is the CA's certificate, the file a device trusts the server by.
func (s Store) CAFile() string { return s.caFile() }

// HasCA: the CA was made.
func (s Store) HasCA() bool {
	_, err := os.Stat(s.caKeyFile())
	return err == nil
}

// CA is the store's CA, made on first use.
func (s Store) CA() (*x509.Certificate, crypto.Signer, error) {
	mu.Lock()
	defer mu.Unlock()
	return s.ca()
}

func (s Store) ca() (*x509.Certificate, crypto.Signer, error) {
	cb, cerr := os.ReadFile(s.caFile())
	kb, kerr := os.ReadFile(s.caKeyFile())
	if cerr == nil && kerr == nil {
		return parsePair(cb, kb)
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, nil, cerr
	}
	if !errors.Is(kerr, os.ErrNotExist) && kerr != nil {
		return nil, nil, kerr
	}
	if cerr == nil || kerr == nil {
		// one of the two alone: never made over, which would orphan every
		// certificate the CA issued
		return nil, nil, fmt.Errorf("magpie's mTLS CA in %s is missing its certificate or its key", s.Dir)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: strings.TrimSpace("magpie CA " + host), Organization: []string{"magpie"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLife),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.caKeyFile(), pemOf("PRIVATE KEY", kder), 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.caFile(), pemOf("CERTIFICATE", der), 0o644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func parsePair(cb, kb []byte) (*x509.Certificate, crypto.Signer, error) {
	p, _ := pem.Decode(cb)
	if p == nil {
		return nil, nil, errors.New("magpie's mTLS CA certificate isn't PEM")
	}
	cert, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return nil, nil, err
	}
	k, _ := pem.Decode(kb)
	if k == nil {
		return nil, nil, errors.New("magpie's mTLS CA key isn't PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(k.Bytes)
	if err != nil {
		return nil, nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("magpie's mTLS CA key can't sign")
	}
	return cert, signer, nil
}

// Pool is the CA as a pool a server verifies client certificates by.
func (s Store) Pool() (*x509.CertPool, error) {
	ca, _, err := s.CA()
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, nil
}

// Issued is a client certificate as it was issued, with its key: the
// only time the key is seen, as the store keeps none.
type Issued struct {
	Client
	Cert, Key, CA []byte // PEM
}

// Issue signs a client certificate for the device name. One issued
// before under the same name, still good, is revoked: the name is the
// device's, and a device issued again (a lost or new one) has the one
// certificate.
func (s Store) Issue(name string) (Issued, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Issued{}, errors.New("a client certificate needs a name, the device's: magpie mtls issue <name>")
	}
	if strings.ContainsAny(name, "/\\:*?\"<>|") || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' }) {
		return Issued{}, fmt.Errorf("%q: a name can't hold / \\ : * ? \" < > | or control characters", name)
	}
	mu.Lock()
	defer mu.Unlock()
	ca, caKey, err := s.ca()
	if err != nil {
		return Issued{}, err
	}
	clients, err := s.clients()
	if err != nil {
		return Issued{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Issued{}, err
	}
	now := time.Now()
	sn := serial()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"magpie"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(clientLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return Issued{}, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Issued{}, err
	}
	for i := range clients {
		if strings.EqualFold(clients[i].Name, name) && clients[i].Revoked.IsZero() {
			clients[i].Revoked = now
		}
	}
	c := Client{Name: name, Serial: sn.Text(16), Issued: now, Expires: tmpl.NotAfter}
	clients = append(clients, c)
	if err := s.saveClients(clients); err != nil {
		return Issued{}, err
	}
	return Issued{Client: c, Cert: pemOf("CERTIFICATE", der), Key: pemOf("PRIVATE KEY", kder), CA: pemOf("CERTIFICATE", ca.Raw)}, nil
}

// List is every certificate issued, the newest first.
func (s Store) List() ([]Client, error) {
	mu.Lock()
	defer mu.Unlock()
	c, err := s.clients()
	slices.SortStableFunc(c, func(a, b Client) int { return b.Issued.Compare(a.Issued) })
	return c, err
}

// Revoke takes back the good certificates of the device name, or the one
// whose serial name is. A connection already made with one ends with its
// next request: the gateway asks Admits on each handshake and each
// request.
func (s Store) Revoke(name string) (int, error) {
	name = strings.TrimSpace(name)
	mu.Lock()
	defer mu.Unlock()
	clients, err := s.clients()
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range clients {
		if (strings.EqualFold(clients[i].Name, name) || strings.EqualFold(clients[i].Serial, name)) && clients[i].Revoked.IsZero() {
			clients[i].Revoked = time.Now()
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no certificate issued to %q is in use: magpie mtls list", name)
	}
	return n, s.saveClients(clients)
}

// Admits says whether cert, a client certificate the CA's pool verified,
// is let in: issued here and not revoked. One the list doesn't hold (its
// file lost, or edited) is refused, as is any when the list can't be
// read.
func (s Store) Admits(cert *x509.Certificate) error {
	clients, err := s.cachedClients()
	if err != nil {
		return fmt.Errorf("the client certificates issued can't be read: %w", err)
	}
	sn := cert.SerialNumber.Text(16)
	for _, c := range clients {
		if c.Serial == sn {
			if !c.Revoked.IsZero() {
				return fmt.Errorf("the client certificate %q was revoked", c.Name)
			}
			return nil
		}
	}
	return fmt.Errorf("the client certificate %q isn't one this magpie issued", cert.Subject.CommonName)
}

var cache struct {
	sync.Mutex
	gen     int64 // saves in this process, so one within the file's mtime tick is seen
	path    string
	mod     time.Time
	size    int64
	clients []Client
}

// cachedClients is clients.json, parsed again only when it changed: it is
// asked on every handshake.
func (s Store) cachedClients() ([]Client, error) {
	fi, err := os.Stat(s.clientsFile())
	if err != nil {
		return nil, err
	}
	cache.Lock()
	defer cache.Unlock()
	if cache.path == s.clientsFile() && cache.mod.Equal(fi.ModTime()) && cache.size == fi.Size() && cache.gen == saves.Load() {
		return cache.clients, nil
	}
	gen := saves.Load()
	mu.Lock()
	c, err := s.clients()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	cache.path, cache.mod, cache.size, cache.clients, cache.gen = s.clientsFile(), fi.ModTime(), fi.Size(), c, gen
	return c, nil
}

func (s Store) clients() ([]Client, error) {
	b, err := os.ReadFile(s.clientsFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Client
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", s.clientsFile(), err)
	}
	return out, nil
}

func (s Store) saveClients(c []Client) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	defer saves.Add(1)
	return writeFile(s.clientsFile(), append(b, '\n'), 0o600)
}

var saves atomic.Int64

// ServerCert is a server certificate the CA signs for names and ips, with
// its key: made in memory each time the gateway starts serving, never
// written.
func (s Store) ServerCert(names []string, ips []net.IP) (tls.Certificate, error) {
	ca, caKey, err := s.CA()
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	cn := "magpie"
	if len(names) > 0 {
		cn = names[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"magpie"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(serverLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// Export writes what a device needs to dir/magpie-<name>: client.pem,
// client-key.pem (0600) and ca.pem, and returns that folder.
func Export(iss Issued, dir string) (string, error) {
	out := filepath.Join(dir, "magpie-"+strings.ReplaceAll(iss.Name, " ", "-"))
	if _, err := os.Stat(out); err == nil {
		for i := 2; ; i++ {
			o := fmt.Sprintf("%s-%d", out, i)
			if _, err := os.Stat(o); errors.Is(err, os.ErrNotExist) {
				out = o
				break
			}
		}
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	for _, f := range []struct {
		name string
		b    []byte
		mode os.FileMode
	}{{"client.pem", iss.Cert, 0o644}, {"client-key.pem", iss.Key, 0o600}, {"ca.pem", iss.CA, 0o644}} {
		if err := writeFile(filepath.Join(out, f.name), f.b, f.mode); err != nil {
			return "", err
		}
	}
	return out, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

func pemOf(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// WriteFile is writeFile, for the certificate Settings imports.
func WriteFile(path string, b []byte, mode os.FileMode) error { return writeFile(path, b, mode) }

// writeFile writes b to path through a temp file renamed over it, so a
// reader never sees half of one.
func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
