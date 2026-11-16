package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// KoiLx0 on X: a Claude Code connected to magpie on a computer it had never
// run on still opened on the theme picker and the sign-in choice, because
// its first run is marked done in its global config (~/.claude.json,
// hasCompletedOnboarding), which nothing had written. Connecting it writes
// the mark beside the user's other keys, a Sync marks one connected before,
// and a disconnect takes out the mark magpie wrote, back to Claude Code as
// installed. A Claude Code that ran its own first run keeps its mark.
func TestClaudeConnectMarksFirstRunDone(t *testing.T) {
	setup := func(t *testing.T) (home, path, global string) {
		home = t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(home, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return home, path, filepath.Join(home, ".claude.json")
	}
	mark := func(global string) string { v, _ := edit.GetJSON(global, "hasCompletedOnboarding"); return v }

	t.Run("fresh", func(t *testing.T) {
		home, _, global := setup(t)
		a := claude(home)
		if err := a.Field("model").Set("deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if mark(global) != "true" {
			b, _ := os.ReadFile(global)
			t.Fatalf("connected Claude Code's first run isn't marked done:\n%s", b)
		}
		if err := a.Field("model").Set(""); err != nil {
			t.Fatal(err)
		}
		if mark(global) != "" {
			t.Fatal("disconnecting kept the mark magpie wrote")
		}
	})

	t.Run("connected before", func(t *testing.T) {
		home, path, global := setup(t)
		a := claude(home)
		if err := a.Field("model").Set("deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		// as an older magpie left it: wired, the first run not marked
		if err := os.Remove(global); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		forget("claude.onboarded")
		if err := claude(home).Sync(); err != nil {
			t.Fatal(err)
		}
		if mark(global) != "true" {
			b, _ := os.ReadFile(path)
			t.Fatalf("a Sync left the first run unmarked:\n%s", b)
		}
	})

	t.Run("own first run", func(t *testing.T) {
		home, _, global := setup(t)
		own := `{"theme": "light", "hasCompletedOnboarding": true, "lastOnboardingVersion": "2.1.316", "projects": {}}`
		if err := os.WriteFile(global, []byte(own), 0o600); err != nil {
			t.Fatal(err)
		}
		a := claude(home)
		if err := a.Field("model").Set("deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if err := a.Field("model").Set(""); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(global); string(b) != own {
			t.Fatalf("the user's own first run was touched:\n%s", b)
		}
	})

	t.Run("other keys kept", func(t *testing.T) {
		home, _, global := setup(t)
		if err := os.WriteFile(global, []byte(`{"theme": "light", "numStartups": 3}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude(home).Field("model").Set("deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if v, _ := edit.GetJSON(global, "theme"); v != "light" || mark(global) != "true" {
			b, _ := os.ReadFile(global)
			t.Fatalf("theme lost or not marked:\n%s", b)
		}
	})

	t.Run("unparseable left alone", func(t *testing.T) {
		home, _, global := setup(t)
		bad := `{"theme": "light",`
		if err := os.WriteFile(global, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude(home).Field("model").Set("deepseek/pro"); err != nil {
			t.Fatalf("a global config magpie can't read failed the connect: %v", err)
		}
		if b, _ := os.ReadFile(global); string(b) != bad {
			t.Fatalf("an unparseable global config was written:\n%s", b)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR", func(t *testing.T) {
		home, _, _ := setup(t)
		dir := filepath.Join(home, "cc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		if err := claude(home).Field("model").Set("deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if mark(filepath.Join(dir, ".claude.json")) != "true" {
			t.Fatal("the first run isn't marked in $CLAUDE_CONFIG_DIR/.claude.json, where Claude Code reads it")
		}
		if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
			t.Fatal("~/.claude.json was written for a Claude Code kept in $CLAUDE_CONFIG_DIR")
		}
	})
}
