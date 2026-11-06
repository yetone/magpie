package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/provider"
)

// identityHeaders are what a proxy or tunnel in front of magpie says of
// the caller: where it is, and with Tailscale Serve who (the user's
// email, name and picture).
var identityHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "Via",
	"Cf-Connecting-IP", "Cf-Ipcountry", "True-Client-IP",
	"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic",
}

// identityHeadersOn is a request's headers: every identity header, and
// one of the caller's own that goes on.
func identityHeadersOn() []string {
	var hdr []string
	for _, k := range identityHeaders {
		hdr = append(hdr, k, "ident-"+k)
	}
	return append(hdr, "X-Custom", "kept")
}

func leakedIdentity(h http.Header) []string {
	var out []string
	for _, k := range identityHeaders {
		if h.Get(k) != "" {
			out = append(out, k+": "+h.Get(k))
		}
	}
	return out
}

// A request through a proxy in front of magpie (Tailscale Serve, a
// Cloudflare Tunnel) reaches the vendor on the native route without what
// the proxy said of the caller: not its address, and not the Tailscale
// user's email, name or picture. The caller's own headers still go on.
func TestNativeAnyDropsProxyIdentity(t *testing.T) {
	fresh(t)
	// the proxy here signs its clients in itself (lan.go's trustProxy)
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	h := nativeServe(t)
	rec := nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+"vendor/v1/x", "127.0.0.1:50000", `{"model":"m"}`, identityHeadersOn()...)
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	seen := up.asked()
	if len(seen) != 1 || seen[0].header.Get("X-Custom") != "kept" {
		t.Fatalf("vendor asked %+v", seen)
	}
	if l := leakedIdentity(seen[0].header); len(l) > 0 {
		t.Fatalf("the vendor was told %v", l)
	}
}

// The same on the routes that pass the caller's headers on as they came:
// the Codex relay to the ChatGPT backend (copyHeaders, which the
// gateway's Codex passthrough and its model list share), and /mcp/<server>
// to a remote MCP server.
func TestPassthroughDropsProxyIdentity(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	var mu sync.Mutex
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	hdr := map[string]string{"Authorization": "Bearer chatgpt-token", "Content-Type": "application/json"}
	on := identityHeadersOn()
	for i := 0; i+1 < len(on); i += 2 {
		hdr[on[i]] = on[i+1]
	}
	rec := relayDo(CodexRelay(), "POST", "127.0.0.1:3425", CodexPath+"/responses", `{"model":"gpt-5.5","input":"hi","stream":true}`, hdr)
	if rec.Code != 200 {
		t.Fatalf("relay: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if got.Get("X-Custom") != "kept" {
		t.Errorf("relay: the caller's own header didn't go on: %v", got)
	}
	if l := leakedIdentity(got); len(l) > 0 {
		t.Errorf("relay: the ChatGPT backend was told %v", l)
	}
	mu.Unlock()

	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	req, _ := http.NewRequest("POST", gw.URL+"/mcp/neon", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+Token)
	for i := 0; i+1 < len(on); i += 2 {
		req.Header.Set(on[i], on[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("mcp: %d %s", resp.StatusCode, b)
	}
	if f.Seen.Get("X-Custom") != "kept" {
		t.Errorf("mcp: the caller's own header didn't go on: %v", f.Seen)
	}
	if l := leakedIdentity(f.Seen); len(l) > 0 {
		t.Errorf("mcp: the MCP server was told %v", l)
	}
}
