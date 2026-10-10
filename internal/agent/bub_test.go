package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
	"gopkg.in/yaml.v3"
)

func TestBub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".bub")
	path := filepath.Join(dir, "config.yml")
	read := func() (map[string]any, string) {
		var c map[string]any
		b, _ := os.ReadFile(path)
		yaml.Unmarshal(b, &c)
		return c, string(b)
	}
	providers := func(c map[string]any) map[string]any { p, _ := c["providers"].(map[string]any); return p }
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte("# mine\nmodel: dashscope:kimi-k3 # main\nfallback_models:\n  - dashscope:qwen3.8-flash\napi_key: sk-dashscope\nproviders:\n  work:\n    type: openai\n    api_base: https://proxy.example/v1\ntelegram:\n  token: t\n"), 0o644)

	a := bub(home)
	if !a.Detected() {
		t.Fatal("not detected")
	}
	f := a.Field("model")
	if f.Get() != "dashscope:kimi-k3" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read()
	mp, _ := providers(c)["magpie"].(map[string]any)
	if c["model"] != "magpie:deepseek/pro" || mp == nil || mp["type"] != "openai" || !strings.HasSuffix(mp["api_base"].(string), "/v1") ||
		mp["api_key"] != gateway.TokenFor("bub") || providers(c)["work"] == nil || c["api_key"] != "sk-dashscope" ||
		c["fallback_models"] == nil || c["telegram"] == nil || !strings.Contains(raw, "# mine") {
		t.Fatalf("config:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}
	if msg := a.Check(); msg != "" {
		t.Fatalf("check: %s", msg)
	}
	// another magpie model keeps what was stashed first
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}

	// something else rewrote magpie's entry
	_, raw = read()
	os.WriteFile(path, []byte(strings.Replace(raw, gateway.TokenFor("bub"), "other", 1)), 0o644)
	if msg := a.Check(); !strings.Contains(msg, "api_key") {
		t.Fatalf("check after rewrite: %q", msg)
	}

	// its own model: Bub stays joined — magpie's entry stays for ,model
	// to name again, the user's stay
	if err := f.Set("openrouter:openrouter/free"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != "openrouter:openrouter/free" || providers(c)["magpie"] == nil || providers(c)["work"] == nil {
		t.Fatalf("own:\n%s", raw)
	}
	if !a.Wired() {
		t.Fatal("joined: not wired")
	}
	if a.Check() != "" {
		t.Fatalf("check joined: %q", a.Check())
	}

	// back on one of magpie's models
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if c, raw = read(); c["model"] != "magpie:deepseek/flash" || providers(c)["magpie"] == nil {
		t.Fatalf("back:\n%s", raw)
	}

	// reset from magpie: back to what the user picked, still joined
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != "openrouter:openrouter/free" || providers(c)["magpie"] == nil || providers(c)["work"] == nil {
		t.Fatalf("reset:\n%s", raw)
	}

	// sync rewrites magpie's entry where it is, key and all
	_, raw = read()
	os.WriteFile(path, []byte(strings.Replace(raw, gateway.TokenFor("bub"), "other", 1)), 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	mp, _ = providers(c)["magpie"].(map[string]any)
	if mp["api_key"] != gateway.TokenFor("bub") {
		t.Fatalf("sync:\n%s", raw)
	}

	// disconnect: magpie's entry goes, the model the user picked stays
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != "openrouter:openrouter/free" || providers(c)["magpie"] != nil || providers(c)["work"] == nil {
		t.Fatalf("disconnect:\n%s", raw)
	}

	// a config with no model of its own: none comes back
	os.WriteFile(path, []byte("max_steps: 60\n"), 0o644)
	f.Set("magpie/deepseek/pro")
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != nil || c["providers"] != nil || c["max_steps"] == nil {
		t.Fatalf("reset, no model:\n%s", raw)
	}

	// providers of the user's own, empty, stay
	os.WriteFile(path, []byte("providers: {}\n"), 0o644)
	f.Set("magpie/deepseek/pro")
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if c, raw = read(); c["providers"] == nil {
		t.Fatalf("reset, own empty providers:\n%s", raw)
	}

	// effort is completion_args.reasoning_effort, beside the user's own args
	os.WriteFile(path, []byte("completion_args:\n  temperature: 0.2\n"), 0o644)
	e := a.Field("effort")
	if err := e.Set("high"); err != nil {
		t.Fatal(err)
	}
	if e.Get() != "high" {
		t.Fatalf("effort: %q", e.Get())
	}
	if err := e.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	args, _ := c["completion_args"].(map[string]any)
	if args["reasoning_effort"] != nil || args["temperature"] == nil {
		t.Fatalf("effort reset:\n%s", raw)
	}

	// with no completion_args before, none is left after
	os.WriteFile(path, []byte("max_steps: 60\n"), 0o644)
	e.Set("low")
	e.Set("medium")
	if err := e.Set(""); err != nil {
		t.Fatal(err)
	}
	if c, raw = read(); c["completion_args"] != nil {
		t.Fatalf("effort reset, none before:\n%s", raw)
	}
}

// A Bub from before providers takes "magpie:" for an any-llm provider it
// doesn't know, so magpie isn't set there; a Bub that can't be asked is the
// user's call.
func TestBubOld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchers are scripts with #! here")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".bub", "config.yml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// bub as pip writes it, its Python answering as a Bub of each kind would
	bubOn := func(answer string) {
		bin := filepath.Join(t.TempDir(), "bin")
		os.MkdirAll(bin, 0o755)
		py := filepath.Join(bin, "python3")
		testenv.Program(t, py, "#!/bin/sh\necho "+answer+"\n")
		os.WriteFile(filepath.Join(bin, "bub"), []byte("#!"+py+"\n# -*- coding: utf-8 -*-\nimport sys\n"), 0o755)
		t.Setenv("PATH", bin)
		os.WriteFile(path, []byte("model: dashscope:kimi-k3\n"), 0o644)
	}

	bubOn("False")
	a := bub(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("set on an old Bub")
	}
	if b, _ := os.ReadFile(path); string(b) != "model: dashscope:kimi-k3\n" {
		t.Fatalf("old Bub's config written:\n%s", b)
	}
	// set on it by hand: Check says why it won't work
	os.WriteFile(path, []byte("model: magpie:deepseek/pro\n"), 0o644)
	if msg := a.Check(); !strings.Contains(msg, "update Bub") {
		t.Fatalf("check: %q", msg)
	}

	bubOn("True")
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}

	bubOn("nonsense")
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatalf("a Bub that can't be told: %v", err)
	}
}
