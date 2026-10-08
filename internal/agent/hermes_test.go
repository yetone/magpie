package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

func TestHermes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".hermes")
	path := filepath.Join(dir, "config.yaml")
	read := func() (map[string]any, map[string]any, string) {
		var c map[string]any
		b, _ := os.ReadFile(path)
		yaml.Unmarshal(b, &c)
		m, _ := c["model"].(map[string]any)
		return c, m, string(b)
	}
	providers := func(c map[string]any) map[string]any { p, _ := c["providers"].(map[string]any); return p }
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte("# mine\nmodel:\n  default: anthropic/claude-opus-5 # main\n  provider: openrouter\n  base_url: https://openrouter.ai/api/v1\nproviders:\n  mine:\n    base_url: https://x\n"), 0o644)

	f := hermes(home).Field("model")
	if f.Get() != "anthropic/claude-opus-5" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, m, raw := read()
	mp, _ := providers(c)["magpie"].(map[string]any)
	if m["default"] != "deepseek/pro" || m["provider"] != "magpie" || m["base_url"] != "https://openrouter.ai/api/v1" ||
		providers(c)["mine"] == nil || mp == nil || mp["api_mode"] != "chat_completions" || !strings.HasSuffix(mp["base_url"].(string), "/v1") ||
		!strings.Contains(raw, "# mine") {
		t.Fatalf("config:\n%s", raw)
	}
	ids := map[string]bool{}
	for _, x := range mp["models"].([]any) {
		ids[x.(string)] = true
	}
	if !ids["deepseek/pro"] || !ids["deepseek/flash"] {
		t.Fatalf("models: %v", ids)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}
	// another magpie model keeps what was stashed first
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}

	// its own model: the user's provider comes back, magpie's entry stays
	// beside it, so Hermes's session picker still offers magpie's models
	// and it is still connected (lijiho96940561 on X)
	if err := f.Set("moonshotai/kimi-k3"); err != nil {
		t.Fatal(err)
	}
	c, m, raw = read()
	if m["default"] != "moonshotai/kimi-k3" || m["provider"] != "openrouter" || providers(c)["magpie"] == nil || providers(c)["mine"] == nil {
		t.Fatalf("own:\n%s", raw)
	}
	a := hermes(home)
	if !a.Wired() || a.Drift() != nil {
		t.Fatalf("own model: wired %v, drift %+v", a.Wired(), a.Drift())
	}

	// the default changed in Hermes itself (`hermes model`): still connected,
	// not told as drift
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetYAML(path, edit.KV{Path: "model.provider", Value: "anthropic"}, edit.KV{Path: "model.default", Value: "claude-opus-5"}); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() || a.Drift() != nil {
		t.Fatalf("changed in Hermes: wired %v, drift %+v", a.Wired(), a.Drift())
	}

	// Disconnect takes magpie's entry out
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, m, raw = read()
	if m["default"] != "claude-opus-5" || m["provider"] != "anthropic" || providers(c)["magpie"] != nil || providers(c)["mine"] == nil || a.Wired() {
		t.Fatalf("disconnect, own model:\n%s", raw)
	}

	// Disconnect from magpie's model: back to what the user had
	f.Set("magpie/deepseek/flash")
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, m, raw = read()
	if m["default"] != "claude-opus-5" || m["provider"] != "anthropic" || providers(c)["magpie"] != nil {
		t.Fatalf("disconnect:\n%s", raw)
	}

	// a config with no provider of its own: none comes back
	os.WriteFile(path, []byte("agent:\n  max_turns: 60\n"), 0o644)
	f.Set("magpie/deepseek/pro")
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, m, raw = read()
	if m["provider"] != nil || m["default"] != nil || providers(c)["magpie"] != nil || c["agent"] == nil {
		t.Fatalf("disconnect, no provider:\n%s", raw)
	}
}
