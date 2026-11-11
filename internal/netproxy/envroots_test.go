package netproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// signedBy is a server certificate for 127.0.0.1 from a CA made here, with
// the CA as PEM: a root no system trusts, as a HTTPS-inspecting tool's.
func signedBy(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	ca, leaf := key(), key()
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test inspector"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &ca.PublicKey, ca)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &leaf.PublicKey, ca)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leaf}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func fetchVia(t *testing.T, tr *http.Transport, url string) error {
	t.Helper()
	res, err := (&http.Client{Transport: tr}).Get(url)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// A root the user named in SSL_CERT_FILE or NODE_EXTRA_CA_CERTS (or a file
// of SSL_CERT_DIR) is trusted by a transport TrustEnvRoots was given, on
// every OS; one nobody named is still refused.
func TestTrustEnvRoots(t *testing.T) {
	cert, ca := signedBy(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "inspector.pem")
	if err := os.WriteFile(file, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "x.pem"), []byte("not a certificate"), 0o600)

	for _, env := range []struct{ name, val string }{{"SSL_CERT_FILE", file}, {"NODE_EXTRA_CA_CERTS", file}, {"SSL_CERT_DIR", dir}, {"SSL_CERT_DIR", other + string(os.PathListSeparator) + dir}} {
		t.Run(env.name, func(t *testing.T) {
			t.Setenv("SSL_CERT_FILE", "")
			t.Setenv("NODE_EXTRA_CA_CERTS", "")
			t.Setenv("SSL_CERT_DIR", "")
			plain := &http.Transport{}
			if err := fetchVia(t, plain, srv.URL); err == nil {
				t.Fatal("a CA nobody trusts was taken")
			}
			t.Setenv(env.name, env.val)
			tr := &http.Transport{}
			TrustEnvRoots(tr)
			if err := fetchVia(t, tr, srv.URL); err != nil {
				t.Fatalf("a CA named in %s is refused: %v", env.name, err)
			}
			if err := fetchVia(t, plain, srv.URL); err == nil {
				t.Fatal("a transport TrustEnvRoots was not given took the CA")
			}
		})
	}

	t.Run("none named", func(t *testing.T) {
		t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "missing.pem"))
		t.Setenv("NODE_EXTRA_CA_CERTS", filepath.Join(other, "x.pem"))
		t.Setenv("SSL_CERT_DIR", "")
		tr := &http.Transport{}
		TrustEnvRoots(tr)
		if tr.TLSClientConfig != nil {
			t.Fatal("a transport was changed with no certificate named")
		}
		if err := fetchVia(t, tr, srv.URL); err == nil {
			t.Fatal("a CA nobody trusts was taken")
		}
	})
}
