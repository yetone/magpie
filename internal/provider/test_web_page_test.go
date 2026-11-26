package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Neo on Discord: a relay's Gemini models tested green, the relay logged
// no request, and Antigravity couldn't use them through magpie, while
// its Codex models were asked for real. The relay's site answers any path
// its API doesn't serve 200 with its own page (new-api's and one-api's
// NoRoute serve the web app), so a Gemini URL the API isn't at was
// "answered" by the page; the gateway reads that as no answer (#1012).
// A test reads it so too: each reply that isn't the API's is red, and says
// what came, and the API's own reply stays green.
func TestModelTestTakesNoPageForAnAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	site := `<!doctype html><html lang="zh"><head><meta charset="utf-8"><title>New API</title></head><body><div id="root"></div></body></html>`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
		case strings.HasPrefix(r.URL.Path, "/api-gemini/"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`)
		case strings.HasPrefix(r.URL.Path, "/empty/"):
			w.Header().Set("Content-Type", "application/json")
		case strings.HasPrefix(r.URL.Path, "/unlabelled/"):
			// a page with no Content-Type of its own (Go's sniff says
			// text/html, so it is cleared)
			w.Header()["Content-Type"] = nil
			io.WriteString(w, site)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, site)
		}
	}))
	defer up.Close()

	for _, c := range []struct {
		name  string
		proto Protocol
		base  string
		ok    bool
		says  string
	}{
		{"Gemini's API", Gemini, up.URL + "/api-gemini/v1beta", true, ""},
		{"chat's API", Chat, up.URL + "/v1", true, ""},
		{"the site at Gemini's URL", Gemini, up.URL + "/v1beta", false, "web page (text/html), not an API reply: “New API”"},
		{"the site at chat's URL", Chat, up.URL + "/site/v1", false, "web page (text/html), not an API reply: “New API”"},
		{"a page with no Content-Type", Gemini, up.URL + "/unlabelled/v1beta", false, "web page (no Content-Type), not an API reply: “New API”"},
		{"an empty reply", Gemini, up.URL + "/empty/v1beta", false, "with nothing in it, not an API reply"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Provider{ID: "relay", Name: "Relay", Key: "sk-test", Models: []string{"gemini-3-pro"}}
			if c.proto == Gemini {
				p.Gemini = c.base
			} else {
				p.Chat = c.base
			}
			if err := Save(p); err != nil {
				t.Fatal(err)
			}
			got, err := Find("relay")
			if err != nil {
				t.Fatal(err)
			}
			g := got.TestModels(context.Background(), []string{"gemini-3-pro"})[0]
			if g.Protocol != c.proto {
				t.Fatalf("asked on %s", g.Protocol)
			}
			if g.OK != c.ok || !c.ok && (g.Status != http.StatusBadGateway || !strings.Contains(g.Error, c.says)) {
				mu.Lock()
				defer mu.Unlock()
				t.Errorf("ok %v, %d %q; want ok %v saying %q (asked %v)", g.OK, g.Status, g.Error, c.ok, c.says, asked)
			}
		})
	}
}
