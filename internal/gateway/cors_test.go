package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A web page the user listed in Settings (#1051, SkyAerope: a preflight
// from http://localhost:3000 for /v1/models was answered 404 with no CORS
// headers) has its preflight answered and reads the reply, its calls with
// a gateway key; a page not listed gets no CORS headers, as before, nor
// does any page while none is listed; agents, which send no Origin, are
// untouched.
func TestCORSOrigins(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "Web app"})
	if err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	// from this computer, as a browser on it calls 127.0.0.1:3425
	do := func(method, path, origin, key string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = "127.0.0.1:50123"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	preflight := map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization, content-type"}

	// none listed: the reporter's preflight, as before
	if w := do("OPTIONS", "/v1/models", "http://localhost:3000", "", preflight); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code == http.StatusNoContent {
		t.Fatalf("none listed: %d %v", w.Code, w.Header())
	}

	s := settings.Load()
	if s.CORSOrigins, err = settings.CleanOrigins([]string{"http://LOCALHOST:3000/", "https://app.example.com:443"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	w := do("OPTIONS", "/v1/models", "http://localhost:3000", "", map[string]string{
		"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization, content-type, x-app, bad name;",
		"Access-Control-Request-Private-Network": "true"})
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") ||
		w.Header().Get("Access-Control-Allow-Headers") != "authorization, content-type, x-app" ||
		w.Header().Get("Access-Control-Allow-Private-Network") != "true" || w.Header().Get("Vary") != "Origin" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	// the call itself, with a gateway key: answered and readable
	if w := do("GET", "/v1/models", "http://localhost:3000", secret, nil); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("models: %d %v", w.Code, w.Header())
	}
	if w := do("POST", "/v1/chat/completions", "https://app.example.com", secret, nil); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("chat: %d %v %s", w.Code, w.Header(), w.Body)
	}
	// without one, or with a key that isn't a gateway key, it is refused —
	// readably, so the page can say why
	for _, key := range []string{"", "magpie", "sk-magpie-not-a-key"} {
		w := do("POST", "/v1/chat/completions", "http://localhost:3000", key, nil)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" || !strings.Contains(w.Body.String(), "gateway key") {
			t.Errorf("key %q: %d %v %s", key, w.Code, w.Header(), w.Body)
		}
	}
	// a key turned off is no key
	keys, _ := access.List()
	access.Update("off-key", access.Change{Key: keys[len(keys)-1].ID})
	if w := do("GET", "/v1/models", "http://localhost:3000", secret, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("a key turned off: %d", w.Code)
	}
	access.Update("on-key", access.Change{Key: keys[len(keys)-1].ID})

	// another page: no CORS headers, whatever it sends; a port or scheme
	// apart is another origin
	for _, o := range []string{"http://localhost:3001", "https://localhost:3000", "http://evil.example", "null"} {
		if w := do("OPTIONS", "/v1/models", o, "", preflight); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code == http.StatusNoContent {
			t.Errorf("%s preflight: %d %v", o, w.Code, w.Header())
		}
		if w := do("GET", "/v1/models", o, secret, nil); w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: %v", o, w.Header())
		}
	}
	// an agent: no Origin, no key, as before
	if w := do("POST", "/v1/chat/completions", "", "", nil); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("agent: %d %v", w.Code, w.Header())
	}
}

// On the real server lanGuard sits in front of Handler and, for a gateway
// key sent from this computer, records who called and puts magpie's own
// token in the key's place; a listed page's keyed call is still answered.
func TestCORSKeyThroughTheServer(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "Web app"})
	if err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.CORSOrigins = []string{"http://localhost:3000"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	srv := lanGuard(New().Handler())
	for _, key := range []string{secret, ""} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = "127.0.0.1:50123"
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		want := http.StatusOK
		if key == "" {
			want = http.StatusUnauthorized
		}
		if w.Code != want || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
			t.Errorf("key %q: %d %v %s", key, w.Code, w.Header(), w.Body)
		}
	}
}

// Any web page the user visits could make the browser send a "simple"
// text/plain POST to 127.0.0.1:3425, which on loopback needs no key, and
// spend the user's subscriptions without reading the reply. A page whose
// http(s) origin isn't listed, on this computer or the gateway's own
// address is refused on every route before anything reaches a vendor;
// pages on this computer, listed ones, the gateway's own origin, agents
// (no Origin) and desktop apps, webviews and extensions (null and
// non-http origins) are answered as before.
func TestForeignPagesRefused(t *testing.T) {
	fresh(t)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "Web app"})
	if err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.CORSOrigins = []string{"https://app.example.com"}
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	type call struct{ method, path, body string }
	msg := `{"model":"plan/m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	do := func(c call, origin, host, remote, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.RemoteAddr = remote
		if host != "" {
			r.Host = host
		}
		// what a page's fetch(..., {mode: "no-cors"}) or a form sends
		r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		lanGuard(h).ServeHTTP(w, r)
		return w
	}
	chat := call{"POST", "/v1/chat/completions", chatReq}
	routes := []call{
		chat,
		{"POST", "/chat/completions", chatReq},
		{"POST", "/v1/responses", `{"model":"plan/m1","input":"hi"}`},
		{"POST", "/responses", `{"model":"plan/m1","input":"hi"}`},
		{"GET", "/v1/responses", ""},
		{"POST", "/v1/messages", msg},
		{"POST", "/messages", msg},
		{"POST", "/v1/messages/count_tokens", msg},
		{"POST", "/v1/systemone", msg},
		{"POST", "/v1beta/models/plan/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"POST", "/v1/embeddings", `{"model":"plan/m1","input":"hi"}`},
		{"POST", "/v1/rerank", `{"model":"plan/m1","query":"hi","documents":["a"]}`},
		{"POST", "/v1/images/generations", `{"model":"plan/m1","prompt":"hi"}`},
		{"POST", "/v1/videos", `{"model":"plan/m1","prompt":"hi"}`},
		{"POST", CodexPath + "/responses", `{"model":"plan/m1","input":"hi"}`},
		{"POST", "/mcp/notes", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"POST", "/_magpie/claude-mcp/tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"POST", provider.RemoteRefreshPath, ""},
		{"GET", "/v1/magpie/quotas", ""},
		{"GET", "/v1/models", ""},
		{"OPTIONS", "/v1/chat/completions", ""},
	}
	const loop = "127.0.0.1:50123"
	for _, o := range []string{"https://evil.example", "http://evil.example:3425", "http://localhost.evil.example", "http://192.168.1.7:3000", "HTTPS://Evil.Example", "https://usemagpie.ai"} {
		for _, c := range routes {
			if w := do(c, o, "127.0.0.1:3425", loop, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "web page") || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("%s %s from %s: %d %s", c.method, c.path, o, w.Code, w.Body)
			}
		}
	}
	// a gateway key doesn't let an unlisted page in either
	if w := do(chat, "https://evil.example", "127.0.0.1:3425", loop, secret); w.Code != http.StatusForbidden {
		t.Errorf("unlisted page with a key: %d", w.Code)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a refused page reached the vendor %d times", n)
	}

	// answered as before
	for _, c := range []struct{ origin, host, remote, key string }{
		{"", "127.0.0.1:3425", loop, ""},                                             // every agent
		{"http://localhost:5173", "127.0.0.1:3425", loop, ""},                        // a page on this computer
		{"http://127.0.0.2:8080", "127.0.0.1:3425", loop, ""},                        // 127.0.0.0/8
		{"http://[::1]:3000", "[::1]:3425", "[::1]:50123", ""},                       // IPv6 loopback
		{"http://tauri.localhost", "127.0.0.1:3425", loop, ""},                       // Tauri's webview on Windows
		{"https://app.example.com", "127.0.0.1:3425", loop, secret},                  // listed, with its key
		{"http://192.168.1.5:3425", "192.168.1.5:3425", "192.168.1.9:50123", secret}, // the gateway's own origin, shared on the LAN
		{"https://magpie.example.com", "magpie.example.com", loop, ""},               // its own origin behind a port-443 name
		{"null", "127.0.0.1:3425", loop, ""},                                         // a sandboxed frame, a file:// page
		{"app://cherry", "127.0.0.1:3425", loop, ""},                                 // an Electron renderer
		{"file://", "127.0.0.1:3425", loop, ""},
		{"tauri://localhost", "127.0.0.1:3425", loop, ""},
		{"vscode-webview://1abc", "127.0.0.1:3425", loop, ""},               // an editor's webview
		{"chrome-extension://abcdefghijklmnop", "127.0.0.1:3425", loop, ""}, // an extension
	} {
		if w := do(chat, c.origin, c.host, c.remote, c.key); w.Code != 200 || !strings.Contains(w.Body.String(), "ok") {
			t.Errorf("%q to %s: %d %s", c.origin, c.host, w.Code, w.Body)
		}
	}
	// usemagpie.ai still reads the Omarchy theme, and only that
	if w := do(call{"GET", "/v1/magpie/omarchy", ""}, "https://usemagpie.ai", "127.0.0.1:3425", loop, ""); w.Code == http.StatusForbidden {
		t.Errorf("usemagpie.ai's Omarchy theme: %d %s", w.Code, w.Body)
	}
}
