package netproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/mtls"
)

func pinServer(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, answer) }))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{freshCert(t)}}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// A pinned server certificate (Zhangzhangi on X) is taken at the hosts it
// is given for, unchecked otherwise: another host is checked as before,
// another certificate at a pinned host is refused, a certificate a
// trusted CA signed included, and a request made for another provider
// (With) doesn't carry the pin.
func TestPinOnlyThatCertificate(t *testing.T) {
	own := pinServer(t, "own")
	other := pinServer(t, "other")
	d := Dispatch(&http.Transport{Proxy: Func, ForceAttemptHTTP2: true}).(*dispatch)
	t.Cleanup(d.CloseIdleConnections)
	c := &http.Client{Transport: d}
	bg := context.Background()
	pin := CertSHA256(own.Certificate())

	if _, err := body(t, c, bg, own.URL); !certRefused(err) {
		t.Fatalf("not pinned: %v", err)
	}
	ctx := WithClientCert(bg, "", "", "", ColonHex(pin), own.URL+"/v1")
	if got, err := body(t, c, ctx, own.URL+"/v1/models"); err != nil || got != "own" {
		t.Fatalf("pinned: %q, %v", got, err)
	}
	if _, err := body(t, c, ctx, other.URL); !certRefused(err) {
		t.Fatalf("another host under the same ctx: %v", err)
	}
	if _, err := body(t, c, With(ctx, ""), own.URL); !certRefused(err) {
		t.Fatalf("made for another provider: %v", err)
	}
	var mis *PinMismatchError
	wrong := WithClientCert(bg, "", "", "", pin, other.URL)
	if _, err := body(t, c, wrong, other.URL); !errors.As(err, &mis) || mis.Got != CertSHA256(other.Certificate()) {
		t.Fatalf("another certificate at a pinned host: %v", err)
	}

	// a server the given CA signs, pinned to another certificate: the pin
	// wins over the CA
	st := mtls.Store{Dir: t.TempDir()}
	signed := mtlsServer(t, st, false, nil)
	_, _, ca := device(t, st, "x")
	if _, err := body(t, c, WithClientCert(bg, "", "", ca, "", signed.URL), signed.URL); err != nil {
		t.Fatalf("CA alone: %v", err)
	}
	if _, err := body(t, c, WithClientCert(bg, "", "", ca, pin, signed.URL), signed.URL); !errors.As(err, &mis) {
		t.Fatalf("CA and another pin: %v", err)
	}
}

func TestNormalPin(t *testing.T) {
	h := strings.Repeat("0a", 32)
	for in, want := range map[string]string{
		h:                               h,
		strings.ToUpper(h):              h,
		ColonHex(h):                     h,
		"sha256:" + ColonHex(h):         h,
		" " + strings.ToUpper(h) + "\n": h,
		"abcd":                          "",
		strings.Repeat("zz", 32):        "",
		"":                              "",
	} {
		if got := NormalPin(in); got != want {
			t.Errorf("NormalPin(%q) = %q, want %q", in, got, want)
		}
	}
}

// freshCert is a self-signed certificate of its own for 127.0.0.1:
// httptest's servers all show one built-in certificate, so a server that
// must show another needs this.
func freshCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "api.magpie.local"},
		DNSNames:     []string{"api.magpie.local"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
