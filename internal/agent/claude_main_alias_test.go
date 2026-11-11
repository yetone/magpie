package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// TestClaudeMainModelIsAnAlias (iryan_sun on X): Claude Desktop's Code tab
// runs Claude Code on the user's own subscription with the provider
// routing managed by the host, so it drops the settings.json env (the
// gateway's address, the tiers) but still takes the top-level "model".
// magpie's provider-prefixed id there went to Anthropic, which doesn't
// know it, and every request failed (WebFetch among them). The main model
// is written as the alias of the tier that carries it, which the env gives
// magpie's model and Anthropic's own gives the real tier.
func TestClaudeMainModelIsAnAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "anth", Name: "Anthropic", Chat: "https://api.anthropic.com/v1", Key: "k", Models: []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"}},
		{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	for _, main := range []string{"anth/claude-opus-4-7", "anth/claude-sonnet-4-6", "deepseek/pro"} {
		if err := a.Field("model").Set(main); err != nil {
			t.Fatal(err)
		}
		m, _ := edit.GetJSON(path, "model")
		if strings.Contains(m, "/") || !contains(claudeAliases, strings.TrimSuffix(m, "[1m]")) {
			t.Fatalf("%s: settings.json model is %q, which Anthropic's API doesn't know", main, m)
		}
		if got, _ := edit.GetJSON(path, "env."+tierEnv(strings.TrimSuffix(m, "[1m]"))); strings.TrimSuffix(got, "[1m]") != main {
			t.Fatalf("%s: the alias %q is on %q", main, m, got)
		}
		if got := a.Field("model").Get(); got != main {
			t.Fatalf("%s: shown as %q", main, got)
		}
		if got := claudeMain(path); got != main {
			t.Fatalf("%s: claudeMain %q", main, got)
		}
	}
	// a Sync leaves it as it is, and a settings.json an older magpie wrote
	// with the prefixed id is brought to the alias
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); strings.Contains(m, "/") {
		t.Fatalf("after Sync: %q", m)
	}
	if got := a.Field("model").Get(); got != "deepseek/pro" {
		t.Fatalf("after Sync shown as %q", got)
	}
	// a model the user set themselves is never rewritten
	for _, own := range []string{"claude-haiku-9", "own/xyz", "opusplan"} {
		if err := edit.SetJSON(path, edit.KV{Path: "model", Value: own}); err != nil {
			t.Fatal(err)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if m, _ := edit.GetJSON(path, "model"); m != own {
			t.Fatalf("the user's %q became %q", own, m)
		}
	}
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	// Disconnect leaves no alias on magpie's tiers behind
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	if m, has := edit.GetJSON(path, "model"); has && m != "" {
		t.Fatalf("after Unwire: model %q", m)
	}
}

// writtenModel is settings.json's main model as the id it stands for: an
// alias magpie wrote resolved through its tier's env, anything else as is.
func writtenModel(path string) string {
	m, _ := edit.GetJSON(path, "model")
	if t := strings.TrimSuffix(m, "[1m]"); contains(claudeTiers, t) {
		v, _ := edit.GetJSON(path, "env."+tierEnv(t))
		return v
	}
	return m
}
