package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Code compacts at the percent of its window the user picked for it
// in magpie (moooyo on Discord: compact once 65% of the context is used),
// through CLAUDE_AUTOCOMPACT_PCT_OVERRIDE, whichever window it runs to: a
// Claude model's whole one too. None picked takes magpie's out; one the
// user set themselves is theirs, never written over or taken out; and it
// goes with the rest of magpie's when Claude Code leaves magpie.
func TestClaudeCompactPercent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "claude-opus-5-5", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	pct := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactPctEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what, want string) {
		t.Helper()
		if got := pct(); got != want {
			t.Fatalf("%s: %q; want %q", what, got, want)
		}
	}

	must(a.Field("model").Set("v/big[1m]"))
	expect("none picked", "")
	must(provider.SetAgentCompactPct("claude", 65))
	must(a.Sync())
	expect("65 picked", "65")
	// the window it is a percent of stays magpie's
	if w, _ := edit.GetJSON(path, "env."+claudeCompactEnv); w != "272000" {
		t.Fatalf("the window beside it: %q", w)
	}
	must(a.Field("model").Set("v/claude-opus-5-5[1m]"))
	expect("a Claude model's whole window", "65")
	must(provider.SetAgentCompactPct("claude", 80))
	must(a.Sync())
	expect("80 picked", "80")
	// another agent's is not Claude Code's
	must(provider.SetAgentCompactPct("claude", 0))
	must(provider.SetAgentCompactPct("codex", 70))
	must(a.Sync())
	expect("cleared", "")

	// leaving magpie takes magpie's out
	must(provider.SetAgentCompactPct("claude", 65))
	must(a.Sync())
	expect("picked again", "65")
	must(a.Field("model").Set(""))
	expect("off magpie", "")

	// the user's own stays as they set it, picked or cleared, on magpie or off
	must(edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactPctEnv, Value: "50"}))
	must(a.Field("model").Set("v/big[1m]"))
	must(a.Sync())
	expect("the user's own, 65 picked", "50")
	must(provider.SetAgentCompactPct("claude", 0))
	must(a.Sync())
	expect("the user's own, cleared", "50")
	must(a.Field("model").Set(""))
	expect("the user's own, off magpie", "50")
	if s := settings.Load(); s.AgentCompactPcts["claude"] != 0 || s.AgentCompactPcts["codex"] != 70 {
		t.Fatalf("settings: %v", s.AgentCompactPcts)
	}
}
