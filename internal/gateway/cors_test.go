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
	"github.com/yetone/magpie/internal/sessions"
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
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	srv := lanGuard(New().Handler())
	for _, key := range []string{secret, ""} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = "127.0.0.1:50123"
		r.Host = "127.0.0.1:3425"
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(SessionHeader, "browser-chat")
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
		if key != "" && (w.Header().Get(SessionHeader) != "browser-chat" || w.Header().Get("Access-Control-Expose-Headers") != SessionHeader) {
			t.Error("browser client cannot read the session its conversation is recorded under")
		}
	}
}

// Any web page the user visits could make the browser send a "simple"
// text/plain POST to 127.0.0.1:3425, which on loopback needs no key, and
// spend the user's subscriptions without reading the reply. A page whose
// http(s) origin isn't listed, on this computer or the gateway's own
// address is refused on every route before anything reaches a vendor;
// pages on this computer, listed ones, the gateway's own origin, agents
// (no Origin) and apps and extensions (non-http schemes) are answered as
// before. Origin "null" is a page's too (TestOpaqueOriginNeedsAKey): a
// key is asked of it, so it is gone from the rows below.
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
		{"POST", "/v1/decisions", `{"model":"plan/m1","input":"hi","questions":[{"type":"predicate","instructions":"is it?"}]}`},
		{"POST", "/v1/audio/transcriptions", "--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nplan/m1\r\n--b--\r\n"},
		{"POST", "/v1/audio/speech", `{"model":"plan/m1","input":"hi","voice":"coral"}`},
		{"POST", "/v1/images/generations", `{"model":"plan/m1","prompt":"hi"}`},
		{"POST", "/v1/videos", `{"model":"plan/m1","prompt":"hi"}`},
		{"POST", CodexPath + "/responses", `{"model":"plan/m1","input":"hi"}`},
		{"POST", "/mcp/notes", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"POST", "/_magpie/claude-mcp/tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"POST", provider.RemoteRefreshPath, ""},
		{"POST", provider.RemoteCodexResetPath + "?user=me@example.com", ""},
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
	// ... nor does one sandboxed frame, whose Origin "null" any page can
	// send: it is refused with the others, and asks for a gateway key
	if w := do(chat, "null", "127.0.0.1:3425", loop, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "gateway key") {
		t.Errorf("a sandboxed frame with no key: %d %s", w.Code, w.Body)
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
		{"http://tauri.localhost", "127.0.0.1:3425", loop, secret},                   // a localhost subdomain, with a key (keyless on Windows: TestLocalhostSubdomainNeedsAKey)
		{"https://app.example.com", "127.0.0.1:3425", loop, secret},                  // listed, with its key
		{"http://192.168.1.5:3425", "192.168.1.5:3425", "192.168.1.9:50123", secret}, // the gateway's own origin, shared on the LAN
		{"https://magpie.example.com", "magpie.example.com", loop, secret},           // its own origin behind a port-443 name, with a key (without one, a rebound page: TestReboundPageRefused)
		{"null", "127.0.0.1:3425", loop, secret},                                     // a sandboxed frame or a file:// page, with its key
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

// Origin "null" is not an app's: a sandboxed frame sends it on any web
// page's behalf (a file:// page and some webviews send it too), so one
// iframe on a page the user visits could send the "simple" text/plain
// POST and spend a subscription with no key, which is the case corsGuard
// exists for. It is held to an enabled gateway key, as a listed page is
// Real browsers proved the request and its null Origin arrive with no
// preflight (2026-10-15): a sandboxed frame on a public https page
// reached loopback in WebKit 27.2 — Chromium 156 and Firefox 157 refuse
// the cross-site request themselves (Local Network Access) — and a
// file:// page reached it in all three. A scheme only an app or an
// extension can send is answered as before.
func TestOpaqueOriginNeedsAKey(t *testing.T) {
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
	secret, err := access.Update("add-key", access.Change{Name: "Local page"})
	if err != nil {
		t.Fatal(err)
	}
	// what a browser sends from a sandboxed frame or an opened file: a
	// "simple" POST, so no preflight comes first
	do := func(method, path, origin, key string) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = "127.0.0.1:50123"
		r.Host = "127.0.0.1:3425"
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", "no-cors")
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.2 Safari/605.1.15")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		return w
	}
	for _, p := range []struct{ method, path string }{
		{"POST", "/v1/chat/completions"},
		{"POST", "/v1/messages"},
		{"POST", "/v1/images/generations"},
		{"POST", "/v1/embeddings"},
		{"POST", "/v1/decisions"},
		{"GET", "/v1/models"},
	} {
		if w := do(p.method, p.path, "null", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "gateway key") || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s %s with no key: %d %v %s", p.method, p.path, w.Code, w.Header(), w.Body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a sandboxed frame with no key reached the vendor %d times", hits.Load())
	}
	// with an enabled key it is answered as before, and reads nothing
	// either way (no CORS headers: only a listed origin the user named
	// gets those)
	if w := do("POST", "/v1/chat/completions", "null", secret); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") || w.Header().Get("Access-Control-Allow-Origin") != "" || hits.Load() != 1 {
		t.Errorf("with its key: %d %v %s", w.Code, w.Header(), w.Body)
	}
	// a key turned off is no key
	keys, _ := access.List()
	access.Update("off-key", access.Change{Key: keys[len(keys)-1].ID})
	if w := do("POST", "/v1/chat/completions", "null", secret); w.Code != http.StatusForbidden {
		t.Errorf("with a key turned off: %d %s", w.Code, w.Body)
	}
	access.Update("on-key", access.Change{Key: keys[len(keys)-1].ID})
	// a scheme only that app or extension can send is untouched
	for _, o := range []string{"app://cherry", "tauri://localhost", "vscode-webview://1abc", "chrome-extension://abcdefghijklmnop"} {
		if w := do("POST", "/v1/chat/completions", o, ""); w.Code != http.StatusOK {
			t.Errorf("%s as before: %d %s", o, w.Code, w.Body)
		}
	}
}

// A page at a subdomain of localhost (#2256, ggbdpq) is not this
// computer's in every browser: Chrome and Firefox resolve *.localhost to
// loopback by a rule of their own, but Safari and other WebKit views ask
// the system resolver, which before macOS 26 asks the network's DNS (WebKit
// bug 160504, closed when macOS 26 resolved it below WebKit). A resolver
// that answers such a name — a captive portal's, a hostile network's —
// serves the page from anywhere, and it then reaches the gateway two ways:
// cross-origin from http://evil.localhost with a "simple" text/plain POST,
// and rebound, at http://evil.localhost:3425 itself once the name turns to
// 127.0.0.1, where its GET carries no Origin. Both are held to an enabled
// gateway key, as Origin "null" is (#2242). localhost itself, which the
// system answers from its hosts file, is unchanged, and so are
// tauri.localhost and wails.localhost on Windows, where Tauri's and
// Wails' webviews serve themselves and no browser asks the DNS for them.
func TestLocalhostSubdomainNeedsAKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
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
	secret, err := access.Update("add-key", access.Change{Name: "Local page"})
	if err != nil {
		t.Fatal(err)
	}
	// what Safari sends: a "simple" POST, so no preflight comes first
	do := func(method, path, host, origin, key string) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = "127.0.0.1:50123"
		r.Host = host
		site := "same-origin"
		if origin != "" {
			r.Header.Set("Origin", origin)
			site = "cross-site"
		}
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Sec-Fetch-Mode", "no-cors")
		r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Safari/605.1.15")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		lanGuard(New().Handler()).ServeHTTP(w, r)
		return w
	}
	routes := []struct{ method, path string }{
		{"POST", "/v1/chat/completions"},
		{"POST", "/v1/messages"},
		{"POST", "/v1/responses"},
		{"POST", "/v1/embeddings"},
		{"GET", "/v1/models"},
		{"GET", "/v1/magpie/quotas"},
	}
	const gw = "127.0.0.1:3425"
	for _, os := range []string{"darwin", "linux", "windows"} {
		t.Run(os, func(t *testing.T) {
			was := webviewOS
			webviewOS = os
			t.Cleanup(func() { webviewOS = was })
			hits.Store(0)
			// cross-origin, from a page at any subdomain; on macOS and
			// Linux the two webview names too, which a hostile resolver
			// answers as readily
			pages := []string{"http://evil.localhost", "http://evil.localhost:8080", "HTTP://Free-Gift.LOCALHOST", "http://a.b.localhost"}
			if os != "windows" {
				pages = append(pages, "http://tauri.localhost", "http://wails.localhost")
			}
			for _, o := range pages {
				for _, p := range routes {
					if w := do(p.method, p.path, gw, o, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "gateway key") || w.Header().Get("Access-Control-Allow-Origin") != "" {
						t.Errorf("%s %s from %s with no key: %d %s", p.method, p.path, o, w.Code, w.Body)
					}
				}
			}
			// rebound: the page's own name turned to 127.0.0.1, its GET
			// with no Origin and its POST with its own
			for _, p := range routes {
				o := ""
				if p.method == "POST" {
					o = "http://evil.localhost:3425"
				}
				if w := do(p.method, p.path, "evil.localhost:3425", o, ""); w.Code != http.StatusForbidden {
					t.Errorf("%s %s rebound at evil.localhost with no key: %d %s", p.method, p.path, w.Code, w.Body)
				}
			}
			if n := hits.Load(); n != 0 {
				t.Fatalf("a localhost subdomain with no key reached the vendor %d times", n)
			}
			// with an enabled key it is answered, both ways, and reads
			// nothing more (no CORS headers)
			if w := do("POST", "/v1/chat/completions", gw, "http://evil.localhost", secret); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("cross-origin with its key: %d %s", w.Code, w.Body)
			}
			if w := do("GET", "/v1/models", "evil.localhost:3425", "", secret); w.Code != http.StatusOK {
				t.Errorf("rebound with its key: %d %s", w.Code, w.Body)
			}
			// answered as before: a page at localhost itself, and the two
			// webviews on Windows
			keyless := []string{"http://localhost:5173", "http://127.0.0.1:8080"}
			if os == "windows" {
				keyless = append(keyless, "http://tauri.localhost", "http://wails.localhost")
			}
			for _, o := range keyless {
				if w := do("POST", "/v1/chat/completions", gw, o, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
					t.Errorf("%s as before: %d %s", o, w.Code, w.Body)
				}
			}
			for _, host := range []string{"localhost:3425", "LOCALHOST.:3425", "docker.for.mac.localhost:3425"} {
				if w := do("GET", "/v1/models", host, "", ""); w.Code != http.StatusOK {
					t.Errorf("a browser at %s: %d %s", host, w.Code, w.Body)
				}
			}
		})
	}
}

// A browser extension the user listed by its origin (#1051, SkyAerope on
// Discord: an extension such as Sider, without host permissions, is held
// to CORS like a page) is treated as a listed web page is, no more: its
// preflight is answered, its keyed call reads the reply, and a call
// without a gateway key is refused 401. An extension not listed is
// answered as before — no CORS headers, so only one that holds host
// permissions (and doesn't enforce CORS) reads anything.
func TestCORSExtensionOrigin(t *testing.T) {
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
	secret, err := access.Update("add-key", access.Change{Name: "Extension"})
	if err != nil {
		t.Fatal(err)
	}
	const ext = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	const other = "chrome-extension://ponmlkjihgfedcbaponmlkjihgfedcba"
	const fox = "moz-extension://e0b5d7c7-c079-484b-8825-44d261383cb6"
	srv := lanGuard(New().Handler())
	do := func(method, origin, key string) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, "/v1/chat/completions", body)
		r.RemoteAddr = "127.0.0.1:50123"
		r.Host = "127.0.0.1:3425"
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0")
		if method == "OPTIONS" {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		} else {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	// not listed: no CORS headers, as before; a web page listed doesn't list it
	s := settings.Load()
	s.CORSOrigins = []string{"http://localhost:3000"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if w := do("OPTIONS", ext, ""); w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted extension's preflight: %d %v", w.Code, w.Header())
	}
	if w := do("POST", ext, ""); w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted extension, as before: %d %v %s", w.Code, w.Header(), w.Body)
	}

	// listed, written as the user might copy it
	if s.CORSOrigins, err = settings.CleanOrigins([]string{"http://localhost:3000", "CHROME-EXTENSION://ABCDEFGHIJKLMNOPABCDEFGHIJKLMNOP/", fox}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{ext, fox} {
		w := do("OPTIONS", o, "")
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != o ||
			w.Header().Get("Access-Control-Allow-Headers") != "authorization, content-type" || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") {
			t.Errorf("%s preflight: %d %v", o, w.Code, w.Header())
		}
		if w := do("POST", o, secret); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") || w.Header().Get("Access-Control-Allow-Origin") != o {
			t.Errorf("%s with its key: %d %v %s", o, w.Code, w.Header(), w.Body)
		}
	}
	n := hits.Load()
	// a listed extension needs a gateway key, as a listed page does
	if w := do("POST", ext, ""); w.Code != http.StatusUnauthorized || w.Header().Get("Access-Control-Allow-Origin") != ext || !strings.Contains(w.Body.String(), "gateway key") {
		t.Errorf("listed extension without a key: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := do("POST", ext, "not-a-key"); w.Code != http.StatusUnauthorized {
		t.Errorf("listed extension with a wrong key: %d %s", w.Code, w.Body)
	}
	if hits.Load() != n {
		t.Fatal("a keyless call from a listed extension reached the vendor")
	}
	// another extension is still not listed
	if w := do("OPTIONS", other, ""); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("another extension's preflight: %d %v", w.Code, w.Header())
	}
	if w := do("POST", other, secret); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("another extension: %d %v", w.Code, w.Header())
	}
}

// A preflight that reaches the gateway from another machine was refused
// 401 for the key a preflight may not carry (#4931: the same preflight
// for a listed origin was answered 204 on loopback and 401, with no CORS
// headers, on the LAN address and behind an HTTPS reverse proxy, whose
// forwarding header makes it remote from loopback too — a browser
// reports it as PreflightMissingAllowOriginHeader and the page's fetch
// fails). lanGuard now hands a preflight to corsGuard, which answers a
// listed origin's and refuses any other page's, while the call it asks
// after still needs the key, and the network's rules (the allow list,
// sharing on the LAN) are judged first, as for every request.
func TestCORSPreflightFromLAN(t *testing.T) {
	fresh(t)
	s := settings.Load()
	s.CORSOrigins = []string{"https://learn.trancy.org"}
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	srv := lanGuard(New().Handler())
	do := func(remote, host, method, origin, key string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/models", nil)
		r.RemoteAddr = remote
		r.Host = host
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
		srv.ServeHTTP(w, r)
		return w
	}
	pf := map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}
	const trancy = "https://learn.trancy.org"
	const lan = "192.168.1.9:50123"
	const lanHost = "192.168.1.5:3425"

	// the issue's first row: on loopback the preflight was answered
	if w := do("127.0.0.1:50123", "127.0.0.1:3425", "OPTIONS", trancy, "", pf); w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != trancy || w.Header().Get("Access-Control-Allow-Headers") != "authorization" {
		t.Errorf("loopback preflight: %d %v", w.Code, w.Header())
	}
	// direct on the LAN: was 401, with no CORS headers
	if w := do(lan, lanHost, "OPTIONS", trancy, "", pf); w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != trancy || w.Header().Get("Access-Control-Allow-Headers") != "authorization" {
		t.Errorf("LAN preflight: %d %v", w.Code, w.Header())
	}
	// behind a reverse proxy that forwards with a header (nginx): was 401
	if w := do("127.0.0.1:50123", "magpie.example.com", "OPTIONS", trancy, "",
		map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization", "X-Forwarded-For": "203.0.113.7"}); w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != trancy {
		t.Errorf("proxied preflight: %d %v", w.Code, w.Header())
	}

	// a page not listed is refused, as on loopback: no 204, no headers
	if w := do(lan, lanHost, "OPTIONS", "https://evil.example", "", pf); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code == http.StatusNoContent {
		t.Errorf("unlisted origin's preflight: %d %v", w.Code, w.Header())
	}
	// the call the preflight asks after still needs the key
	if w := do(lan, lanHost, "GET", trancy, "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("keyless call from the LAN: %d", w.Code)
	}
	// an OPTIONS that declares no call is no preflight
	if w := do(lan, lanHost, "OPTIONS", trancy, "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("plain OPTIONS from the LAN: %d", w.Code)
	}
	// and while magpie isn't shared, the network's rules refuse a
	// preflight as they refuse everything else from another machine
	s.LAN = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if w := do(lan, lanHost, "OPTIONS", trancy, "", pf); w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight while not shared: %d %v", w.Code, w.Header())
	}
}

// A preflight from another machine is let past the key check for a listed
// origin alone, and then corsGuard answers it with nothing behind it
// reached (#4931). corsGuard passes some unlisted origins on unkeyed (a
// page on this computer, the request's own origin, an app's scheme), and
// a route that takes OPTIONS (Codex's relay, the MCP paths) would serve
// such a "preflight" with no key, so those meet the key check as before;
// and no request but a preflight skips it for a preflight's headers.
func TestCORSPreflightFromLANReachesNothing(t *testing.T) {
	fresh(t)
	s := settings.Load()
	s.CORSOrigins = []string{"https://learn.trancy.org"}
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var reached []string
	srv := lanGuard(corsGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path+" "+r.Header.Get("Origin"))
		w.WriteHeader(http.StatusTeapot)
	})))
	const lan = "192.168.1.9:50123"
	const lanHost = "192.168.1.5:3425"
	do := func(method, path, origin string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = lan
		r.Host = lanHost
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	pf := map[string]string{"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization"}
	for _, path := range []string{CodexPath + "/responses", "/mcp/github", "/v1/models"} {
		for _, origin := range []string{
			"http://localhost:5173",     // a page on this computer
			"http://127.0.0.1:8080",     // the same, by address
			"http://foo.localhost",      // a localhost subdomain
			"http://" + lanHost,         // the request's own origin
			"chrome-extension://abcdef", // an extension
			"app://obsidian.md",         // an app's scheme
			"null",                      // a sandboxed frame
			"https://evil.example",      // a site elsewhere
		} {
			reached = nil
			w := do(http.MethodOptions, path, origin, pf)
			if len(reached) > 0 || w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("keyless preflight for %s from %s: %d %v, reached %v", path, origin, w.Code, w.Header(), reached)
			}
		}
		// a listed origin's preflight is answered, by corsGuard alone
		reached = nil
		if w := do(http.MethodOptions, path, "https://learn.trancy.org", pf); w.Code != http.StatusNoContent || len(reached) > 0 {
			t.Errorf("listed preflight for %s: %d, reached %v", path, w.Code, reached)
		}
		// a preflight's headers on any other method skip nothing
		for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			reached = nil
			if w := do(m, path, "https://learn.trancy.org", pf); w.Code != http.StatusUnauthorized || len(reached) > 0 {
				t.Errorf("keyless %s for %s with a preflight's headers: %d, reached %v", m, path, w.Code, reached)
			}
		}
	}
}
