package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestDsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".dsh", "config.yaml")
	a := dsh(home)
	f := a.Field("model")
	read := func() string { b, _ := os.ReadFile(path); return string(b) }

	// nothing there, nothing written
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("empty: %v %q", err, f.Get())
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("reset wrote a file")
	}

	own := "# mine\n- id: tools\n  config:\n    disabled: []\n\n- id: llm-deepseek\n  config:\n    thinking: disabled\n    reasoningEffort: \"off\"\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(own), 0o644)

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	for _, want := range []string{"# mine", "- id: tools", "- id: llm-deepseek # magpie", `baseURL: "http://`, `apiKey: "magpie"`, `- id: "deepseek/flash"`, "- id: agent-loop # magpie", `model: "deepseek/pro"`, "cwd: !!js process.cwd()", "- id: api-gateway # magpie"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "thinking: disabled") || strings.Count(s, "id: llm-deepseek") != 1 {
		t.Fatalf("the user's entry should be replaced:\n%s", s)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}

	// dsh's own model: the user's endpoint entry comes back
	if err := f.Set("deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	s = read()
	if f.Get() != "deepseek-v4-flash" || !strings.Contains(s, "thinking: disabled") || strings.Contains(s, "llm-deepseek # magpie") || strings.Contains(s, "api-gateway") {
		t.Fatalf("own model: %q\n%s", f.Get(), s)
	}

	// through magpie again, then back to dsh as it ships
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != own {
		t.Fatalf("reset should leave the user's file as it was:\n%s", got)
	}

	if err := f.Set("magpie/nope/x"); err == nil {
		t.Fatal("unknown catalog model accepted")
	}

	// a file magpie cannot read as a list is left alone
	os.WriteFile(path, []byte("llm-deepseek:\n  x: 1\n"), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("a mapping was edited as a list")
	}
}

// Since 0.1.5 each profile has its own patch list, the key is a credential
// and new sessions start on agent-default-model.
func TestDshProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".dsh")
	template := "# Your patch layer for this dsh profile.\n[]\n"
	web, desktop := filepath.Join(dir, "profiles", "web", "cordis.patch.yml"), filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	for _, p := range []string{web, desktop} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(template), 0o644)
	}
	legacy := filepath.Join(dir, "config.yaml")
	os.WriteFile(legacy, []byte("- id: agent-loop # magpie\n  config:\n    agents: []\n"), 0o644)
	settings := filepath.Join(dir, "settings.yaml")
	os.WriteFile(settings, []byte("ui:\n  theme: dark\nagent-default-model:\n  provider: deepseek-official\n  model: deepseek-flash\n"), 0o644)
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	f := dsh(home).Field("model")
	if f.Get() != "deepseek-flash" {
		t.Fatalf("the model picked in dsh: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{web, desktop} {
		s := read(p)
		for _, want := range []string{"# Your patch layer", "- id: llm-pi-ai # magpie", "    providers:\n      magpie:\n", "apiKeyEnv: " + dshKeyRef, "baseURL: http://", "- id: agent-default-model # magpie", "provider: magpie", `model: "deepseek/pro"`} {
			if !strings.Contains(s, want) {
				t.Fatalf("missing %q in %s:\n%s", want, p, s)
			}
		}
		if strings.Contains(s, "[]") || strings.Contains(s, "apiKey:") || strings.Contains(s, "agent-loop") || strings.Contains(s, "llm-deepseek") {
			t.Fatalf("%s:\n%s", p, s)
		}
	}
	if got := read(legacy); got != "[]\n" {
		t.Fatalf("config.yaml should lose magpie's entries: %q", got)
	}
	if !strings.Contains(read(filepath.Join(dir, ".env")), dshKeyRef+"=magpie") {
		t.Fatal("no key for the gateway")
	}
	if s := read(settings); strings.Contains(s, "agent-default-model") || !strings.Contains(s, "theme: dark") {
		t.Fatalf("settings:\n%s", s)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}

	if err := f.Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if s := read(web); strings.Contains(s, "llm-pi-ai") || !strings.Contains(s, "provider: deepseek-official") || !strings.Contains(s, `model: "deepseek-v4-pro"`) || f.Get() != "deepseek-v4-pro" {
		t.Fatalf("own model: %q\n%s", f.Get(), s)
	}
	if strings.Contains(read(filepath.Join(dir, ".env")), dshKeyRef) {
		t.Fatal("the key outlived the gateway")
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(web); got != template {
		t.Fatalf("reset should leave the template as it was:\n%q", got)
	}
}

func TestDshSettingsEndpoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.yaml")
	os.WriteFile(p, []byte("ui:\n  theme: dark\nllm-deepseek:\n  thinking: enabled\n"), 0o644)
	if dshSettingsEndpoint(p) {
		t.Fatal("thinking alone is not an endpoint")
	}
	os.WriteFile(p, []byte("llm-deepseek:\n  baseURL: https://x\nui:\n  apiKey: y\n"), 0o644)
	if !dshSettingsEndpoint(p) {
		t.Fatal("baseURL missed")
	}
}

// Without them dsh takes every model for a text-only one with a million
// tokens of context and 256K out.
func TestDshModelLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k", Models: []string{"see", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{
		{ID: "see", Name: "see", Images: true, ImageInput: imageInputBool(true), Context: 400000, Output: 128000},
		{ID: "plain", Name: "plain", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := yaml.Marshal(dshRouteConfig())
	s := string(b)
	want := `    - id: v/see
      name: see · V
      contextWindow: 400000
      maxTokens: 128000
      input: [text, image]
    - id: v/plain
      name: plain · V
      input: [text]`
	if !strings.Contains(s, want) || strings.Count(s, "contextWindow") != 1 {
		t.Fatalf("models:\n%s", s)
	}
}

// The desktop app's profile made after the web's was set up (Discord 莫:
// the web version lists magpie's models, the desktop one DeepSeek's alone)
// is reported and, on the next sync, given the model the web's has.
func TestDshProfileMadeLater(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".dsh")
	template := "# Your patch layer for this dsh profile.\n[]\n"
	web, desktop, mine := filepath.Join(dir, "profiles", "web", "cordis.patch.yml"), filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml"), filepath.Join(dir, "profiles", "mine", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte(template), 0o644)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("wired: %s", d)
	}
	// dsh's desktop app opened for the first time, and a profile with the
	// user's own llm-deepseek
	own := "- id: llm-deepseek\n  config:\n    baseURL: https://example.com\n"
	for p, body := range map[string]string{desktop: template, mine: own} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	if d := a.Check(); !strings.Contains(d, "desktop profile") {
		t.Fatalf("the new profile isn't reported: %q", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(desktop)
	for _, want := range []string{"- id: llm-pi-ai # magpie", "apiKeyEnv: " + dshKeyRef, "- id: agent-default-model # magpie", `model: "deepseek/pro"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q in the desktop profile:\n%s", want, b)
		}
	}
	// dsh's own DeepSeek row, pointed elsewhere by the user, is no longer
	// magpie's to take: the profile gets magpie's route beside it
	b, _ = os.ReadFile(mine)
	if !strings.HasPrefix(string(b), own) || !strings.Contains(string(b), "- id: llm-pi-ai # magpie") {
		t.Fatalf("the user's own entry was changed:\n%s", b)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after the sync: %s", d)
	}
}
