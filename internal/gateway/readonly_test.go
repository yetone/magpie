package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// readOnlyRefused is every route a read-only key is refused, by the pattern
// it is registered under. A route the router has that is neither here nor
// in readOnlyRoutes fails TestReadOnlyKeyEveryRoute, so a new one is
// classified before it ships: the guard refuses it until then.
var readOnlyRefused = []string{
	// model lists and their Ollama spellings: what the key could use
	"GET /v1/models", "GET /models", "GET /v1/models/{id...}", "GET /muse-code/models",
	"GET /api/hello", "GET /api/version", "GET /api/tags",
	"POST /api/show", "POST /api/chat", "POST /api/generate",
	// writes, another key's routes, the whole trace, debugging
	"POST /v1/magpie/usage/requests", "POST " + provider.RemoteRefreshPath,
	// a Codex reset, which can't be undone (#3850)
	"POST " + provider.RemoteCodexResetPath,
	// an update another magpie asks for
	"POST " + provider.RemoteUpdatePath,
	"GET /v1/magpie/route", "GET /v1/magpie/trace",
	"GET " + ClaudeRunsPath, "GET " + HeapPath, "GET " + CPUPath, "GET " + GoroutinesPath,
	"POST " + RestartPath,
	"POST " + KeysPath, "GET " + KeysPath, "DELETE " + KeysPath + "/{id}", "POST " + OIDCKeysPath,
	"GET /v1/magpie/omarchy", "OPTIONS /v1/magpie/omarchy",
	// models, a WebSocket's upgrade and count_tokens among them
	"POST /v1/chat/completions", "POST /chat/completions",
	"POST /v1/responses", "POST /responses", "GET /v1/responses", "GET /responses",
	"POST /v1/messages", "POST /messages", "POST /v1/systemone",
	"POST /v1" + AlphaSearchPath, "POST " + AlphaSearchPath, "POST /v1/messages/count_tokens",
	"POST /v1/images/generations", "POST /images/generations", "POST /v1/images/edits", "POST /images/edits",
	"POST /v1/embeddings", "POST /embeddings", "POST /v1/rerank", "POST /rerank",
	"POST /v1/decisions", "POST /decisions",
	"POST /v1/audio/transcriptions", "POST /audio/transcriptions",
	"POST /v1/audio/translations", "POST /audio/translations",
	"POST /v1/audio/speech", "POST /audio/speech",
	"POST /v1/videos", "POST /videos", "GET /v1/videos/{id}", "GET /videos/{id}",
	"GET /v1/videos/{id}/content", "GET /videos/{id}/content",
	"POST " + NativePath + "{provider}/{path...}", "GET " + NativePath + "{provider}/{path...}", "DELETE " + NativePath + "{provider}/{path...}",
	"PUT " + NativePath + "{provider}/{path...}", "PATCH " + NativePath + "{provider}/{path...}",
	// MCP servers, plugins' and Codex's backends, Gemini, everything else
	"POST " + claudeMCPPath + "{token}", "/mcp/{name}", "/mcp/run/{name}",
	SearchMCPPath, VisionMCPPath, ImageMCPPath, ImageMCPFiles + "{name}",
	BrowserMCPPath, "GET " + BrowserExtensionPath, "GET " + BrowserStatusPath, ToolsMCPPath, ToolsMCPOne + "{plugin}",
	HubMCPPath + "{agent}", HubMCPAll, CodexPath + "/", "GET " + CodexCatalogPath,
	"GET /v1beta/models", "POST /v1beta/models/{call...}", "/",
}

var wildcard = regexp.MustCompile(`\{[a-z]+(\.\.\.)?\}`)

// requestsFor is a request a pattern is routed, one per method it takes.
func requestsFor(pattern string) [][2]string {
	methods, path := []string{"GET", "POST", "PUT", "DELETE"}, pattern
	if m, p, ok := strings.Cut(pattern, " "); ok {
		methods, path = []string{m}, p
	}
	switch {
	case path == "/":
		path = "/no/such/route" // the catch-all
	case strings.HasSuffix(path, "{$}"):
		path = strings.TrimSuffix(path, "{$}") // that path alone
	case strings.HasSuffix(path, "/"):
		path += "x" // a subtree: a path under it
	}
	path = wildcard.ReplaceAllString(path, "x")
	var out [][2]string
	for _, m := range methods {
		out = append(out, [2]string{m, path})
	}
	return out
}

// Steven on Discord: an ESP32 desk display polls magpie's quotas and usage
// on his network every 15s and sends no model request, yet the key it
// keeps in flash had to be one that could spend every account. A
// read-only key is answered the stats alone: every other route the
// router has (every model, count_tokens, a WebSocket's upgrade, every
// MCP server, a plugin's backend and every write) is refused it, from
// this computer and from another, and no upstream is reached. What it
// reads is what a key held to nothing reads, the usage summary and every
// account's quotas, and none of it is a key's secret.
func TestReadOnlyKeyEveryRoute(t *testing.T) {
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
	keys, secrets := newCaller(t, "Desk display", "Laptop")
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	display, full := secrets[0], secrets[1]

	mux := New().routes()
	for _, p := range mux.patterns {
		if readOnlyRoutes[p] == slices.Contains(readOnlyRefused, p) {
			t.Errorf("route %q is in neither readOnlyRoutes nor readOnlyRefused, or in both: say whether a read-only key may reach it", p)
		}
	}
	for p := range readOnlyRoutes {
		if !slices.Contains(mux.patterns, p) {
			t.Errorf("readOnlyRoutes names %q, which the router doesn't have", p)
		}
	}

	h := lanGuard(New().Handler())
	do := func(method, path, from, secret string, hdr ...string) (int, string) {
		var body io.Reader
		if method == "POST" || method == "PUT" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = from
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	// the full key makes a request, so there is usage to read
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", full); c != 200 {
		t.Fatal("a full key's request:", c, b)
	}
	reached.Store(0)

	for _, from := range []string{"192.168.1.9:5000", "127.0.0.1:5000"} {
		for _, p := range readOnlyRefused {
			for _, mp := range requestsFor(p) {
				c, b := do(mp[0], mp[1], from, display)
				if c != http.StatusForbidden || !strings.Contains(b, ReadOnlyCode) {
					t.Errorf("read-only key from %s: %s %s (%s): %d %.200s", from, mp[0], mp[1], p, c, b)
				}
			}
		}
		// a WebSocket's upgrade, by its header and by a path that isn't clean
		for _, path := range []string{"/v1/responses", "/responses"} {
			c, b := do("GET", path, from, display, "Upgrade", "websocket", "Connection", "Upgrade", "Sec-WebSocket-Version", "13", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			if c != http.StatusForbidden || !strings.Contains(b, ReadOnlyCode) {
				t.Errorf("read-only key from %s: a WebSocket on %s: %d %.200s", from, path, c, b)
			}
		}
		if c, b := do("POST", "/v1//chat/completions", from, display); c == 200 || strings.Contains(b, `"choices"`) {
			t.Errorf("read-only key from %s: an unclean chat path: %d %.200s", from, c, b)
		}
		if n := reached.Load(); n != 0 {
			t.Fatalf("read-only key from %s: the upstream was reached %d times", from, n)
		}

		for p := range readOnlyRoutes {
			for _, mp := range requestsFor(p) {
				c, b := do(mp[0], mp[1], from, display)
				if c == http.StatusForbidden || c == http.StatusUnauthorized {
					t.Errorf("read-only key from %s: %s %s refused: %d %.200s", from, mp[0], mp[1], c, b)
				}
				for _, s := range append(slices.Clone(secrets), "upstream-secret") {
					if strings.Contains(b, s) {
						t.Errorf("read-only key from %s: %s %s tells a secret: %.200s", from, mp[0], mp[1], b)
					}
				}
			}
		}
		// the usage summary, which a held key isn't told, and the quotas
		c, b := do("GET", "/v1/magpie/usage", from, display)
		if c != 200 || !strings.Contains(b, "Laptop") {
			t.Errorf("read-only key from %s: the usage summary: %d %.300s", from, c, b)
		}
		if c, b := do("GET", "/v1/magpie/quotas", from, display); c != 200 || strings.Contains(b, provider.KeyHeldCode) {
			t.Errorf("read-only key from %s: the quotas: %d %.300s", from, c, b)
		}
	}

	// the full key is as it was; made full again, so is the display's
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", full); c != 200 {
		t.Error("the full key's request after:", c, b)
	}
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", display); c != 200 {
		t.Error("the display's key given full access:", c, b)
	}
}
