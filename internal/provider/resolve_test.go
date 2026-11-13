package provider

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A saved rule is held to what netproxy.CheckResolve takes, and only a
// provider the user pointed at their own server keeps one.
func TestResolveSaved(t *testing.T) {
	isolate(t)
	own := Provider{ID: "corp", Name: "Corp", Key: "k", Models: []string{"m"}, Chat: "https://api.example.com/v1",
		Resolve: map[string][]string{" API.example.com ": {"dns:gw.corp.test", "10.0.0.1", "10.0.0.1"}}}
	if err := Save(own); err != nil {
		t.Fatal(err)
	}
	got, _ := Find("corp")
	if r := got.Resolve["api.example.com"]; len(r) != 2 || r[0] != "dns:gw.corp.test" || r[1] != "10.0.0.1" {
		t.Errorf("kept %v", got.Resolve)
	}
	bad := own
	bad.ID = "bad"
	bad.Resolve = map[string][]string{"api.example.com": {"gateway.corp"}}
	if err := Save(bad); err == nil || !strings.Contains(err.Error(), "resolve: ") {
		t.Errorf("a place that is a bare name: %v", err)
	}
	sub := Provider{ID: "codex", Name: "Codex", Chat: "https://api.example.com/v1", Resolve: map[string][]string{"api.example.com": {"10.0.0.1"}}}
	if err := Save(sub); err == nil {
		t.Error("a subscription's id took a connect address")
	}
	vendor := Provider{ID: "v2", Name: "V", Key: "k", Models: []string{"m"}, Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Resolve: map[string][]string{"api.deepseek.com": {"10.0.0.1"}}}
	if err := Save(vendor); err == nil {
		t.Error("a preset at its vendor's own host took a connect address")
	}
}

// Via carries the rule to the requests made for the provider: the name in
// its base URL is connected to the address the rule gives, and the server
// is still asked for that name (SNI and Host). An account of a subscription
// takes no rule.
func TestResolveVia(t *testing.T) {
	outside := dialsOut(t)
	var sni, host string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { host = r.Host }))
	srv.EnableHTTP2 = false
	srv.StartTLS()
	t.Cleanup(srv.Close)
	srv.TLS.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) { sni = h.ServerName; return nil, nil }
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p := Provider{ID: "corp", Name: "Corp", Key: "k", InsecureTLS: true, Chat: "https://gw.invalid:" + port + "/v1",
		Resolve: map[string][]string{"gw.invalid": {"127.0.0.1"}}}
	if !p.TakesResolve() {
		t.Fatal("a provider with its own base URL takes a rule")
	}
	req, _ := http.NewRequestWithContext(p.Via(context.Background()), http.MethodGet, p.Chat, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if sni != "gw.invalid" || host != "gw.invalid:"+port {
		t.Errorf("SNI %q, Host %q: the URL's name goes to the server", sni, host)
	}
	if d := outside(); len(d) != 0 {
		t.Errorf("dialled %v: the rule's address only", d)
	}
	p.Account = &Account{}
	if p.TakesResolve() {
		t.Error("an account of a subscription takes a rule")
	}
}
