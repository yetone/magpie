package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// TestClaudeCompactWindow: on magpie Claude Code compacts at the working
// window (CLAUDE_CODE_AUTO_COMPACT_WINDOW), a [1m] model too, so a 1M model's
// conversation isn't run to 1M with every turn sending all of it (X: Chen);
// settings.FullContext takes it away, a value the user set is theirs, and
// Claude Code as installed has none.
func TestClaudeCompactWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(a.Field("model").Set("v/big[1m]"))
	if compact() != "272000" {
		t.Fatalf("on magpie: %q", compact())
	}
	must(settings.Save(settings.Settings{FullContext: true}))
	must(a.Sync())
	if compact() != "" {
		t.Fatalf("full context: %q", compact())
	}
	must(settings.Save(settings.Settings{}))
	must(a.Sync())
	if compact() != "272000" {
		t.Fatalf("back to the working window: %q", compact())
	}
	must(a.Field("model").Set(""))
	if compact() != "" {
		t.Fatalf("Claude Code as installed: %q", compact())
	}
	// the user's own is left as it is
	must(edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactEnv, Value: "500000"}))
	must(a.Field("model").Set("v/big[1m]"))
	must(a.Sync())
	if compact() != "500000" {
		t.Fatalf("the user's own: %q", compact())
	}
	must(a.Field("model").Set(""))
	if compact() != "500000" {
		t.Fatalf("reset took the user's own: %q", compact())
	}
}
