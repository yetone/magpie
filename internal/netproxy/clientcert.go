package netproxy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type clientCertKey struct{}

// clientCert is a provider's own TLS identity (#5596, clxsh: a magpie at
// home behind an Nginx that asks for a client certificate, reached by
// Remote magpie from the office): the PEM certificate and key it shows
// the server, and the PEM CA its server's certificate may also be signed
// by (a magpie's own, `magpie mtls`), at the hosts of its own base URLs.
type clientCert struct {
	cert, key, ca, pin string
	hosts              map[string]bool
}

// WithClientCert is ctx for requests made on behalf of a provider that
// shows the server a certificate of its own (cert and key, PEM files), or
// trusts a CA of its own for the server's (ca, a PEM file), or both: a
// request through a transport Dispatch made to the host of one of bases,
// over https, goes through a transport kept for that identity alone,
// which shows the certificate when the server asks for one and takes a
// server certificate signed by ca beside the system's roots. Any other
// host — a redirect elsewhere, an image or a balance URL on another host
// — is sent without them, and an https:// proxy is refused for it, as the
// proxy's TLS would be dialled with them (clientTLS). With (a request
// made for another provider) drops it.
//
// pin, the SHA-256 of a server certificate the user trusted as it is
// (Zhangzhangi on X: a magpie at home on a domain of its own with a
// self-signed certificate), has the certificate's chain and name left
// unchecked at those hosts, and the handshake refused unless the
// server's own certificate is that one (pinTLS).
func WithClientCert(ctx context.Context, cert, key, ca, pin string, bases ...string) context.Context {
	c := clientCert{cert: strings.TrimSpace(cert), key: strings.TrimSpace(key), ca: strings.TrimSpace(ca), pin: NormalPin(pin), hosts: map[string]bool{}}
	if c.cert == "" || c.key == "" {
		c.cert, c.key = "", ""
	}
	if c.cert == "" && c.ca == "" && c.pin == "" {
		return ctx
	}
	for _, b := range bases {
		u, err := url.Parse(strings.TrimSpace(b))
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
			continue
		}
		c.hosts[hostKey(u)] = true
	}
	if len(c.hosts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, clientCertKey{}, &c)
}

func clientCertOf(ctx context.Context) *clientCert {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(clientCertKey{}).(*clientCert)
	return c
}

// clientRoute is the route's name for req's own TLS identity
// (WithClientCert), "" when it has none: the files, and when each was
// last changed, so a certificate renewed in place is dialled with anew,
// and the pinned server certificate.
func clientRoute(req *http.Request) string {
	c := clientCertOf(req.Context())
	if c == nil || req.URL == nil || !strings.EqualFold(req.URL.Scheme, "https") || !c.hosts[hostKey(req.URL)] {
		return ""
	}
	var b strings.Builder
	for _, f := range []string{c.cert, c.key, c.ca} {
		b.WriteString(f)
		if f != "" {
			if fi, err := os.Stat(f); err == nil {
				fmt.Fprintf(&b, "\x01%d.%d", fi.ModTime().UnixNano(), fi.Size())
			}
		}
		b.WriteByte(0)
	}
	b.WriteString(c.pin)
	return b.String()
}

// ErrClientCertOverHTTPSProxy is why a request made with a provider's own
// TLS identity isn't sent through an https:// proxy: the proxy's own TLS
// is dialled with the same config, so it could be shown the certificate,
// and its own be taken for being signed by the provider's CA.
var ErrClientCertOverHTTPSProxy = errors.New("a provider with a client certificate or a CA of its own can't be reached through an https:// proxy, which would be shown them too: give it an http:// or socks5:// proxy, or none")

// clientTLS has t, a clone kept for one TLS identity (clientRoute), show
// its certificate when a server asks for one and take a server's signed
// by its CA. The files are read here, when the transport is made; one
// that doesn't load fails each handshake with why.
func clientTLS(t *http.Transport, route string) {
	parts := strings.Split(route, "\x00")
	file := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		f, _, _ := strings.Cut(parts[i], "\x01")
		return f
	}
	cert, key, ca, pin := file(0), file(1), file(2), file(3)
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	cfg := t.TLSClientConfig
	if cert != "" {
		var once sync.Once
		var pair *tls.Certificate
		var perr error
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			once.Do(func() {
				c, err := tls.LoadX509KeyPair(cert, key)
				if err != nil {
					perr = fmt.Errorf("the client certificate %s and key %s don't load: %w", cert, key, err)
					return
				}
				pair = &c
			})
			return pair, perr
		}
	}
	if pin != "" {
		pinTLS(cfg, pin)
	}
	if ca != "" {
		pool, err := caPool(ca)
		if err != nil {
			cfg.InsecureSkipVerify = false
			cfg.VerifyConnection = func(tls.ConnectionState) error { return err }
		} else if !cfg.InsecureSkipVerify {
			cfg.RootCAs = pool
		}
	}
	if next := t.Proxy; next != nil {
		t.Proxy = func(req *http.Request) (*url.URL, error) {
			u, err := next(req)
			if err == nil && u != nil && strings.EqualFold(u.Scheme, "https") {
				return nil, ErrClientCertOverHTTPSProxy
			}
			return u, err
		}
	}
}

// CAPool is caPool for a caller outside: the system's roots with the
// PEM file ca's beside them.
func CAPool(ca string) (*x509.CertPool, error) { return caPool(ca) }

// caPool is the system's roots with the CA in the PEM file ca beside
// them: a server signed by either is taken.
func caPool(ca string) (*x509.CertPool, error) {
	b, err := os.ReadFile(ca)
	if err != nil {
		return nil, fmt.Errorf("the server CA %s can't be read: %w", ca, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("the server CA %s holds no PEM certificate", ca)
	}
	return pool, nil
}

// CheckClientCert says why a provider's TLS identity can't be used: a
// certificate without its key or a key without its certificate, a pair
// that doesn't load or has run out, a CA file with no certificate. nil
// with none set.
func CheckClientCert(cert, key, ca string) error {
	cert, key, ca = strings.TrimSpace(cert), strings.TrimSpace(key), strings.TrimSpace(ca)
	switch {
	case cert != "" && key == "":
		return fmt.Errorf("the client certificate %s needs its private key too", cert)
	case cert == "" && key != "":
		return fmt.Errorf("the private key %s needs its client certificate too", key)
	}
	if cert != "" {
		c, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return fmt.Errorf("the client certificate %s and key %s don't load: %w", cert, key, err)
		}
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil && time.Now().After(leaf.NotAfter) {
			return fmt.Errorf("the client certificate %s ran out on %s", cert, leaf.NotAfter.Format("2006-01-02"))
		}
	}
	if ca != "" {
		if _, err := caPool(ca); err != nil {
			return err
		}
	}
	return nil
}

// pinTLS has cfg take the server certificate whose SHA-256 is pin, and
// no other: its chain, its name and its dates aren't checked (a
// self-signed one, or one macOS calls not standards compliant), and any
// other certificate fails the handshake, a CA's that checks included.
func pinTLS(cfg *tls.Config, pin string) {
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the server showed no certificate")
		}
		if got := CertSHA256(cs.PeerCertificates[0]); got != pin {
			return &PinMismatchError{Host: cmp.Or(cs.ServerName, "the server"), Got: got, Want: pin}
		}
		return nil
	}
}

// PinMismatchError is why a server whose certificate was pinned isn't
// spoken to: it showed another one.
type PinMismatchError struct{ Host, Got, Want string }

func (e *PinMismatchError) Error() string {
	return fmt.Sprintf("%s showed a certificate other than the one trusted (SHA-256 %s, trusted %s): if the server's certificate was renewed, trust the new one in its editor; if not, another machine may be answering at its address", e.Host, ColonHex(e.Got), ColonHex(e.Want))
}

// CertSHA256 is c's fingerprint as a pin is kept: the SHA-256 of its DER,
// in lower-case hex.
func CertSHA256(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}

// NormalPin is a typed SHA-256 fingerprint as a pin is kept: lower-case
// hex, its colons, spaces and a "sha256:" before it taken out; "" for
// one that isn't 64 hex digits.
func NormalPin(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "sha256:"), "sha256/")
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// ColonHex is a pin as people read fingerprints: AB:CD:….
func ColonHex(pin string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pin); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(pin[i : i+2]))
	}
	return b.String()
}
