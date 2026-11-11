package provider

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
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

// PinHosts are the base URLs a trusted server certificate (ServerPin) is
// taken at: every endpoint of a provider that takes a client certificate
// (TakesClientCert), and of a preset's only the https:// addresses the user
// gave it at a host of their own (Ollama on another computer, an Azure
// resource), never the vendor's own hosts (Zhangzhangi on X). None for a
// subscription's or a plugin's account.
func (p Provider) PinHosts() []string {
	if p.TakesClientCert() {
		return p.UncheckedHosts()
	}
	if p.Account != nil || p.Preset == "" || p.IsVertex() || subscriptionID(p.ID) {
		return nil
	}
	pr := Preset(p.Preset)
	if pr == nil {
		return nil
	}
	own := map[string]bool{}
	for _, u := range []string{pr.Chat, pr.Responses, pr.Anthropic, pr.Decide} {
		own[pinName(u)] = true
	}
	for _, r := range pr.Regions {
		for _, u := range []string{r.Chat, r.Responses, r.Anthropic} {
			own[pinName(u)] = true
		}
	}
	var out []string
	for _, u := range p.UncheckedHosts() {
		if h := pinName(u); h != "" && !own[h] && strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "https://") {
			out = append(out, u)
		}
	}
	return out
}

// TakesServerPin is whether the provider may trust a server certificate
// of its own by fingerprint (ServerPin): it has an address PinHosts takes.
func (p Provider) TakesServerPin() bool { return len(p.PinHosts()) > 0 }

// pinName is u's host name as PinHosts compares them.
func pinName(u string) string { return strings.ToLower(hostnameOf(strings.TrimSpace(u))) }

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

// ServerCertSeen is the certificate a provider's HTTPS host shows, as its
// editor offers it to be trusted (ServerPin).
type ServerCertSeen struct {
	// Host is the host:port asked
	Host string `json:"host"`
	// SHA256 is its fingerprint as ServerPin keeps it
	SHA256 string `json:"sha256"`
	// Subject and Issuer are the names it is issued to and by; Names its
	// DNS names and addresses
	Subject string   `json:"subject"`
	Issuer  string   `json:"issuer"`
	Names   []string `json:"names,omitempty"`
	// NotBefore and NotAfter are when it is good from and until
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// Checks is whether this computer would take it without a pin: then
	// there's nothing to trust; Why is why not
	Checks bool   `json:"checks"`
	Why    string `json:"why,omitempty"`
}

// PeekServerCert is the certificate p's first HTTPS base URL shows,
// asked through p's own proxy (and with its client certificate, when the
// server asks for one) with the certificate taken unchecked, so the user
// can see its fingerprint before trusting it (trust on first use). Its
// pin, if it has one, plays no part.
func (p Provider) PeekServerCert(ctx context.Context) (ServerCertSeen, error) {
	var base *url.URL
	for _, h := range p.PinHosts() {
		if u, err := url.Parse(strings.TrimSpace(h)); err == nil && strings.EqualFold(u.Scheme, "https") && u.Hostname() != "" {
			base = u
			break
		}
	}
	if base == nil {
		return ServerCertSeen{}, errors.New("it has no https:// address: there's no certificate to trust")
	}
	p.ServerPin = ""
	ctx = netproxy.WithUncheckedTLS(p.Via(ctx), base.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return ServerCertSeen{}, err
	}
	c := &http.Client{Transport: http.DefaultClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		return ServerCertSeen{}, err
	}
	res.Body.Close()
	if res.TLS == nil || len(res.TLS.PeerCertificates) == 0 {
		return ServerCertSeen{}, errors.New("it answered without a certificate")
	}
	leaf := res.TLS.PeerCertificates[0]
	seen := ServerCertSeen{Host: base.Host, SHA256: netproxy.CertSHA256(leaf), Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(), Names: leaf.DNSNames, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
	for _, ip := range leaf.IPAddresses {
		seen.Names = append(seen.Names, ip.String())
	}
	mid := x509.NewCertPool()
	for _, c := range res.TLS.PeerCertificates[1:] {
		mid.AddCert(c)
	}
	opts := x509.VerifyOptions{DNSName: base.Hostname(), Intermediates: mid}
	if p.ServerCA != "" {
		// a server its own CA signs is checked already: nothing to pin
		if pool, err := netproxy.CAPool(p.ServerCA); err == nil {
			opts.Roots = pool
		}
	}
	if _, err := leaf.Verify(opts); err != nil {
		seen.Why = err.Error()
	} else {
		seen.Checks = true
	}
	return seen, nil
}
