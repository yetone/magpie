package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCommandCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".commandcode")
	settingsPath, providersPath := filepath.Join(dir, "settings.json"), filepath.Join(dir, "providers.json")
	read := func(p string) map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(p)
		json.Unmarshal(b, &m)
		return m
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(settingsPath, []byte(`{"theme":"dark","model":"deepseek/deepseek-v4-flash"}`), 0o644)
	os.WriteFile(providersPath, []byte(`{"provider":{"mine":{"name":"Mine","api":"openai-completions","baseURL":"https://x","apiKey":"$X","models":{"a":{}}}}}`), 0o644)

	f := commandCode(home).Field("model")
	if f.Get() != "deepseek/deepseek-v4-flash" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s, p := read(settingsPath), read(providersPath)
	if s["model"] != "magpie/deepseek/pro" || s["modelProvider"] != "magpie" || s["theme"] != "dark" {
		t.Fatalf("settings: %v", s)
	}
	ps := p["provider"].(map[string]any)
	m, ok := ps["magpie"].(map[string]any)
	if !ok || ps["mine"] == nil || m["apiKey"] != false || m["api"] != "openai-completions" {
		t.Fatalf("providers: %v", p)
	}
	if ms := m["models"].(map[string]any); ms["deepseek/pro"] == nil || ms["deepseek/flash"] == nil {
		t.Fatalf("models: %v", ms)
	}

	// its own model: magpie steps out
	if err := f.Set("moonshotai/kimi-k3"); err != nil {
		t.Fatal(err)
	}
	s, p = read(settingsPath), read(providersPath)
	if s["model"] != "moonshotai/kimi-k3" || s["modelProvider"] != nil || p["provider"].(map[string]any)["magpie"] != nil || p["provider"].(map[string]any)["mine"] == nil {
		t.Fatalf("own: %v %v", s, p)
	}

	// through magpie, then reset
	f.Set("magpie/deepseek/flash")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	s, p = read(settingsPath), read(providersPath)
	if s["model"] != nil || s["modelProvider"] != nil || s["theme"] != "dark" || p["provider"].(map[string]any)["magpie"] != nil {
		t.Fatalf("reset: %v %v", s, p)
	}
}
