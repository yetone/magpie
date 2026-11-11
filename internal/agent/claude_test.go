package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }

	if err := a.Field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("a tier before Claude Code is routed should fail")
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/pro" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" || a.Field("haiku").Get() != "" {
		t.Fatalf("tiers should follow the model: %v", a.Values())
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/flash" || env("ANTHROPIC_SMALL_FAST_MODEL") != "deepseek/flash" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("haiku: %v", a.Values())
	}
	if env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/pro" || a.Field("haiku").Get() != "deepseek/flash" {
		t.Fatalf("others: %v", a.Values())
	}
	// a new main model carries the tiers that followed it, not haiku
	if err := a.Field("model").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// fable is left unset on a model that is no Fable (#2090)
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "deepseek/pro" || env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "" {
		t.Fatalf("sonnet should follow, fable be unset: %v", a.Values())
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/pro" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("haiku back to the model: %v", a.Values())
	}
	// back to Claude Code as installed
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "" || env("ANTHROPIC_BASE_URL") != "" {
		t.Fatal("reset left env behind")
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatal("theme lost")
	}
}

// Claude Code's subagents can have a model of their own (#468):
// CLAUDE_CODE_SUBAGENT_MODEL, the model a subagent runs on when neither the
// call nor its definition names one. Unset, magpie writes it as before;
// set, it outlives a tier of its own and a new main model.
func TestClaudeSubagentModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "lite"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	sub := a.Field("subagent")
	if sub == nil {
		t.Fatal("no subagent field")
	}
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	if err := sub.Set("deepseek/flash"); err == nil {
		t.Fatal("a subagent model before Claude Code is routed should fail")
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if sub.Get() != "" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("subagents should follow the model: %v", a.Values())
	}
	if err := sub.Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if sub.Get() != "deepseek/flash" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "deepseek/flash" || env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/pro" {
		t.Fatalf("subagents: %v", a.Values())
	}
	if err := a.Field("haiku").Set("deepseek/lite"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/lite"); err != nil {
		t.Fatal(err)
	}
	if m := writtenModel(path); env("CLAUDE_CODE_SUBAGENT_MODEL") != "deepseek/flash" || m != "deepseek/lite" {
		t.Fatalf("own subagent model lost: %v", a.Values())
	}
	if err := sub.Set(""); err != nil {
		t.Fatal(err)
	}
	if sub.Get() != "" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("back to the model: %v", a.Values())
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatal("reset left the subagent model behind")
	}
}

// Claude Code's settings keep an effort up to xhigh; max is its
// CLAUDE_CODE_EFFORT_LEVEL, which another level takes away again.
func TestClaudeEffortMax(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"effortLevel": "high", "env": {"FOO": "1"}}`), 0o644)
	e := claude(home).Field("effort")
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	if e.Get() != "high" {
		t.Fatalf("get %q", e.Get())
	}
	if err := e.Set("max"); err != nil {
		t.Fatal(err)
	}
	if e.Get() != "max" || get("env.CLAUDE_CODE_EFFORT_LEVEL") != "max" || get("env.FOO") != "1" {
		b, _ := os.ReadFile(path)
		t.Fatalf("max:\n%s", b)
	}
	if err := e.Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if e.Get() != "xhigh" || get("env.CLAUDE_CODE_EFFORT_LEVEL") != "" || get("effortLevel") != "xhigh" || get("env.FOO") != "1" {
		b, _ := os.ReadFile(path)
		t.Fatalf("xhigh:\n%s", b)
	}
	e.Set("max")
	if err := e.Set(""); err != nil || e.Get() != "" {
		t.Fatalf("reset: %v %q", err, e.Get())
	}
	if e.Set("ultra") == nil {
		t.Fatal("ultra taken")
	}
}

// #2545: a key made under Gateway keys, put in Claude Code's
// ANTHROPIC_AUTH_TOKEN in place of magpie's, goes through magpie all the
// same, so a model pick keeps it, as a sync keeps one. A key the gateway
// doesn't take is still wired over.
func TestClaudePickKeepsAGatewayKeyOfTheUsers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); tok == "" {
		b, _ := os.ReadFile(path)
		t.Fatalf("not wired:\n%s", b)
	}
	secret, err := access.Update("add-key", access.Change{Name: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: secret}); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("a key the gateway issued is drift: %s", d)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); v != secret {
		b, _ := os.ReadFile(path)
		t.Fatalf("a model pick wrote over the user's caller key:\n%s", b)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "sk-not-this-gateways"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); v == "sk-not-this-gateways" {
		t.Fatalf("a key the gateway doesn't take is kept: %q", v)
	}
}

// #2545's review: the sharing key magpie itself made is never the user's.
// Claude Code is wired to an address beyond loopback (MAGPIE_ADDR), where
// magpie writes the sharing key while sharing is on and gateway.Token when
// it is off; a pick kept the sharing key, which still authenticates (it
// stays enabled in caller-keys.json with sharing off), so sharing off
// never brought gateway.Token back. Claude has no WSL twin: the address is
// how its writeTiers reaches the sharing key, as a distro is Codex's.
func TestClaudeSharingOffWiresGatewayTokenBack(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "192.0.2.10:3425")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); tok != gateway.Token {
		b, _ := os.ReadFile(path)
		t.Fatalf("wired with sharing off: key %q, want magpie's own\n%s", tok, b)
	}
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	lan := access.LANSecret()
	if lan == "" {
		t.Fatal("no sharing key")
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); tok != lan {
		t.Fatalf("sharing on: key %q, want the sharing key", tok)
	}
	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); tok != gateway.Token {
		t.Fatalf("sharing off: key %q, want gateway.Token back", tok)
	}
}
