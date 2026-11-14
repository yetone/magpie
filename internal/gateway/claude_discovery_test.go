package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's Gateway Model Discovery keeps only the ids that say
// claude or anthropic (/(claude|anthropic)/i in its 2.1.316 bundle), so a
// remote magpie's Kimi and DeepSeek were in no /model (zent00 on X). Its
// request is shown every model by an id it keeps, each served as the model
// it stands for; an id it keeps already, a provider named anthropic's
// included, stays as it is; and every other client, Claude Code's turns'
// claude-cli/ without the discovery's header among them, is shown the
// ids as they were.
func TestClaudeCodeDiscoveryKeepsEveryModel(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"ds":{"models":{"deepseek-v4":{"id":"deepseek-v4","name":"DeepSeek V4"}}},"km":{"models":{"kimi-k3":{"id":"kimi-k3","name":"Kimi K3"},"claude-opus-4-8":{"id":"claude-opus-4-8","name":"Claude Opus 4.8"}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []provider.Provider{
		{ID: "ds", Name: "DeepSeek", Catalog: "ds", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4"}},
		{ID: "km", Name: "Moonshot", Catalog: "km", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kimi-k3", "claude-opus-4-8"}},
		// a provider named anthropic, which serves any model asked of it
		// as anthropic/<model>: km's kimi-k3 is never listed so
		{ID: "anthropic", Name: "Mine", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"km/kimi-k3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	list := func(ua string, version bool) map[string]string {
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("User-Agent", ua)
		if version {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []struct {
				ID      string `json:"id"`
				Display string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		got := map[string]string{}
		for _, m := range out.Data {
			got[m.ID] = m.Display
		}
		return got
	}
	got := list("claude-code/2.1.316", true)
	want := map[string]string{
		aliasFor("ds/deepseek-v4"): "ds/deepseek-v4",
		aliasFor("km/kimi-k3"):     "km/kimi-k3",
		aliasFor("auto/kimi-k3"):   "auto/kimi-k3", // the routing group of both kimi-k3s
		// kept as they are
		"km/claude-opus-4-8":   "km/claude-opus-4-8",
		"anthropic/km/kimi-k3": "anthropic/km/kimi-k3",
	}
	for id, real := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("Claude Code's discovery was not shown %s for %s: %v", id, real, got)
		}
		if !claudeCodeKept.MatchString(id) {
			t.Fatalf("Claude Code leaves out %s", id)
		}
		if u := unprefixed(id); u != real {
			t.Fatalf("%s is served as %s, want %s", id, u, real)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Claude Code's discovery was shown %v", got)
	}
	if d := got[aliasFor("ds/deepseek-v4")]; d != "DeepSeek V4 · DeepSeek" {
		t.Fatalf("its /model names the row by display_name, got %q", d)
	}
	for _, c := range []struct {
		ua      string
		version bool
	}{
		{"claude-cli/2.1.316 (external, cli)", false},
		{"OpenAI/Go 1.0", false},
		{"curl/8.0", true},
	} {
		other := list(c.ua, c.version)
		if _, ok := other["ds/deepseek-v4"]; !ok || len(other) != 5 {
			t.Fatalf("%s was shown %v", c.ua, other)
		}
		for id := range other {
			if magpieAliasAsked(id) {
				t.Fatalf("%s was shown Claude Code's ids: %v", c.ua, other)
			}
		}
	}
}
