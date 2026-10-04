package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

func TestMorph(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MISTER_MORPH_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".morph")
	path := filepath.Join(dir, "config.yaml")
	read := func() (map[string]any, string) {
		var c map[string]any
		b, _ := os.ReadFile(path)
		yaml.Unmarshal(b, &c)
		llm, _ := c["llm"].(map[string]any)
		return llm, string(b)
	}
	os.MkdirAll(dir, 0o755)
	mine := "# mine\nllm:\n  inference_provider: openai\n  provider: openai_resp\n  model: \"gpt-5.4\" # main\n" +
		"  endpoint: \"https://api.openai.com\"\n  api_key: \"${OPENAI_API_KEY}\"\n  context_window_tokens: 100000\n" +
		"  headers:\n    X-Team: core\n  reasoning_effort: high\ntools:\n  bash:\n    enabled: true\n"
	os.WriteFile(path, []byte(mine), 0o644)

	a := morph(home)
	if !a.Detected() {
		t.Fatal("not found by ~/.morph")
	}
	f := a.Field("model")
	if f.Get() != "gpt-5.4" || a.Check() != "" {
		t.Fatalf("get: %q, check %q", f.Get(), a.Check())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	llm, raw := read()
	h, _ := llm["headers"].(map[string]any)
	if llm["inference_provider"] != "openai_response_compatible" || llm["provider"] != "openai_resp" || llm["model"] != "deepseek/pro" ||
		llm["endpoint"] != gateway.URL()+"/v1" || llm["api_key"] != gateway.Token || llm["context_window_tokens"] != nil ||
		h["User-Agent"] != morphUA || h["X-Team"] != "core" || llm["reasoning_effort"] != "high" ||
		!strings.Contains(raw, "# mine") || !strings.Contains(raw, "enabled: true") {
		t.Fatalf("config:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get: %q, check %q", f.Get(), a.Check())
	}
	// another magpie model keeps what was stashed first
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if llm, _ := read(); llm["model"] != "deepseek/flash" {
		t.Fatalf("flash: %v", llm)
	}

	// its own model: what the user had comes back, magpie's header goes
	if err := f.Set("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	llm, raw = read()
	h, _ = llm["headers"].(map[string]any)
	if llm["model"] != "gpt-5.5" || llm["inference_provider"] != "openai" || llm["provider"] != "openai_resp" ||
		llm["endpoint"] != "https://api.openai.com" || llm["api_key"] != "${OPENAI_API_KEY}" ||
		llm["context_window_tokens"] != 100000 || len(h) != 1 || h["X-Team"] != "core" {
		t.Fatalf("own:\n%s", raw)
	}

	// reset from magpie: back to what the user had
	f.Set("magpie/deepseek/flash")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if llm, raw = read(); llm["model"] != "gpt-5.5" || llm["api_key"] != "${OPENAI_API_KEY}" {
		t.Fatalf("reset:\n%s", raw)
	}

	// a config with nothing under llm: nothing of magpie's stays, headers
	// magpie's User-Agent was alone in included
	os.WriteFile(path, []byte("tools:\n  bash:\n    enabled: true\n"), 0o644)
	f.Set("magpie/deepseek/pro")
	if llm, raw = read(); llm["provider"] != nil {
		t.Fatalf("provider written:\n%s", raw)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if llm, raw = read(); len(llm) != 0 || !strings.Contains(raw, "enabled: true") {
		t.Fatalf("reset, nothing of its own:\n%s", raw)
	}

	// the gateway's address changed under it: said so
	f.Set("magpie/deepseek/pro")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gateway.URL(), "http://127.0.0.1:1", 1)), 0o644)
	if c := a.Check(); !strings.Contains(c, "endpoint") {
		t.Fatalf("check: %q", c)
	}

	// MISTER_MORPH_CONFIG names the file
	other := filepath.Join(home, "elsewhere.yaml")
	t.Setenv("MISTER_MORPH_CONFIG", other)
	if err := morph(home).Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(other); err != nil || !strings.Contains(string(b), "deepseek/pro") {
		t.Fatalf("MISTER_MORPH_CONFIG: %s %v", b, err)
	}

	if a, err := Find("morph"); err != nil || a.Name != "Mister Morph" {
		t.Fatal("not among All()")
	}
}

// The window magpie knows a model to take is handed to morph, which knows
// one only from a list of its own.
func TestMorphContextWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MISTER_MORPH_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k",
		Models: []string{"pro", "flash"}, Contexts: map[string]int{"pro": 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".morph", "config.yaml")
	window := func() (int, string) {
		var c struct {
			LLM struct {
				Window int `yaml:"context_window_tokens"`
			} `yaml:"llm"`
		}
		b, _ := os.ReadFile(path)
		yaml.Unmarshal(b, &c)
		return c.LLM.Window, string(b)
	}
	f := morph(home).Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if n, raw := window(); n != 1000000 {
		t.Fatalf("pro: %d\n%s", n, raw)
	}
	// one whose window magpie doesn't know: morph's own guess, not pro's
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if n, raw := window(); n != 0 {
		t.Fatalf("flash: %d\n%s", n, raw)
	}
}
