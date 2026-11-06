package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// The key magpie writes into the Magpie Browser extension's zip lands in
// Downloads, so a copy of it can turn up anywhere. It is a browser key: it
// opens the extension's socket and every other route the router has (every
// model, count_tokens, a WebSocket's upgrade, every MCP server, the stats,
// a plugin's backend and every write) is refused it, from this computer and
// from another, however the key is sent, and no upstream is reached. The
// key a magpie from before made with full access is held so too
// (ScopeOwnBrowserKey), and stays so.
func TestBrowserKeyEveryRoute(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	var reached atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Magpie Browser", "Laptop")
	ext, full := secrets[0], secrets[1]
	if err := SetOwnBrowserKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}

	h := lanGuard(New().Handler())
	do := func(method, path, from, secret string, hdr ...string) (int, string) {
		var body io.Reader
		if method == "POST" || method == "PUT" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = from
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	// a magpie from before gave it full access: it reached a model
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", ext); c != 200 {
		t.Fatal("the extension's key before it is held:", c, b)
	}
	ScopeOwnBrowserKey()
	ScopeOwnBrowserKey() // twice is once
	reached.Store(0)

	refused := func(from, what string, c int, b string) {
		t.Helper()
		if c != http.StatusForbidden || !strings.Contains(b, BrowserKeyCode) {
			t.Errorf("browser key from %s: %s: %d %.200s", from, what, c, b)
		}
	}
	mux := New().routes()
	if !slices.Contains(mux.patterns, browserKeyRoute) {
		t.Fatalf("the router has no %q", browserKeyRoute)
	}
	for _, from := range []string{"192.168.1.9:5000", "127.0.0.1:5000"} {
		for _, p := range mux.patterns {
			if p == browserKeyRoute {
				continue
			}
			for _, mp := range requestsFor(p) {
				c, b := do(mp[0], mp[1], from, ext)
				refused(from, mp[0]+" "+mp[1]+" ("+p+")", c, b)
			}
		}
		// the key as each client sends it
		c, b := do("POST", "/v1/messages", from, "", "x-api-key", ext)
		refused(from, "x-api-key on /v1/messages", c, b)
		c, b = do("POST", "/v1beta/models/m1:generateContent?key="+ext, from, "")
		refused(from, "?key= on generateContent", c, b)
		c, b = do("POST", "/v1/chat/completions?key="+ext, from, "")
		refused(from, "?key= on /v1/chat/completions", c, b)
		for _, path := range []string{"/v1/responses", "/responses"} {
			c, b := do("GET", path, from, ext, "Upgrade", "websocket", "Connection", "Upgrade", "Sec-WebSocket-Version", "13", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			refused(from, "a WebSocket on "+path, c, b)
		}
		if c, b := do("POST", "/v1//chat/completions", from, ext); c == 200 || strings.Contains(b, `"choices"`) {
			t.Errorf("browser key from %s: an unclean chat path: %d %.200s", from, c, b)
		}
		if n := reached.Load(); n != 0 {
			t.Fatalf("browser key from %s: the upstream was reached %d times", from, n)
		}
		// the extension's own route is let through to its handler, which
		// takes a WebSocket alone
		if c, b := do("GET", BrowserExtensionPath, from, ext, "Origin", "chrome-extension://x"); strings.Contains(b, BrowserKeyCode) {
			t.Errorf("browser key from %s: the extension's route: %d %.200s", from, c, b)
		}
	}

	// it stays held: neither given full access nor made an issuer, and a
	// key it would issue doesn't exist to be issued
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID}); err == nil {
		t.Error("the browser key was given full access")
	}
	if _, err := access.Update("issuer-key", access.Change{Key: keys[0].ID, Issues: true}); err == nil {
		t.Error("the browser key was made an issuer")
	}
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", ext); c == 200 {
		t.Error("the browser key reached a model after:", c, b)
	}
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", full); c != 200 {
		t.Error("the other key's request:", c, b)
	}
}

// A browser connected with a gateway key is let go once the key isn't on:
// turned off, rotated or removed. Its socket is closed (watchKey), and a
// call in the moment before that is handed to no browser (call): least of
// all to this computer's agents' when the key was the own browser's.
func TestBrowserKeyOffClosesItsBrowser(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	was := browserKeyCheck
	t.Cleanup(func() { browserKeyCheck = was })
	s := New()
	const testPeer, testHost = "X-Test-Peer", "X-Test-Host"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get(testPeer); p != "" {
			r.RemoteAddr, r.Host = p, r.Header.Get(testHost)
			r.Header.Del(testPeer)
			r.Header.Del(testHost)
		}
		lanGuard(s.Handler()).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	keys, secrets := newCaller(t, "Off", "Rotated", "Removed", "Own")
	if err := SetOwnBrowserKey(keys[3].ID); err != nil {
		t.Fatal(err)
	}
	ScopeOwnBrowserKey()
	away := http.Header{testPeer: {"172.17.0.1:40000"}, testHost: {"192.0.2.1:3425"}}
	reply := func(_, _, _ string, _ map[string]any) any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "tabs"}}}
	}
	count := func(id string) int { return len(s.browser.all(keyedSession(id, "x"))) }
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	h := lanGuard(s.Handler())
	tabs := func(addr string, hdr ...string) (string, bool) {
		t.Helper()
		m := readImageReply(t, browserPost(h, addr, imageToolCall("browser_tabs", map[string]any{}), hdr...))
		var all []string
		for _, c := range m.Result.Content {
			all = append(all, c.Text)
		}
		return strings.Join(all, "\n"), m.Result.IsError
	}

	// the call looks for itself: the watch is too slow to see it
	browserKeyCheck = time.Hour
	fakeExtensionWith(t, srv.URL, secrets[3], http.Header{testPeer: {"127.0.0.1:40002"}, testHost: {"host.docker.internal:3425"}}, "Chrome/141", reply)
	waitFor("the own browser never connected", func() bool { return count(keys[3].ID) == 1 })
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); bad || got != "tabs" {
		t.Fatalf("this computer's caller: %q", got)
	}
	if _, err := access.Update("off-key", access.Change{Key: keys[3].ID}); err != nil {
		t.Fatal(err)
	}
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); !bad || got == "tabs" {
		t.Fatalf("this computer's caller used the browser of a key turned off: %q", got)
	}
	waitFor("the own browser stayed connected after its key was off", func() bool { return count(keys[3].ID) == 0 })

	// rotated: the browser holds the old secret, and a caller with the
	// new one isn't handed it
	fakeExtensionWith(t, srv.URL, secrets[1], away, "Chrome/141", reply)
	waitFor("the rotated key's browser never connected", func() bool { return count(keys[1].ID) == 1 })
	fresh2, err := access.Update("rotate-key", access.Change{Key: keys[1].ID})
	if err != nil || fresh2 == "" {
		t.Fatal(fresh2, err)
	}
	if got, bad := tabs("172.17.0.1:40000", "Authorization", "Bearer "+fresh2, BrowserSessionHeader, "s1"); !bad || got == "tabs" {
		t.Fatalf("the new secret's caller used the browser the old one connected: %q", got)
	}

	// the watch: each is let go with no call made
	browserKeyCheck = 20 * time.Millisecond
	fakeExtensionWith(t, srv.URL, secrets[0], away, "Chrome/141", reply)
	fakeExtensionWith(t, srv.URL, secrets[2], away, "Chrome/141", reply)
	fakeExtensionWith(t, srv.URL, fresh2, away, "Chrome/141", reply)
	waitFor("the keyed browsers never connected", func() bool {
		return count(keys[0].ID) == 1 && count(keys[2].ID) == 1 && count(keys[1].ID) >= 1
	})
	time.Sleep(100 * time.Millisecond) // several looks: a key that is on stays connected
	if count(keys[0].ID) != 1 || count(keys[2].ID) != 1 || count(keys[1].ID) != 1 {
		t.Fatalf("a browser whose key is on was let go: off %d, removed %d, rotated %d", count(keys[0].ID), count(keys[2].ID), count(keys[1].ID))
	}
	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitFor("the browser stayed connected after its key was turned off", func() bool { return count(keys[0].ID) == 0 })
	if _, err := access.Update("remove-key", access.Change{Key: keys[2].ID}); err != nil {
		t.Fatal(err)
	}
	waitFor("the browser stayed connected after its key was removed", func() bool { return count(keys[2].ID) == 0 })
	if _, err := access.Update("rotate-key", access.Change{Key: keys[1].ID}); err != nil {
		t.Fatal(err)
	}
	waitFor("the browser stayed connected after its key was rotated", func() bool { return count(keys[1].ID) == 0 })
}
