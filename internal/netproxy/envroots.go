package netproxy

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// envCAFiles are the variables a user points at a CA file (or folder) to
// have a program trust a root beyond the system's: SSL_CERT_FILE and
// SSL_CERT_DIR as OpenSSL, curl and Go on Linux take them, and
// NODE_EXTRA_CA_CERTS, which a HTTPS-inspecting tool such as dev-sidecar
// tells its users to set. Go on macOS and Windows reads none of them, only
// the system's store, so a root the user set there and not in the
// keychain was refused by magpie while every other program took it.
var envCAFiles = []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"}
var envCADirs = []string{"SSL_CERT_DIR"}

// TrustEnvRoots has t take, beside the system's roots, the roots in the
// files and folders those variables name. Nothing is taken off: a server
// signed by a root the system trusts is trusted as before, and one signed
// by none of them is refused as before. With none set, or none holding a
// certificate, t is left as it is.
func TrustEnvRoots(t *http.Transport) {
	if t.TLSClientConfig != nil && (t.TLSClientConfig.InsecureSkipVerify || t.TLSClientConfig.RootCAs != nil) {
		return
	}
	pool := envRoots(os.Getenv)
	if pool == nil {
		return
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	t.TLSClientConfig.RootCAs = pool
	// a custom TLS config turns HTTP/2 off unless it is asked for again
	t.ForceAttemptHTTP2 = true
}

// envRoots is the system's roots with the certificates in the files and
// folders getenv names; nil when it names none that holds one.
func envRoots(getenv func(string) string) *x509.CertPool {
	var files []string
	for _, n := range envCAFiles {
		if f := strings.TrimSpace(getenv(n)); f != "" {
			files = append(files, f)
		}
	}
	for _, n := range envCADirs {
		for _, d := range filepath.SplitList(getenv(n)) {
			if d = strings.TrimSpace(d); d == "" {
				continue
			}
			es, _ := os.ReadDir(d)
			for _, e := range es {
				files = append(files, filepath.Join(d, e.Name()))
			}
		}
	}
	var pool *x509.CertPool
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if !x509.NewCertPool().AppendCertsFromPEM(b) {
			continue
		}
		if pool == nil {
			if pool, _ = x509.SystemCertPool(); pool == nil {
				pool = x509.NewCertPool()
			}
		}
		pool.AppendCertsFromPEM(b)
	}
	return pool
}
