package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// On a Claude model, Claude Code's haiku tier — its titles, Explore
// subagents, the small asks it makes on its own — follows on the Haiku the
// same provider serves, as Claude Code runs them by itself, not on the main
// model (X, AncientTwo: a Claude subscription's limits went much faster
// through magpie). A model of the user's own for it stays; on a model that
// isn't Claude's it follows the main model as before.
func TestClaudeHaikuTierOnClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "anth", Name: "Anthropic", Chat: "https://api.anthropic.com/v1", Key: "k", Models: []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-haiku-3-5"}},
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
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	tiers := func() [3]string {
		return [3]string{env("ANTHROPIC_DEFAULT_OPUS_MODEL"), env("ANTHROPIC_DEFAULT_HAIKU_MODEL"), env("ANTHROPIC_SMALL_FAST_MODEL")}
	}

	if err := a.Field("model").Set("anth/claude-opus-4-7"); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got != [3]string{"anth/claude-opus-4-7", "anth/claude-haiku-4-5", "anth/claude-haiku-4-5"} {
		t.Fatalf("haiku should be the provider's Haiku: %v", got)
	}
	if a.Field("haiku").Get() != "anth/claude-haiku-4-5" {
		t.Fatalf("haiku shown: %q", a.Field("haiku").Get())
	}
	// another Claude model: haiku stays on Haiku
	if err := a.Field("model").Set("anth/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got != [3]string{"anth/claude-sonnet-4-6", "anth/claude-haiku-4-5", "anth/claude-haiku-4-5"} {
		t.Fatalf("on sonnet: %v", got)
	}
	// a model that isn't Claude's: haiku follows it
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got != [3]string{"deepseek/pro", "deepseek/pro", "deepseek/pro"} {
		t.Fatalf("on deepseek: %v", got)
	}
	if err := a.Field("model").Set("anth/claude-opus-4-7"); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got[1] != "anth/claude-haiku-4-5" {
		t.Fatalf("back on Claude: %v", got)
	}
	// the user's own pick, even the main model, stays
	if err := a.Field("haiku").Set("anth/claude-opus-4-7"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("anth/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got[1] != "anth/claude-opus-4-7" {
		t.Fatalf("own haiku lost: %v", got)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got[1] != "anth/claude-opus-4-7" {
		t.Fatalf("own haiku lost on Follow: %v", got)
	}
	// back to following: the Haiku again
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got[1] != "anth/claude-haiku-4-5" {
		t.Fatalf("haiku back to following: %v", got)
	}
	// an effort for the haiku tier keeps it on the Haiku
	if err := a.Field("haiku_effort").Set("low"); err == nil {
		if got := tiers(); got[1] != "anth/claude-haiku-4-5:low" {
			t.Fatalf("haiku effort: %v", got)
		}
		if err := a.Field("haiku_effort").Set(""); err != nil {
			t.Fatal(err)
		}
	}

	// an older magpie wrote every tier on the main model: Follow moves the
	// haiku tier onto the Haiku
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: "anth/claude-sonnet-4-6"}, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: "anth/claude-sonnet-4-6"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if got := tiers(); got[1] != "anth/claude-haiku-4-5" || got[2] != "anth/claude-haiku-4-5" {
		t.Fatalf("older wiring not moved: %v", got)
	}
}

func TestClaudeLight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "anth", Name: "Claude", Chat: "https://api.anthropic.com/v1", Key: "k", Models: []string{"claude-opus-5-5", "claude-haiku-4-5-20251001", "claude-haiku-4-5", "claude-haiku-3-5"}},
		{ID: "or", Name: "Router", Chat: "https://or.test/v1", Key: "k", Models: []string{"anthropic/claude-opus-5.5", "anthropic/claude-haiku-4.5"}},
		{ID: "cp", Name: "Copilot", Chat: "https://cp.test/v1", Key: "k", Models: []string{"claude-opus-5.5", "gpt-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for main, want := range map[string]string{
		"anth/claude-opus-5-5":         "anth/claude-haiku-4-5",
		"anth/claude-opus-5-5[1m]":     "anth/claude-haiku-4-5",
		"anth/claude-haiku-4-5":        "",
		"or/anthropic/claude-opus-5.5": "or/anthropic/claude-haiku-4.5",
		"cp/claude-opus-5.5":           "",
		"cp/gpt-5":                     "",
	} {
		if got := claudeLight(main); got != want {
			t.Errorf("claudeLight(%q) = %q, want %q", main, got, want)
		}
	}
}
