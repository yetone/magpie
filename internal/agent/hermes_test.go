package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
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
	setUpInMagpie("hermes")
	dir := appdir.HermesHomeIn(home)
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
	setUpInMagpie("hermes")
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
	setUpInMagpie("hermes")
	f.Set("magpie/deepseek/pro")
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, m, raw = read()
	if m["provider"] != nil || m["default"] != nil || providers(c)["magpie"] != nil || c["agent"] == nil {
		t.Fatalf("disconnect, no provider:\n%s", raw)
	}
}

// Each of Hermes's bots is a profile under ~/.hermes/profiles/<name> with a
// config.yaml of its own, which doesn't read the main one's: connected,
// magpie is offered in every bot's picker, each keeps its own model, and
// Disconnect takes magpie out of each (lijiho96940561 on X: only the main
// bot had magpie).
func TestHermesProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	write("config.yaml", "model:\n  default: anthropic/claude-opus-5\n  provider: openrouter\n")
	// as `hermes profile create` leaves them: config.yaml, .env, SOUL.md
	bot := write("profiles/coder/config.yaml", "# coder bot\nmodel:\n  default: kimi-k3\n  provider: moonshot\nproviders:\n  mine:\n    base_url: https://x\n")
	write("profiles/coder/SOUL.md", "coder\n")
	// cloned from a main config already on magpie
	clone := write("profiles/writer/config.yaml", "model:\n  default: deepseek/pro\n  provider: magpie\n")
	// deleted (a tombstone beside it), and a name Hermes doesn't allow
	gone := write("profiles/old/config.yaml", "model:\n  default: x\n")
	write("profiles/.deleted/old", "")
	bad := write("profiles/Bad/config.yaml", "model:\n  default: x\n")
	read := func(p string) (map[string]any, string) {
		var c map[string]any
		b, _ := os.ReadFile(p)
		yaml.Unmarshal(b, &c)
		return c, string(b)
	}
	has := func(p string) bool {
		c, _ := read(p)
		ps, _ := c["providers"].(map[string]any)
		return ps["magpie"] != nil
	}

	a := hermes(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read(bot)
	m, _ := c["model"].(map[string]any)
	if !has(bot) || !has(clone) || m["default"] != "kimi-k3" || m["provider"] != "moonshot" || !strings.Contains(raw, "# coder bot") ||
		c["providers"].(map[string]any)["mine"] == nil {
		t.Fatalf("coder after connect:\n%s", raw)
	}
	if has(gone) || has(bad) {
		t.Fatal("a deleted or invalid profile got magpie")
	}

	// a profile made later gets it at the next sync
	later := write("profiles/later/config.yaml", "model:\n  default: x\n")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !has(later) {
		t.Fatal("sync left a new profile out")
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c, raw = read(bot)
	m, _ = c["model"].(map[string]any)
	if has(bot) || has(later) || m["default"] != "kimi-k3" || m["provider"] != "moonshot" || c["providers"].(map[string]any)["mine"] == nil {
		t.Fatalf("coder after disconnect:\n%s", raw)
	}
	// the clone isn't left naming a provider that is gone: it goes onto
	// the model the user had in the main config, as Hermes would have
	// seeded it from there
	c, raw = read(clone)
	m, _ = c["model"].(map[string]any)
	if has(clone) || m["provider"] != "openrouter" || m["default"] != "anthropic/claude-opus-5" {
		t.Fatalf("writer after disconnect:\n%s", raw)
	}
	// disconnected: a sync doesn't put it back
	if err := a.Sync(); err != nil || has(bot) {
		t.Fatalf("sync after disconnect: %v", err)
	}
}

// HERMES_HOME set to a profile outside ~/.hermes (Docker): magpie connects
// that profile, and the root's config and its other profiles get the
// provider too
func TestHermesProfilesOfCustomRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "data")
	for _, p := range []string{"config.yaml", "profiles/a/config.yaml", "profiles/b/config.yaml"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("model:\n  default: x\n"), 0o644)
	}
	t.Setenv("HERMES_HOME", filepath.Join(root, "profiles", "a"))
	if got := hermesProfiles(here(home)); len(got) != 3 {
		t.Fatalf("profiles: %v", got)
	}
	if err := hermes(home).Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"config.yaml", "profiles/b/config.yaml"} {
		v, ok := edit.GetYAML(filepath.Join(root, p), "providers.magpie.base_url")
		if !ok || !strings.HasSuffix(v, "/v1") {
			t.Fatalf("%s: %q", p, v)
		}
	}
	if v, _ := edit.GetYAML(filepath.Join(root, "config.yaml"), "model.default"); v != "x" {
		t.Fatalf("root model: %q", v)
	}
}

// TestHermesFolder (新v学员 on Discord): on this machine Hermes's folder is
// its own default, %LOCALAPPDATA%\hermes on native Windows, not ~/.hermes
// there, and its profiles are under it; in a WSL distro, which is Linux and
// whose variables magpie can't read, it is ~/.hermes.
func TestHermesFolder(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("HERMES_HOME", "")
	want := filepath.Join(home, ".hermes")
	if runtime.GOOS == "windows" {
		want = filepath.Join(local, "hermes")
	}
	if a := hermes(home); a.Dir != want || a.Path != filepath.Join(want, "config.yaml") {
		t.Fatalf("Hermes at %s (%s), want %s", a.Dir, a.Path, want)
	}
	os.MkdirAll(filepath.Join(want, "profiles", "bot"), 0o755)
	os.WriteFile(filepath.Join(want, "profiles", "bot", "config.yaml"), []byte("model: {}\n"), 0o644)
	if got := hermesProfiles(here(home)); len(got) != 2 || got[0] != filepath.Join(want, "config.yaml") {
		t.Fatalf("profiles %v, want under %s", got, want)
	}
	t.Setenv("HERMES_HOME", filepath.Join(home, "custom"))
	wsl := place{home: home, spell: func(s string) string { return s }}
	if a := hermesIn(wsl); a.Dir != filepath.Join(home, ".hermes") {
		t.Fatalf("Hermes in WSL at %s, want ~/.hermes", a.Dir)
	}
}

// What the user, or Hermes itself, set on magpie's provider stays through
// every sync and pick, in the connected config and in each profile's
// (#2030's siblings): the default_model `hermes model` writes there, a name,
// a timeout, a key command, a header beside magpie's. The address, key, API
// mode and model list stay magpie's.
func TestHermesKeepsTheUsersSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	path := filepath.Join(root, "config.yaml")
	bot := filepath.Join(root, "profiles", "coder", "config.yaml")
	os.MkdirAll(filepath.Dir(bot), 0o755)
	os.WriteFile(path, []byte("model:\n  default: anthropic/claude-opus-5\n  provider: openrouter\n"), 0o644)
	os.WriteFile(bot, []byte("model:\n  default: kimi-k3\n  provider: moonshot\n"), 0o644)
	a := hermes(home)
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, bot} {
		if err := edit.SetYAML(p,
			edit.KV{Path: "providers.magpie.name", Value: "My GW"},
			edit.KV{Path: "providers.magpie.default_model", Value: "deepseek/pro"},
			edit.KV{Path: "providers.magpie.request_timeout_seconds", Value: 900},
			edit.KV{Path: "providers.magpie.key_cmd", Value: "cat ~/.gw-key"},
			edit.KV{Path: "providers.magpie.extra_headers.X-Mine", Value: "1"},
			edit.KV{Path: "providers.magpie.base_url", Value: "http://127.0.0.1:1/v1"},
			edit.KV{Path: "providers.magpie.models", Value: []string{"gone/old"}},
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.ai/v1", Key: "k", Models: []string{"k3"}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	check := func(when string) {
		t.Helper()
		for _, p := range []string{path, bot} {
			var c struct {
				Providers map[string]struct {
					Name    string            `yaml:"name"`
					Default string            `yaml:"default_model"`
					Timeout int               `yaml:"request_timeout_seconds"`
					KeyCmd  string            `yaml:"key_cmd"`
					Headers map[string]string `yaml:"extra_headers"`
					BaseURL string            `yaml:"base_url"`
					APIKey  string            `yaml:"api_key"`
					APIMode string            `yaml:"api_mode"`
					Models  []string          `yaml:"models"`
				} `yaml:"providers"`
			}
			raw := readFile(p)
			if err := yaml.Unmarshal([]byte(raw), &c); err != nil {
				t.Fatalf("%v\n%s", err, raw)
			}
			mp := c.Providers[magpieID]
			if mp.Name != "My GW" || mp.Default != "deepseek/pro" || mp.Timeout != 900 || mp.KeyCmd != "cat ~/.gw-key" || mp.Headers["X-Mine"] != "1" {
				t.Errorf("%s: the user's settings not kept in %s:\n%s", when, p, raw)
			}
			if mp.BaseURL == "http://127.0.0.1:1/v1" || mp.APIKey == "" || mp.APIMode != "chat_completions" || mp.Headers["User-Agent"] != "hermes-agent" ||
				slices.Contains(mp.Models, "gone/old") || !slices.Contains(mp.Models, "kimi/k3") {
				t.Errorf("%s: magpie's own parts not written in %s:\n%s", when, p, raw)
			}
		}
	}
	check("sync")
	if err := f.Set("magpie/kimi/k3"); err != nil {
		t.Fatal(err)
	}
	check("pick")
}

// PaulT on Discord: a key made under Gateway keys, put in Hermes's
// config.yaml in place of magpie's, goes through magpie all the same, so
// the check is quiet and a sync keeps it. A key the gateway doesn't take
// still is drift.
func TestHermesWithAGatewayKeyOfTheUsers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	dir := appdir.HermesHomeIn(home)
	path := filepath.Join(dir, "config.yaml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte("model:\n  default: x\n"), 0o644)
	a := hermes(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("as magpie wrote it: %s", d)
	}
	secret, err := access.Update("add-key", access.Change{Name: "hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.SetYAML(path, edit.KV{Path: "providers.magpie.api_key", Value: secret}); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("a key the gateway issued is drift: %s", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(path, "providers.magpie.api_key"); v != secret {
		t.Fatalf("a sync wrote over the user's key: %q", v)
	}
	if err := edit.SetYAML(path, edit.KV{Path: "providers.magpie.api_key", Value: "sk-not-this-gateways"}); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); !strings.Contains(d, "api_key") {
		t.Fatalf("a key the gateway doesn't take is not reported: %q", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(path, "providers.magpie.api_key"); v != gateway.Token {
		t.Fatalf("a key the gateway doesn't take is kept: %q", v)
	}
}

// PaulT on Discord: Hermes's provider written as Hermes keeps keys, the
// api_key "${MAGPIE_API_KEY}" with the key in Hermes's .env, was marked as
// needing attention. A reference names the key Hermes sends: one that
// reaches magpie is not drift and a sync keeps it; one magpie can't see is
// unknown, kept and not drift; one naming a key the gateway doesn't take
// is drift, and a sync puts magpie's key into the .env, the reference kept.
func TestHermesWithAKeyInItsEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_API_KEY", "")
	os.Unsetenv("MAGPIE_API_KEY")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	dir := appdir.HermesHomeIn(home)
	path := filepath.Join(dir, "config.yaml")
	env := filepath.Join(dir, ".env")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte("model:\n  default: x\n"), 0o644)
	a := hermes(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "hermes"})
	if err != nil {
		t.Fatal(err)
	}
	// the reporter's provider, as he wrote it
	mine := "model:\n  default: deepseek/pro\n  provider: magpie\nproviders:\n  magpie:\n    name: magpie\n    api_key: \"${MAGPIE_API_KEY}\"\n    api_mode: chat_completions\n    base_url: " + here(home).v1() + "\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := func(what string) {
		t.Helper()
		if v, _ := edit.GetYAML(path, "providers.magpie.api_key"); v != "${MAGPIE_API_KEY}" {
			t.Fatalf("%s: the user's reference was written over: %q", what, v)
		}
	}
	for _, c := range []struct{ name, line string }{
		{"a gateway key", "MAGPIE_API_KEY=" + secret},
		{"magpie's own key, quoted", "export MAGPIE_API_KEY=\"" + gateway.Token + "\""},
	} {
		os.WriteFile(env, []byte("OPENROUTER_API_KEY=sk-or\n"+c.line+"\n"), 0o600)
		if d := a.Check(); d != "" {
			t.Fatalf("%s in .env is drift: %s", c.name, d)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		ref(c.name)
		if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		ref(c.name + ", then a pick")
	}
	// ${env:VAR}, Hermes's other spelling, from the environment
	os.Remove(env)
	if err := edit.SetYAML(path, edit.KV{Path: "providers.magpie.api_key", Value: "${env:MAGPIE_API_KEY}"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_API_KEY", secret)
	if d := a.Check(); d != "" {
		t.Fatalf("a gateway key in the environment is drift: %s", d)
	}
	os.Unsetenv("MAGPIE_API_KEY")
	if err := edit.SetYAML(path, edit.KV{Path: "providers.magpie.api_key", Value: "${MAGPIE_API_KEY}"}); err != nil {
		t.Fatal(err)
	}
	// a variable only the shell sets, which magpie doesn't see: unknown
	if d := a.Check(); d != "" {
		t.Fatalf("a variable magpie can't see is drift: %s", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	ref("a variable magpie can't see")
	if _, err := os.Stat(env); err == nil {
		t.Fatal("a sync made a .env for a variable magpie can't see")
	}
	// a key the gateway doesn't take
	os.WriteFile(env, []byte("# keys\nMAGPIE_API_KEY=sk-not-this-gateways\nOTHER=1\n"), 0o600)
	if d := a.Check(); !strings.Contains(d, "api_key") {
		t.Fatalf("a key the gateway doesn't take is not reported: %q", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	ref("a key the gateway doesn't take")
	b, _ := os.ReadFile(env)
	if want := "# keys\nMAGPIE_API_KEY=" + gateway.Token + "\nOTHER=1\n"; string(b) != want {
		t.Fatalf(".env after a sync:\n%s\nwant:\n%s", b, want)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after a sync: %s", d)
	}
	// more than the one reference: magpie's key, as before
	if err := edit.SetYAML(path, edit.KV{Path: "providers.magpie.api_key", Value: "x${MAGPIE_API_KEY}"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(path, "providers.magpie.api_key"); v != gateway.Token {
		t.Fatalf("a value that doesn't reach magpie is kept: %q", v)
	}
}

// keyHolds, which every agent's check of magpie's key goes through: a key
// of the gateway's own stands in for magpie's, never for a URL or another
// value, and one the gateway doesn't take doesn't.
func TestKeyHoldsTakesAGatewayKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	secret, err := access.Update("add-key", access.Change{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		v, want string
		ok      bool
	}{
		{gateway.Token, gateway.Token, true},
		{secret, gateway.Token, true},
		{secret, gateway.TokenFor("alma"), true},
		{"sk-other", gateway.Token, false},
		{"", gateway.Token, false},
		{secret, "http://127.0.0.1:3425/v1", false},
		{secret, "chat_completions", false},
	} {
		if got := keyHolds(c.v, c.want); got != c.ok {
			t.Errorf("keyHolds(%q, %q) = %v", c.v, c.want, got)
		}
	}
	if d := wiringOff("X", "x.yaml", func(string) (string, bool) { return secret, true }, "api_key", gateway.Token); d != "" {
		t.Fatalf("wiringOff: %s", d)
	}
}

// A bot Hermes seeded or cloned from the connected config holds a copy of
// the model magpie set there and never reads the main config again: it
// follows the next model magpie sets, while a bot on a model its user
// picked keeps it (tjhhhh_84378 on Discord: another profile still showed
// the old model).
func TestHermesProfilesFollowMagpieModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max", "lite"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	model := func(p string) (string, string) {
		var c struct {
			Model struct{ Default, Model, Provider string }
		}
		b, _ := os.ReadFile(p)
		yaml.Unmarshal(b, &c)
		return c.Model.Default + c.Model.Model, c.Model.Provider
	}
	// connected before magpie kept a record of the model it set
	write("config.yaml", "model:\n  default: deepseek/pro\n  provider: magpie\n")
	// seeded by `hermes profile create` from that config
	seeded := write("profiles/writer/config.yaml", "# writer bot\nmodel:\n  default: deepseek/pro\n  provider: magpie\n")
	// a model its user picked in that bot, magpie's or its own
	picked := write("profiles/coder/config.yaml", "model:\n  default: deepseek/lite\n  provider: magpie\n")
	own := write("profiles/chat/config.yaml", "model:\n  default: deepseek/pro\n  provider: deepseek\n")
	// model.model, which Hermes reads with default unset
	old := write("profiles/old/config.yaml", "model:\n  model: deepseek/pro\n  provider: magpie\n")

	a := hermes(home)
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	check := func(when, p, want, wantProv string) {
		t.Helper()
		if m, prov := model(p); m != want || prov != wantProv {
			b, _ := os.ReadFile(p)
			t.Fatalf("%s: %s is on %s/%s, want %s/%s:\n%s", when, filepath.Base(filepath.Dir(p)), prov, m, wantProv, want, b)
		}
	}
	check("flash", seeded, "deepseek/flash", "magpie")
	check("flash", old, "deepseek/flash", "magpie")
	check("flash", picked, "deepseek/lite", "magpie")
	check("flash", own, "deepseek/pro", "deepseek")
	if b, _ := os.ReadFile(seeded); !strings.Contains(string(b), "# writer bot") {
		t.Fatalf("writer lost its comment:\n%s", b)
	}

	// the connected config on a model of its own between: the bots on
	// magpie's last model still follow the next one magpie sets
	if err := a.Apply("model", "moonshotai/kimi-k3"); err != nil {
		t.Fatal(err)
	}
	check("own", seeded, "deepseek/flash", "magpie")
	if err := a.Apply("model", "magpie/deepseek/max"); err != nil {
		t.Fatal(err)
	}
	check("max", seeded, "deepseek/max", "magpie")
	check("max", old, "deepseek/max", "magpie")
	check("max", picked, "deepseek/lite", "magpie")
	check("max", own, "deepseek/pro", "deepseek")

	// a bot whose user moves it off magpie's model, onto the one magpie
	// had before, keeps it
	edit.SetYAML(seeded, edit.KV{Path: "model.default", Value: "deepseek/flash"})
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	check("pro", seeded, "deepseek/flash", "magpie")
	check("pro", old, "deepseek/pro", "magpie")
}

// Switching magpie off and on again leaves every bot on a model `hermes`
// starts with (tjhhhh on Discord: after Off and On the bot's default model
// was gone and the hermes CLI failed at start). Off puts a bot on magpie
// onto the model the user had in the main config, with that provider's
// entry, or onto its own from when magpie connected it; On puts it back on
// the model it was on. A bot the user changed in between keeps their pick,
// and a bot an older magpie left with no model gets magpie's.
func TestHermesOffAndOnKeepsEveryBotsModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "lite"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	main := write("config.yaml", "model:\n  default: kimi-k3\n  provider: mine\nproviders:\n  mine:\n    base_url: https://x/v1\n    api_key: sk-mine\n")
	a := hermes(home)
	if c, err := a.ConnectHow(); err != nil || !a.Wired() {
		t.Fatalf("connect: %+v %v", c, err)
	}
	ref := hermesModelOf(main).Default
	// seeded from the connected config, picked in the bot, the bot's own
	seeded := write("profiles/writer/config.yaml", "model:\n  default: "+ref+"\n  provider: magpie\n")
	picked := write("profiles/coder/config.yaml", "model:\n  model: deepseek/lite\n  provider: magpie\n")
	own := write("profiles/chat/config.yaml", "model:\n  default: gpt-9\n  provider: openai\n")
	// emptied by an older magpie's Off
	empty := write("profiles/empty/config.yaml", "agent:\n  max_turns: 9\n")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	check := func(when, p string, want hermesModel) {
		t.Helper()
		if got := hermesModelOf(p); got.Provider != want.Provider || got.Default != want.Default {
			b, _ := os.ReadFile(p)
			t.Fatalf("%s: %s is on %+v, want %+v:\n%s", when, filepath.Base(filepath.Dir(p)), got, want, b)
		}
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	check("off", main, hermesModel{Provider: "mine", Default: "kimi-k3"})
	check("off", seeded, hermesModel{Provider: "mine", Default: "kimi-k3"})
	check("off", picked, hermesModel{Provider: "mine", Default: "kimi-k3"})
	check("off", own, hermesModel{Provider: "openai", Default: "gpt-9"})
	// the provider the bot is put on is there for it, as Hermes seeds it
	if v, _ := edit.GetYAML(seeded, "providers.mine.base_url"); v != "https://x/v1" {
		t.Fatalf("writer has no entry for its provider:\n%s", func() []byte { b, _ := os.ReadFile(seeded); return b }())
	}
	if v, _ := edit.GetYAML(picked, "model.model"); v != "" {
		t.Fatalf("coder keeps magpie's model.model %q", v)
	}

	// the user moves the coder bot to a model of their own while off
	edit.SetYAML(picked, edit.KV{Path: "model.default", Value: "gpt-9"}, edit.KV{Path: "model.provider", Value: "openai"})

	if c, err := a.ConnectHow(); err != nil || !a.Wired() {
		t.Fatalf("connect again: %+v %v", c, err)
	}
	check("on", main, hermesModel{Provider: "magpie", Default: ref})
	check("on", seeded, hermesModel{Provider: "magpie", Default: ref})
	check("on", picked, hermesModel{Provider: "openai", Default: "gpt-9"})
	check("on", own, hermesModel{Provider: "openai", Default: "gpt-9"})
	check("on", empty, hermesModel{Provider: "magpie", Default: ref})
	if v, _ := edit.GetYAML(empty, "agent.max_turns"); v != "9" {
		t.Fatal("the empty bot lost its other settings")
	}

	// the bots put back still follow the next model magpie sets
	next := "deepseek/flash"
	if ref == next {
		next = "deepseek/pro"
	}
	if err := a.Apply("model", "magpie/"+next); err != nil {
		t.Fatal(err)
	}
	check("next", seeded, hermesModel{Provider: "magpie", Default: next})
}

// A model changed in Hermes itself (`hermes model` in the main config)
// onto another of magpie's is what a bot made after holds: such a bot
// follows the next model magpie sets too, not only one on the model magpie
// had recorded (tjhhhh on Discord: switching for the other bots didn't
// take).
func TestHermesBotsFollowTheModelHermesPicked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	os.MkdirAll(root, 0o755)
	main := filepath.Join(root, "config.yaml")
	os.WriteFile(main, []byte("model:\n  default: x\n  provider: openrouter\n"), 0o644)
	a := hermes(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// `hermes model` in the main config, then a bot made from it
	edit.SetYAML(main, edit.KV{Path: "model.default", Value: "deepseek/flash"})
	bot := filepath.Join(root, "profiles", "writer", "config.yaml")
	os.MkdirAll(filepath.Dir(bot), 0o755)
	os.WriteFile(bot, []byte("model:\n  default: deepseek/flash\n  provider: magpie\n"), 0o644)
	if err := a.Apply("model", "magpie/deepseek/max"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(bot, "model.default"); v != "deepseek/max" {
		b, _ := os.ReadFile(bot)
		t.Fatalf("writer stayed:\n%s", b)
	}
}

// `hermes profile use coder` makes coder the bot `hermes` starts in
// (<root>/active_profile), and `hermes model` writes its config: magpie
// connects that one, so a model picked in magpie reaches the bot the user
// runs (tjhhhh on Discord), and the main config, another bot now, keeps the
// model the user picked there. A sticky name that is gone, deleted or not
// Hermes's, and a HERMES_HOME naming a profile, leave it as before.
func TestHermesStickyProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	main := write("config.yaml", "model:\n  default: kimi-k3\n  provider: moonshot\n")
	coder := write("profiles/coder/config.yaml", "model:\n  default: gpt-9\n  provider: openai\n")
	write("profiles/old/config.yaml", "model:\n  default: x\n")
	write("profiles/.deleted/old", "")

	for _, c := range []struct{ sticky, want string }{
		{"coder\n", filepath.Join(root, "profiles", "coder")},
		{"\ufeffcoder", filepath.Join(root, "profiles", "coder")},
		{"default\n", root},
		{"", root},
		{"gone\n", root},
		{"old\n", root},
		{"../x\n", root},
	} {
		write("active_profile", c.sticky)
		if got := hermes(home).Dir; got != c.want {
			t.Errorf("active_profile %q: connects %s, want %s", c.sticky, got, c.want)
		}
	}
	t.Setenv("HERMES_HOME", filepath.Join(root, "profiles", "old"))
	write("active_profile", "coder\n")
	if got := hermes(home).Dir; got != filepath.Join(root, "profiles", "old") {
		t.Errorf("HERMES_HOME naming a profile: connects %s", got)
	}
	t.Setenv("HERMES_HOME", "")

	a := hermes(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if m := hermesModelOf(coder); m.Provider != "magpie" || m.Default != "deepseek/pro" {
		t.Fatalf("the bot hermes starts in: %+v", m)
	}
	if m := hermesModelOf(main); m.Provider != "moonshot" || m.Default != "kimi-k3" {
		t.Fatalf("main: %+v", m)
	}
	if v, _ := edit.GetYAML(main, "providers.magpie.base_url"); v == "" {
		t.Fatal("main has no magpie in its picker")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m := hermesModelOf(coder); m.Provider != "openai" || m.Default != "gpt-9" {
		t.Fatalf("coder after Off: %+v", m)
	}
}

// Each of Hermes's other profiles has a model picker of its own
// (dongshanfengdongshizhcn on Discord: only the default profile's model
// could be set): it writes that profile's config alone, a magpie model
// stays when the connected config's model changes, an own model puts back
// the provider the profile had, and a profile nobody picks for keeps what
// it has. With the root's profile only, there is no such picker.
func TestHermesProfileModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	model := func(p string) (string, string) {
		var c struct {
			Model struct{ Default, Provider string }
		}
		b, _ := os.ReadFile(p)
		yaml.Unmarshal(b, &c)
		return c.Model.Default, c.Model.Provider
	}
	check := func(when, p, want, wantProv string) {
		t.Helper()
		if m, prov := model(p); m != want || prov != wantProv {
			b, _ := os.ReadFile(p)
			t.Fatalf("%s: %s is on %s/%s, want %s/%s:\n%s", when, p, prov, m, wantProv, want, b)
		}
	}
	main := write("config.yaml", "model:\n  default: anthropic/claude-opus-5\n  provider: openrouter\n")
	profileKeys := func(a *Agent) []string {
		var ks []string
		for _, f := range a.Fields {
			if strings.HasPrefix(f.Key, "profile:") {
				ks = append(ks, f.Key)
			}
		}
		return ks
	}
	if ks := profileKeys(hermes(home)); len(ks) != 0 {
		t.Fatalf("one profile only, yet pickers %v", ks)
	}

	coder := write("profiles/coder/config.yaml", "# coder bot\nmodel:\n  default: kimi-k3\n  provider: moonshot\nproviders:\n  mine:\n    base_url: https://x\n")
	writer := write("profiles/writer/config.yaml", "model:\n  default: gpt-5.5\n  provider: openai\n")
	write("profiles/old/config.yaml", "model:\n  default: x\n")
	write("profiles/.deleted/old", "")
	a := hermes(home)
	if ks := profileKeys(a); !slices.Equal(ks, []string{"profile:coder", "profile:writer"}) {
		t.Fatalf("pickers %v", ks)
	}
	if f := a.Field("profile:coder"); f == nil || f.Label != "coder" || f.Get() != "kimi-k3" {
		t.Fatalf("coder's picker: %+v", f)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// picked here: that profile alone, on magpie
	if err := a.Apply("profile:coder", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	check("coder on flash", coder, "deepseek/flash", "magpie")
	check("coder on flash", main, "deepseek/pro", "magpie")
	check("coder on flash", writer, "gpt-5.5", "openai")
	if b, _ := os.ReadFile(coder); !strings.Contains(string(b), "# coder bot") || !strings.Contains(string(b), "mine:") {
		t.Fatalf("coder lost its own settings:\n%s", b)
	}
	if v := hermes(home).Field("profile:coder").Get(); v != "magpie/deepseek/flash" {
		t.Fatalf("coder reads %q", v)
	}

	// picked the model the connected config is on: it stays when that one
	// changes, as it was picked for this profile
	if err := a.Apply("profile:coder", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/max"); err != nil {
		t.Fatal(err)
	}
	check("main on max", coder, "deepseek/pro", "magpie")
	check("main on max", writer, "gpt-5.5", "openai")

	// a model of its own: the provider it had before magpie comes back
	if err := a.Apply("profile:coder", "kimi-k3.5"); err != nil {
		t.Fatal(err)
	}
	check("coder on its own", coder, "kimi-k3.5", "moonshot")

	// Default on a profile on magpie: the model it had before magpie;
	// on one of its own model, it keeps it
	if err := a.Apply("profile:writer", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("profile:writer", ""); err != nil {
		t.Fatal(err)
	}
	check("writer on default", writer, "gpt-5.5", "openai")
	if err := a.Apply("profile:coder", ""); err != nil {
		t.Fatal(err)
	}
	check("coder on default", coder, "kimi-k3.5", "moonshot")

	// on magpie again, then Off and On: back on its own model, then on
	// the one picked for it
	if err := a.Apply("profile:writer", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	check("after disconnect", writer, "gpt-5.5", "openai")
	check("after disconnect", coder, "kimi-k3.5", "moonshot")
	if err := hermes(home).Connect(); err != nil {
		t.Fatal(err)
	}
	check("connected again", writer, "deepseek/flash", "magpie")
	check("connected again", coder, "kimi-k3.5", "moonshot")
	if err := hermes(home).Disconnect(); err != nil {
		t.Fatal(err)
	}

	// a bot put on magpie while the connected config isn't keeps magpie's
	// provider current: a model added since reaches its list at a sync
	a = hermes(home)
	if err := a.Apply("profile:writer", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max", "lite"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(writer); !strings.Contains(string(b), "deepseek/lite") {
		t.Fatalf("writer's provider after a sync:\n%s", b)
	}

	// with a sticky profile, the root's config is the profile "default"
	write("active_profile", "coder\n")
	a = hermes(home)
	if ks := profileKeys(a); !slices.Equal(ks, []string{"profile:default", "profile:writer"}) {
		t.Fatalf("sticky coder: pickers %v", ks)
	}
	if err := a.Apply("profile:default", "openrouter/x-model"); err != nil {
		t.Fatal(err)
	}
	if m, _ := model(main); m != "openrouter/x-model" {
		b, _ := os.ReadFile(main)
		t.Fatalf("default profile:\n%s", b)
	}
}

// Norethcy on X: Hermes's other profiles are agents of their own
// (hermes#<name>), so the Library can give each its skills and servers. The
// row is the profile's model picker, and none is listed with Hermes gone.
func TestHermesProfilesAreAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	root := appdir.HermesHomeIn(home)
	write := func(rel, s string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	write("config.yaml", "model:\n  default: anthropic/claude-opus-5\n  provider: openrouter\n")
	write("hermes-agent/run_agent.py", "")
	coder := write("profiles/coder/config.yaml", "model:\n  default: kimi-k3\n  provider: moonshot\n")
	write("profiles/old/config.yaml", "model:\n  default: x\n")
	write("profiles/.deleted/old", "")
	ids := func() (out []string) {
		for _, a := range onMachine(home, filepath.Join(home, ".config")) {
			if strings.HasPrefix(a.ID, "hermes") {
				out = append(out, a.ID)
			}
		}
		return
	}
	if got := ids(); !slices.Equal(got, []string{"hermes", "hermes#coder"}) {
		t.Fatalf("agents %v", got)
	}
	// Hermes uninstalled, its folder left behind: no profile is an agent
	os.RemoveAll(filepath.Join(root, "hermes-agent"))
	if got := ids(); len(got) != 1 {
		t.Fatalf("with Hermes gone: %v", got)
	}
	write("hermes-agent/run_agent.py", "")
	var row *Agent
	for _, a := range onMachine(home, filepath.Join(home, ".config")) {
		if a.ID == "hermes#coder" {
			row = a
		}
	}
	if row.Name != "Hermes · coder" || row.Dir != filepath.Dir(coder) || row.Path != coder || !row.Detected() {
		t.Fatalf("row: %+v", row)
	}
	if f := row.Field("model"); f == nil || f.Get() != "kimi-k3" {
		t.Fatalf("model field: %+v", f)
	}
}
