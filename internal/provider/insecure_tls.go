package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TakesInsecureTLS is whether the provider may have its certificate left
// unchecked (InsecureTLS, #2383): only one the user added with a base URL
// of their own. A subscription's or a plugin's account, a preset's (whose
// vendor serves a certificate that checks) and a built-in's never do,
// whatever providers.json says.
func (p Provider) TakesInsecureTLS() bool {
	return p.Account == nil && p.Preset == "" && !p.IsVertex() && hasEndpoint(p) && !subscriptionID(p.ID)
}

// TakesClientCert is whether the provider may show a client certificate
// of its own, or trust a server CA of its own (ClientCert, #5596): one the
// user added with a base URL of its own, as TakesInsecureTLS, and Remote
// magpie, whose other magpie may ask for one (`magpie mtls`).
func (p Provider) TakesClientCert() bool {
	return p.Account == nil && (p.Preset == "" || p.IsRemoteMagpie()) && !p.IsVertex() && hasEndpoint(p) && !subscriptionID(p.ID)
}

// UncheckedHosts are the base URLs whose host's certificate is taken as it
// is when the provider has InsecureTLS: its own endpoints, never a models,
// balance or image URL elsewhere.
func (p Provider) UncheckedHosts() []string {
	var out []string
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide} {
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}

// CertPath is a client certificate's, key's or CA's file as ClientCert
// keeps it: ~ taken as home, and a whole path, since the gateway reads it
// from wherever it was started.
func CertPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if p == "" {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s: give the file's whole path", p)
	}
	return filepath.Clean(p), nil
}
