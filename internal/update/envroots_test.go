package update

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The updater's client trusts a root the user named in SSL_CERT_FILE, as a
// HTTPS-inspecting tool (dev-sidecar) tells its users to, on macOS and
// Windows too, where Go reads only the system's store.
func TestUpdaterTrustsSSLCertFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	t.Setenv("SSL_CERT_DIR", "")
	if res, err := (&http.Client{Transport: proxied()}).Get(srv.URL); err == nil {
		res.Body.Close()
		t.Fatal("a certificate nobody trusts was taken")
	}
	t.Setenv("SSL_CERT_FILE", file)
	res, err := (&http.Client{Transport: proxied()}).Get(srv.URL)
	if err != nil {
		t.Fatalf("the CA in SSL_CERT_FILE is refused: %v", err)
	}
	res.Body.Close()
}
