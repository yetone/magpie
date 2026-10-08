package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// A tunnel or proxy on this computer (cloudflared, ngrok, Tailscale serve,
// frp, Caddy) reaches the gateway on loopback for clients anywhere (#1022).
// What it forwards carries a forwarding header, and is answered as from
// another machine: refused while magpie isn't shared, and with sharing on
// only with an enabled gateway key — on every route, the model calls, the
// quotas, the MCP sign-ins. An agent on loopback without those headers is
// let in as before, with any token.
func TestProxiedLoopbackNeedsTheKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	reached := ""
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = access.Caller(r.Context()).KeyID + "|" + r.Header.Get("Authorization")
	}))
	call := func(path string, hdr ...string) (int, string) {
		reached = ""
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = "127.0.0.1:51234"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	// what each puts on a request it passes on
	tunnels := map[string][]string{
		"cloudflared":     {"Cf-Connecting-IP", "203.0.113.9", "X-Forwarded-For", "203.0.113.9", "Cf-Ray", "8c1f-SJC"},
		"ngrok":           {"X-Forwarded-For", "198.51.100.4", "X-Forwarded-Proto", "https"},
		"nginx":           {"X-Real-IP", "198.51.100.4"},
		"RFC 7239":        {"Forwarded", "for=198.51.100.4;proto=https"},
		"Akamai":          {"True-Client-IP", "198.51.100.4"},
		"Tailscale serve": {"Tailscale-User-Login", "someone@example.com"},
	}

	if c, _ := call("/v1/messages", "Authorization", "Bearer magpie"); c != 200 || reached != "|Bearer magpie" {
		t.Fatal("an agent on loopback:", c, reached)
	}
	for name, hdr := range tunnels {
		c, body := call("/v1/messages", append([]string{"Authorization", "Bearer magpie"}, hdr...)...)
		if c != http.StatusForbidden || reached != "" || !strings.Contains(body, "proxy or tunnel") || !strings.Contains(body, "Share on local network") {
			t.Fatalf("%s, magpie not shared: %d %q reached=%q", name, c, body, reached)
		}
	}

	keys, secrets := newCaller(t, "Tunnel client") // shares magpie
	for name, hdr := range tunnels {
		for _, bad := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "sk-magpie-wrong"}} {
			if c, _ := call("/v1/messages", append(append([]string{}, bad...), hdr...)...); c != http.StatusUnauthorized || reached != "" {
				t.Fatalf("%s with %v: %d reached=%q", name, bad, c, reached)
			}
		}
		if c, _ := call("/v1/messages", append([]string{"Authorization", "Bearer " + secrets[0]}, hdr...)...); c != 200 || reached != keys[0].ID+"|Bearer magpie" {
			t.Fatalf("%s with the key: %d reached=%q", name, c, reached)
		}
	}
	if c, _ := call("/v1/messages", "Authorization", "Bearer anything"); c != 200 {
		t.Fatal("an agent on loopback, shared:", c)
	}
	access.Update("off-key", access.Change{Key: keys[0].ID})
	if c, _ := call("/v1/messages", "Authorization", "Bearer "+secrets[0], "Cf-Connecting-IP", "203.0.113.9"); c != http.StatusUnauthorized {
		t.Fatal("a disabled key through the tunnel:", c)
	}

	// a proxy that signs its own clients in is said to be trusted
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	s := settings.Load()
	s.LAN = false
	settings.Save(s)
	if c, _ := call("/v1/messages", "Authorization", "Bearer magpie", "X-Forwarded-For", "10.0.0.2"); c != 200 {
		t.Fatal("MAGPIE_TRUST_PROXY:", c)
	}
	t.Setenv("MAGPIE_TRUST_PROXY", "")

	// MAGPIE_ADDR on loopback is closed to a tunnel; one open to the
	// network is open to it as to everyone else
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:4555")
	if c, _ := call("/v1/messages", "Cf-Connecting-IP", "203.0.113.9"); c != http.StatusForbidden {
		t.Fatal("MAGPIE_ADDR on loopback, through a tunnel:", c)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:4555")
	if c, _ := call("/v1/messages", "Cf-Connecting-IP", "203.0.113.9"); c != 200 {
		t.Fatal("MAGPIE_ADDR open, through a tunnel:", c)
	}
}

// The gateway's own routes that answer this computer only — quotas, the
// MCP sign-ins, the Omarchy theme — don't take a tunnel's request for one.
func TestProxiedLoopbackRoutes(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	h := lanGuard(New().Handler())
	call := func(method, path string, hdr ...string) int {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "127.0.0.1:51234"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	cf := []string{"Cf-Connecting-IP", "203.0.113.9", "Host", "magpie.example.com"}
	if c := call("GET", "/v1/magpie/quotas"); c != 200 {
		t.Fatal("quotas on loopback:", c)
	}
	for _, p := range []string{"/v1/magpie/quotas", "/v1/magpie/quotas/history", "/v1/models", "/v1/magpie/omarchy"} {
		if c := call("GET", p, cf...); c != http.StatusForbidden {
			t.Fatal(p, "through a tunnel, not shared:", c)
		}
	}
	if c := call("POST", "/mcp/docs", cf...); c != http.StatusForbidden {
		t.Fatal("MCP through a tunnel, not shared:", c)
	}
	_, secrets := newCaller(t, "Tunnel client")
	if c := call("GET", "/v1/magpie/quotas", cf...); c != http.StatusUnauthorized {
		t.Fatal("quotas through a tunnel without the key:", c)
	}
	if c := call("GET", "/v1/magpie/quotas", append([]string{"x-api-key", secrets[0]}, cf...)...); c != 200 {
		t.Fatal("quotas through a tunnel with the key:", c)
	}
	if c := call("GET", "/v1/magpie/omarchy", append([]string{"x-api-key", secrets[0]}, cf...)...); c != http.StatusForbidden {
		t.Fatal("the Omarchy theme is this machine's only:", c)
	}
	if c := call("POST", "/mcp/docs", cf...); c != http.StatusUnauthorized {
		t.Fatal("MCP through a tunnel without the key:", c)
	}
}

// Sharing keeps a host MAGPIE_ADDR names (#1112): a gateway kept on
// loopback behind Tailscale Serve asks what Serve forwards for a gateway
// key once shared, and still listens on loopback alone; the addresses it
// offers other machines are the one it listens on, or none on loopback.
func TestSharedKeepsPinnedAddr(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:4555")
	reached := false
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	call := func(hdr ...string) int {
		reached = false
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = "127.0.0.1:51234"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	serve := []string{"Tailscale-User-Login", "someone@example.com", "X-Forwarded-For", "100.64.0.7"}

	_, secrets := newCaller(t, "Tailnet laptop") // shares magpie
	if a := listenAddr(); a != "127.0.0.1:4555" {
		t.Fatal("shared, MAGPIE_ADDR on loopback, listens on", a)
	}
	if u := LANURLs(); len(u) != 0 {
		t.Fatal("shared on loopback offers", u)
	}
	for _, bad := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "sk-magpie-wrong"}} {
		if c := call(append(append([]string{}, bad...), serve...)...); c != http.StatusUnauthorized || reached {
			t.Fatalf("through Serve with %v: %d reached=%v", bad, c, reached)
		}
	}
	if c := call(append([]string{"Authorization", "Bearer " + secrets[0]}, serve...)...); c != 200 || !reached {
		t.Fatal("through Serve with the key:", c)
	}
	if c := call("Authorization", "Bearer anything"); c != 200 {
		t.Fatal("an agent on loopback:", c)
	}

	t.Setenv("MAGPIE_ADDR", "100.64.0.1:4555")
	if a := listenAddr(); a != "100.64.0.1:4555" {
		t.Fatal("shared, MAGPIE_ADDR on one address, listens on", a)
	}
	if u := LANURLs(); len(u) != 1 || u[0] != "http://100.64.0.1:4555" {
		t.Fatal("shared on one address offers", u)
	}
	// every interface, said or left to Settings, is every interface still
	for _, a := range []string{"0.0.0.0:4555", "[::]:4555", ":4555"} {
		t.Setenv("MAGPIE_ADDR", a)
		if got := listenAddr(); got != "0.0.0.0:4555" {
			t.Fatal("shared, MAGPIE_ADDR", a, "listens on", got)
		}
	}
	t.Setenv("MAGPIE_ADDR", "")
	if got := listenAddr(); got != "0.0.0.0:"+Port() {
		t.Fatal("shared from Settings listens on", got)
	}
}
