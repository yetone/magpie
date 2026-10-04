//go:build !darwin && !windows

package plugin

import "os"

// systemCABundle is the distribution's CA bundle, where an installed root
// ends up (update-ca-certificates, update-ca-trust): Bun reads it as it is.
// SSL_CERT_FILE, as Go and OpenSSL take it, comes first.
func systemCABundle() string {
	files := []string{
		os.Getenv("SSL_CERT_FILE"),
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
		"/etc/pki/tls/cacert.pem",
		"/etc/ssl/cert.pem",
	}
	for _, f := range files {
		if fi, err := os.Stat(f); f != "" && err == nil && fi.Mode().IsRegular() {
			return f
		}
	}
	return ""
}

func systemRoots() [][]byte { return nil }
