package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// On a Claude model that is no Fable, Claude Code's fable tier is left
// unset rather than following the main model: Claude Code takes the model
// ANTHROPIC_DEFAULT_FABLE_MODEL names for a Fable model, so Opus written
// there was Fable to it, and a Claude Pro sign-in without Fable asked for
// usage credits and fell back to Haiku (Zhenzhen on Discord, magpie claude
// claude/claude-opus-5-5). On a Fable, or another vendor's model, it follows
// as before; a value of the user's own stays theirs.
func TestClaudeFableTierOnClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "anth", Name: "Anthropic", Chat: "https://api.anthropic.com/v1", Key: "k", Models: []string{"claude-opus-5-5", "claude-sonnet-5", "claude-fable-5-1", "claude-haiku-4-5"}},
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
	// the user's own fable model, from before magpie
	if err := os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_DEFAULT_FABLE_MODEL":"my-fable"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	fable := func() (string, bool) { return edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL") }
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	noFable := func(when string) {
		t.Helper()
		if v, has := fable(); has {
			t.Fatalf("%s: fable tier written as %q, want it left out:\n%s", when, v, readFile(path))
		}
	}

	if err := a.Field("model").Set("anth/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	noFable("on Opus")
	if env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "anth/claude-opus-5-5" || env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "anth/claude-opus-5-5" || env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "anth/claude-haiku-4-5" {
		t.Fatalf("the other tiers should follow as before:\n%s", readFile(path))
	}
	if a.Field("fable").Get() != "" {
		t.Fatalf("fable shown as %q, want following", a.Field("fable").Get())
	}
	// Claude Code's own fable, asked of the gateway, runs on the main model
	if got := claudeStandInAt(path, "claude-fable-5-1", env("ANTHROPIC_BASE_URL")); got != "anth/claude-opus-5-5" {
		t.Fatalf("claude-fable-5-1 stands in as %q", got)
	}
	// another Claude model that is no Fable
	if err := a.Field("model").Set("anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	noFable("on Sonnet")
	// a Fable: the fable tier follows it
	if err := a.Field("model").Set("anth/claude-fable-5-1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "anth/claude-fable-5-1" {
		t.Fatalf("on Fable, fable is %q", v)
	}
	if err := a.Field("model").Set("anth/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	noFable("back from Fable")
	// another vendor's model: it follows as before
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "deepseek/pro" {
		t.Fatalf("on deepseek, fable is %q", v)
	}
	if err := a.Field("model").Set("anth/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	noFable("back from deepseek")

	// the user's pick of the main model for fable is theirs: it stays
	if err := a.Field("fable").Set("anth/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "anth/claude-opus-5-5" || a.Field("fable").Get() != "anth/claude-opus-5-5" {
		t.Fatalf("own fable pick: %q, shown %q", v, a.Field("fable").Get())
	}
	if err := a.Field("model").Set("anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "anth/claude-opus-5-5" {
		t.Fatalf("own fable pick lost: %q", v)
	}
	if err := a.Field("fable").Set(""); err != nil {
		t.Fatal(err)
	}
	noFable("fable back to following")

	// an effort picked for the fable tier keeps it on the main model at that
	// effort, as the user asked
	if err := a.Field("fable_effort").Set("low"); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "anth/claude-sonnet-5:low" {
		t.Fatalf("fable effort: %q", v)
	}
	if err := a.Field("fable_effort").Set(""); err != nil {
		t.Fatal(err)
	}
	noFable("fable effort taken away")

	// one the user wrote themselves while routed, not a model of magpie's,
	// stays as written when the main model moves
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_DEFAULT_FABLE_MODEL", Value: "claude-fable-5-1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("anth/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "claude-fable-5-1" {
		t.Fatalf("the user's own fable written over: %q", v)
	}
	if err := edit.DelJSON(path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL"); err != nil {
		t.Fatal(err)
	}

	// an older magpie wrote fable on the main model: Follow takes it out
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_DEFAULT_FABLE_MODEL", Value: "anth/claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	noFable("older wiring after Follow")

	// magpie steps out: the user's own fable model is back
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := fable(); v != "my-fable" {
		t.Fatalf("the user's own fable not given back: %q\n%s", v, readFile(path))
	}
}
