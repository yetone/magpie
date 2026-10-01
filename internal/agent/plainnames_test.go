package agent

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The model lists magpie writes into agents' own files (OpenCode, Pi, …)
// name models alone once the user wants it (#335), as Codex's catalog
// does: "Sol", not "Sol · A", and a group "Mix", not "Mix · routing group".
func TestPlainNamesReachAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Changed = nil
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"sol", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mix", Members: []string{"a/sol", "a/flash"}}); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		var out []string
		for _, m := range magpieModels("opencode") {
			out = append(out, m.ID+"="+m.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(); !slices.Equal(got, []string{"a/flash=flash · A", "a/sol=sol · A", "group/mix=Mix · routing group"}) {
		t.Fatalf("by default: %q", got)
	}
	if err := provider.SetPlainNames(true); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"a/flash=flash", "a/sol=sol", "group/mix=Mix"}) {
		t.Fatalf("plain: %q", got)
	}
}
