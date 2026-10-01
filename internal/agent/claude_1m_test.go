package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// TestClaude1MMarked: every name magpie hands Claude Code for a model of a
// 1M window carries [1m], however it was set: Claude Code 2.1.284 and before
// take "claude/claude-opus-5-5" unmarked for 200K, whatever
// CLAUDE_CODE_MAX_CONTEXT_TOKENS says, and compact it again and again
// ("Autocompact is thrashing").
func TestClaude1MMarked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"small", "big"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{
		{ID: "small", Context: 200000}, {ID: "big", Context: 1000000},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"model", "env.ANTHROPIC_MODEL", "env.ANTHROPIC_SMALL_FAST_MODEL", "env.CLAUDE_CODE_SUBAGENT_MODEL"}
	for _, tier := range claudeTiers {
		names = append(names, "env."+tierEnv(tier))
	}

	for _, v := range []string{"v/big", "v/big[1m]"} {
		set("model", v)
		for _, k := range names {
			if got := get(k); got != "v/big[1m]" {
				t.Fatalf("model %q: %s = %q, want v/big[1m]", v, k, got)
			}
		}
	}
	// a tier set on its own is marked too; a 200K one is not
	set("model", "v/small")
	if got := get("env.ANTHROPIC_MODEL"); got != "v/small" {
		t.Fatalf("a 200K model got marked: %q", got)
	}
	set("haiku", "v/big")
	for _, k := range []string{"env." + tierEnv("haiku"), "env.ANTHROPIC_SMALL_FAST_MODEL"} {
		if got := get(k); got != "v/big[1m]" {
			t.Fatalf("haiku on v/big: %s = %q", k, got)
		}
	}
}
