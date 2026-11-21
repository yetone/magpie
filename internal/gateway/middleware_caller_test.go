package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// A JS gateway middleware is told the agent a request came from as
// ctx.caller, the value a plugin's chat.headers gets as input.caller, and
// can turn an agent away by it before any provider is asked (@febsincos on
// X): an agent here and one other magpies passed on alike, the chain of
// computers included. ctx.agent stays what it was.
func TestMiddlewareToldTheCaller(t *testing.T) {
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var sent struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(b, &sent)
		mu.Lock()
		if len(sent.Messages) > 0 {
			got = append(got, sent.Messages[0].Content)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer up.Close()
	fresh(t)
	t.Cleanup(plugin.Settle)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	was := usage.PluginAgents
	usage.PluginAgents = func() []usage.Known {
		return []usage.Known{{ID: "pi", Name: "Pi Agent", Names: []string{"pi"}}, {ID: "alma", Name: "Alma", Names: []string{"alma"}}}
	}
	t.Cleanup(func() { usage.PluginAgents = was })

	mw := filepath.Join(t.TempDir(), "who.middleware.js")
	os.WriteFile(mw, []byte(`
export function onRequest(body, ctx) {
  if (ctx.caller && ctx.caller.agent === "pi") return ctx.reject(403, "no " + ctx.caller.name + " via [" + ctx.caller.via.join(">") + "]");
  body.messages[0].content = JSON.stringify({agent: ctx.agent || "", caller: ctx.caller === undefined ? "none" : ctx.caller});
  return body;
}`), 0o644)
	b, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: mw}}})
	os.MkdirAll(settings.Dir(), 0o755)
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	middleware.Reload()
	defer middleware.Reload()

	ask := func(h map[string]string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		New().Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, tc := range []struct {
		name   string
		h      map[string]string
		code   int
		upSaw  string // what the middleware wrote for the vendor; "" when refused
		reason string
	}{
		{"an agent here, by its token", map[string]string{"Authorization": "Bearer " + TokenFor("alma")}, 200, `{"agent":"alma","caller":{"agent":"alma","name":"Alma","via":[]}}`, ""},
		{"an agent passed on through two magpies", map[string]string{"User-Agent": "magpie/0.1.0", AgentHeader: "alma", ViaHeader: "MacBook-Pro", ViaChainHeader: "MacBook-Pro,office-mini", HopsHeader: "2"}, 200, `{"agent":"magpie","caller":{"agent":"alma","name":"Alma","via":["MacBook-Pro","office-mini"]}}`, ""},
		{"an agent turned away here", map[string]string{"User-Agent": "pi/0.9"}, 403, "", "no Pi Agent via []"},
		{"an agent turned away through two magpies", map[string]string{"User-Agent": "magpie/0.1.0", AgentHeader: "pi", ViaHeader: "MacBook-Pro", ViaChainHeader: "MacBook-Pro,office-mini", HopsHeader: "2"}, 403, "", "no Pi Agent via [MacBook-Pro>office-mini]"},
		{"a client's own caller headers, not a magpie", map[string]string{"User-Agent": "pi/0.9", AgentHeader: "alma", ViaHeader: "elsewhere", provider.CallerAgentHeader: "alma"}, 403, "", "no Pi Agent via []"},
	} {
		mu.Lock()
		got = nil
		mu.Unlock()
		code, body := ask(tc.h)
		mu.Lock()
		saw := append([]string(nil), got...)
		mu.Unlock()
		if code != tc.code {
			t.Errorf("%s: %d %s; want %d", tc.name, code, body, tc.code)
			continue
		}
		if tc.code != 200 {
			if !strings.Contains(body, tc.reason) {
				t.Errorf("%s: refused with %s; want %q", tc.name, body, tc.reason)
			}
			if len(saw) != 0 {
				t.Errorf("%s: a refused request reached the vendor: %q", tc.name, saw)
			}
			continue
		}
		if len(saw) != 1 || saw[0] != tc.upSaw {
			t.Errorf("%s: the middleware saw %q; want %s", tc.name, saw, tc.upSaw)
		}
	}
}
