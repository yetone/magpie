package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestQoder(t *testing.T) {
	t.Run("global", func(t *testing.T) { testQoder(t, qoder, filepath.Join(".qoder", "settings.json")) })
	t.Run("cn", func(t *testing.T) { testQoder(t, qoderCN, filepath.Join(".qoder-cn", "settings.json")) })
	// Qoder CN's own config dir, apart from Qoder's
	home := t.TempDir()
	t.Setenv("QODER_CONFIG_DIR", filepath.Join(home, "g"))
	t.Setenv("QODERCN_CONFIG_DIR", filepath.Join(home, "c"))
	if g, c := qoder(home), qoderCN(home); g.Path != filepath.Join(home, "g", "settings.json") ||
		c.Path != filepath.Join(home, "c", "settings.json") || c.ID != "qoder-cn" || c.Bin != "qoderclicn" {
		t.Fatalf("paths %q %q", g.Path, c.Path)
	}
}

func testQoder(t *testing.T, mk func(string) *Agent, rel string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("QODER_CONFIG_DIR", "")
	t.Setenv("QODERCN_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, rel)
	type file struct {
		Providers map[string]map[string]any `json:"providers"`
		Model     map[string]any            `json:"model"`
		Theme     string                    `json:"theme"`
	}
	read := func() (file, string) {
		var f file
		b, _ := os.ReadFile(path)
		json.Unmarshal(jsonc.ToJSON(b), &f)
		return f, string(b)
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{
  // the user's
  "theme": "dark",
  "providers": {"mine": {"baseUrl": "https://x/v1", "apiKey": "sk-m", "model": "m1"}},
  "model": {"name": "performance", "reasoningEffort": "high"}
}`), 0o644)

	a := mk(home)
	f, e := a.Field("model"), a.Field("effort")
	if f.Get() != "performance" || e.Get() != "high" {
		t.Fatalf("get: %q %q", f.Get(), e.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read()
	p := c.Providers[magpieID]
	if !strings.Contains(raw, "// the user's") || c.Model["name"] != "magpie/deepseek/pro" || p["baseUrl"] != gatewayV1() || p["apiKey"] != gateway.TokenFor(a.ID) ||
		p["protocol"] != "openai" || p["model"] != "deepseek/pro" || c.Providers["mine"]["apiKey"] != "sk-m" || c.Theme != "dark" {
		t.Fatalf("magpie:\n%s", raw)
	}
	var ids []string
	for _, m := range p["models"].([]any) {
		ids = append(ids, m.(map[string]any)["model"].(string))
	}
	if len(ids) < 2 || a.Check() != "" {
		t.Fatalf("models %v, check %q", ids, a.Check())
	}
	// another magpie model keeps the model stashed first
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := e.Set("low"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c.Providers[magpieID]["model"] != "deepseek/flash" || c.Model["reasoningEffort"] != "low" {
		t.Fatalf("flash:\n%s", raw)
	}
	// the effort is the model's own, in model.preferences: Qoder asks
	// reasoningEffort only of a model with none there
	pref := func(model string) any {
		c, _ := read()
		ps, _ := c.Model["preferences"].(map[string]any)
		p, _ := ps[model].(map[string]any)
		r, _ := p["reasoning"].(map[string]any)
		return r["effort"]
	}
	if pref("magpie/deepseek/flash") != "low" {
		t.Fatalf("flash preference:\n%s", raw)
	}
	// Qoder moved reasoningEffort into a preference of the model's own, and
	// it was turned off: that is the effort shown, and the one set
	edit.DelJSON(path, "model.reasoningEffort")
	edit.SetJSON(path, edit.KV{Path: "model.preferences", Value: map[string]any{
		"magpie/deepseek/flash": map[string]any{"reasoning": map[string]any{"effort": "high", "enabled": false}, "contextWindow": 1000},
		"performance":           map[string]any{"reasoning": map[string]any{"effort": "max"}}}})
	if e.Get() != "high" {
		t.Fatalf("preference get: %q", e.Get())
	}
	if err := e.Set("medium"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if p := c.Model["preferences"].(map[string]any)["magpie/deepseek/flash"].(map[string]any); pref("magpie/deepseek/flash") != "medium" ||
		p["reasoning"].(map[string]any)["enabled"] != nil || p["contextWindow"] != 1000.0 || pref("performance") != "max" || c.Model["reasoningEffort"] != "medium" {
		t.Fatalf("preference set:\n%s", raw)
	}
	// a model with no effort of its own (a dot in its id) takes the one in use
	if err := f.Set("magpie/deepseek/v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	if pref("magpie/deepseek/v4.1-flash") != "medium" || e.Get() != "medium" {
		_, raw = read()
		t.Fatalf("carried effort:\n%s", raw)
	}
	f.Set("magpie/deepseek/flash")

	// reset: magpie's provider goes and the user's model comes back
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if _, ok := c.Providers[magpieID]; ok || c.Model["name"] != "performance" || c.Providers["mine"] == nil {
		t.Fatalf("reset:\n%s", raw)
	}

	// a model of Qoder's own, from magpie, takes magpie's provider out too
	f.Set("magpie/deepseek/pro")
	if err := f.Set("ultimate"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if _, ok := c.Providers[magpieID]; ok || c.Model["name"] != "ultimate" {
		t.Fatalf("own:\n%s", raw)
	}

	// wired before Qoder had a key of its own: still magpie's, and a sync
	// gives it Qoder's key, which the gateway knows it by (not Bun's UA)
	f.Set("magpie/deepseek/pro")
	edit.SetJSON(path, edit.KV{Path: "providers." + magpieID + ".apiKey", Value: gateway.Token})
	if a.Check() != "" {
		t.Fatalf("old key: %q", a.Check())
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if c, raw = read(); c.Providers[magpieID]["apiKey"] != gateway.TokenFor(a.ID) || usage.AgentOf(a.ID) != a.ID {
		t.Fatalf("synced key:\n%s", raw)
	}

	// no settings before: a new file with magpie's provider
	os.Remove(path)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if c, raw = read(); c.Model["name"] != "magpie/deepseek/pro" {
		t.Fatalf("new file:\n%s", raw)
	}
}
