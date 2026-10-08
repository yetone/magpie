package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestFx(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".fx", "settings.json")
	read := func() map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &m)
		return m
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"dark","model":"anthropic/claude-sonnet-5","providers":{"local":{"protocol":"openai-chat-completions","base_url":"http://localhost:11434/v1","auth":{"type":"none"}}}}`), 0o644)

	a := fx(home)
	f := a.Field("model")
	if f.Get() != "anthropic/claude-sonnet-5" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	s := read()
	ps := s["providers"].(map[string]any)
	m, ok := ps["magpie"].(map[string]any)
	if !ok || ps["local"] == nil || s["provider"] != "magpie" || s["models"].(map[string]any)["magpie"] != "deepseek/flash" || s["theme"] != "dark" {
		t.Fatalf("settings: %v", s)
	}
	if m["protocol"] != "openai-chat-completions" || m["base_url"] != gatewayV1() || m["auth"].(map[string]any)["type"] != "none" {
		t.Fatalf("provider: %v", m)
	}
	if ms := m["model_metadata"].(map[string]any); ms["deepseek/pro"] == nil || ms["deepseek/flash"] == nil {
		t.Fatalf("models: %v", ms)
	}
	if f.Get() != "magpie/deepseek/flash" || a.Check() != "" {
		t.Fatalf("get: %q, check: %q", f.Get(), a.Check())
	}

	// its own model: magpie steps out, and the gateway's model is set
	if err := f.Set("openai/gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s["provider"] != nil || s["providers"].(map[string]any)["magpie"] != nil || s["providers"].(map[string]any)["local"] == nil ||
		s["models"].(map[string]any)["gateway"] != "openai/gpt-6-sol" || f.Get() != "openai/gpt-6-sol" {
		t.Fatalf("own: %v", s)
	}

	// one of the user's own providers keeps its model
	os.WriteFile(path, []byte(`{"provider":"local","models":{"local":"qwen"}}`), 0o644)
	if f.Get() != "qwen" {
		t.Fatalf("local: %q", f.Get())
	}

	// through magpie, then reset
	f.Set("magpie/deepseek/pro")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s["provider"] != nil || s["providers"].(map[string]any)["magpie"] != nil || s["models"].(map[string]any)["magpie"] != nil {
		t.Fatalf("reset: %v", s)
	}
}

// fx is also the JSON viewer's name: a command of that name on PATH is no
// sign of the agent, its folder is.
func TestFxDetected(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	testenv.Program(t, filepath.Join(bin, "fx"), "#!/bin/sh\n")
	if fx(home).Detected() {
		t.Error("an fx command alone is taken for the agent")
	}
	if err := os.WriteFile(filepath.Join(home, ".fx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if fx(home).Detected() {
		t.Error("a file named .fx is taken for the agent's folder")
	}
	os.Remove(filepath.Join(home, ".fx"))
	if err := os.MkdirAll(filepath.Join(home, ".fx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !fx(home).Detected() {
		t.Error("~/.fx is not taken for the agent")
	}
}
