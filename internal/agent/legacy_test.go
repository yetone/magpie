package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestRenameLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	write := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	write(".config/opencode/opencode.jsonc", `{
  // keep me
  "provider": {
    "dial": {"npm": "@ai-sdk/openai-compatible", "name": "dial", "options": {"apiKey": "dial", "baseURL": "http://127.0.0.1:3425/v1"}, "models": {}},
    "mine": {"name": "mine"}
  },
  "model": "dial/deepseek/deepseek-flash",
  "theme": "dark"
}
`)
	write(".codex/config.toml", "model_reasoning_effort = \"high\"\nmodel = \"deepseek/deepseek-flash\"\nmodel_provider = \"dial\"\nmodel_catalog_json = '"+filepath.Join(home, ".codex", "dial-models.json")+"'\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n\n[model_providers.dial]\nname = \"dial\"\nbase_url = \"http://127.0.0.1:3425/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"dial\"\n")
	write(".codex/dial-models.json", "{}")
	write(".pi/agent/settings.json", `{"defaultProvider": "dial", "defaultModel": "deepseek/deepseek-flash", "theme": "dark"}`)
	write(".pi/agent/models.json", `{"providers": {"dial": {"api": "openai-completions", "apiKey": "dial", "baseUrl": "http://127.0.0.1:3425/v1", "models": []}}}`)
	write(".claude/settings.json", `{"env": {"ANTHROPIC_BASE_URL": "`+gateway.URL()+`", "ANTHROPIC_AUTH_TOKEN": "dial", "ANTHROPIC_MODEL": "deepseek/deepseek-flash"}, "theme": "dark"}`)

	RenameLegacy()

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	oc := read(".config/opencode/opencode.jsonc")
	if v, _ := edit.GetJSON(filepath.Join(home, ".config/opencode/opencode.jsonc"), "model"); v != "magpie/deepseek/deepseek-flash" {
		t.Fatalf("opencode model: %q", v)
	}
	if !strings.Contains(oc, `"magpie": {`) || strings.Contains(oc, `"dial"`) || !strings.Contains(oc, "// keep me") || !strings.Contains(oc, `"mine"`) {
		t.Fatalf("opencode:\n%s", oc)
	}
	cx := read(".codex/config.toml")
	if strings.Contains(cx, "dial") || !strings.Contains(cx, "[model_providers.magpie]") || !strings.Contains(cx, `model_provider = "magpie"`) || !strings.Contains(cx, `[projects."/x"]`) {
		t.Fatalf("codex:\n%s", cx)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "dial-models.json")); err == nil {
		t.Fatal("dial-models.json still there")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "magpie-models.json")); err != nil {
		t.Fatal("magpie-models.json missing")
	}
	if b := stashLoad(); b["codex.provider"] == "dial" {
		t.Fatal("dial stashed as the provider to go back to")
	}
	if v, _ := edit.GetJSON(filepath.Join(home, ".pi/agent/settings.json"), "defaultProvider"); v != "magpie" {
		t.Fatalf("pi provider: %q", v)
	}
	pm := read(".pi/agent/models.json")
	if strings.Contains(pm, `"dial"`) || !strings.Contains(pm, `"magpie"`) {
		t.Fatalf("pi models:\n%s", pm)
	}
	cl := read(".claude/settings.json")
	if strings.Contains(cl, `"dial"`) || !strings.Contains(cl, `"ANTHROPIC_AUTH_TOKEN": "magpie"`) || !strings.Contains(cl, `"theme": "dark"`) {
		t.Fatalf("claude:\n%s", cl)
	}

	// a second run leaves every file alone
	before := map[string]string{}
	for _, rel := range []string{".config/opencode/opencode.jsonc", ".codex/config.toml", ".pi/agent/settings.json", ".pi/agent/models.json", ".claude/settings.json"} {
		before[rel] = read(rel)
	}
	RenameLegacy()
	for rel, b := range before {
		if read(rel) != b {
			t.Fatalf("%s changed on the second run", rel)
		}
	}
}

// An agent set to one of Cursor's ids at an effort, from before magpie
// offered the family as one model, is set to that model, at that effort
// where its own is unset; an id magpie offers no model for is left.
func TestMoveCursorEfforts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "curs", Name: "Curs", Chat: "https://curs.example/v1", Key: "k", Models: []string{"grok-4.7"}}); err != nil {
		t.Fatal(err)
	}
	base := func(id string) (string, string, bool) {
		if id == "grok-4.7-low" || id == "gone-low" {
			return strings.TrimSuffix(id, "-low"), "low", true
		}
		return "", "", false
	}
	pi := filepath.Join(home, ".pi", "agent", "settings.json")
	write := func(body string) {
		if err := os.MkdirAll(filepath.Dir(pi), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pi, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"defaultProvider": "magpie", "defaultModel": "curs/grok-4.7-low", "theme": "dark"}`)
	moveEfforts("curs", base)
	for k, want := range map[string]string{"defaultProvider": "magpie", "defaultModel": "curs/grok-4.7", "defaultThinkingLevel": "low", "theme": "dark"} {
		if v, _ := edit.GetJSON(pi, k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	// an effort of its own stays; a model the picker doesn't offer stays
	write(`{"defaultProvider": "magpie", "defaultModel": "curs/grok-4.7-low", "defaultThinkingLevel": "high"}`)
	moveEfforts("curs", base)
	if v, _ := edit.GetJSON(pi, "defaultThinkingLevel"); v != "high" {
		t.Errorf("thinking level %q", v)
	}
	write(`{"defaultProvider": "magpie", "defaultModel": "curs/gone-low"}`)
	moveEfforts("curs", base)
	if v, _ := edit.GetJSON(pi, "defaultModel"); v != "curs/gone-low" {
		t.Errorf("model %q", v)
	}
}
