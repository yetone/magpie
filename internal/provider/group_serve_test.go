package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The user's own groups wait on plugins/groups only where the gateway is
// served: ungated (the CLI, the TUI) they are listed; gated, only while
// ServeGroups is held. The groups magpie finds are listed either way, and
// NamedGroups, which keeps the plugin on, counts the user's own regardless.
func TestOwnGroupsWaitOnThePlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := store(file{Groups: []Group{{ID: "mine", Name: "Mine", Members: []string{"a/x", "b/y"}}}}); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{ID: "a/x", Model: "x", Provider: &Provider{ID: "a"}}, {ID: "b/y", Model: "y", Provider: &Provider{ID: "b"}},
		{ID: "a/m", Model: "m", Provider: &Provider{ID: "a"}}, {ID: "b/m", Model: "m", Provider: &Provider{ID: "b"}},
	}
	listed := func() []string {
		var ids []string
		for _, e := range groupEntries(entries) {
			ids = append(ids, e.ID)
		}
		slices.Sort(ids)
		return ids
	}
	both, found := []string{"auto/m", "group/mine"}, []string{"auto/m"}
	check := func(when string, want []string) {
		t.Helper()
		if got := listed(); !slices.Equal(got, want) {
			t.Fatalf("%s: %v, want %v", when, got, want)
		}
		if n, err := NamedGroups(); n != 1 || err != nil {
			t.Fatalf("%s: NamedGroups %d, %v", when, n, err)
		}
	}
	check("ungated", both)
	stop := GateGroups(nil)
	check("gated, plugin off", found)
	remove := ServeGroups()
	check("plugin on", both)
	remove()
	remove() // once
	check("plugin off again", found)
	stop()
	check("gateway stopped", both)
}

// A providers.json that can't be read is not "no groups": the plugin that
// counts them stays on.
func TestNamedGroupsUnreadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(Path(), 0o755); err != nil { // a folder where the file goes
		t.Fatal(err)
	}
	if _, err := NamedGroups(); err == nil {
		t.Fatal("no error for a providers.json that can't be read")
	}
}

// Gated and off, a lookup that finds a group of the user's own asks the
// gateway's refresh first (which turns the plugin on), so one made a
// moment ago is served at once, not on the runtime's next look; a refresh
// that leaves it off is asked at most once a second, and never for the
// groups magpie found alone.
func TestOwnGroupRefreshesTheGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	entries := []Entry{{ID: "a/m", Model: "m", Provider: &Provider{ID: "a"}}, {ID: "b/m", Model: "m", Provider: &Provider{ID: "b"}}}
	asked := 0
	var remove func()
	defer func() {
		if remove != nil {
			remove()
		}
	}()
	refresh := func() {
		asked++
		if asked == 2 {
			remove = ServeGroups() // the plugin found in use, turned on
		}
	}
	defer GateGroups(refresh)()
	groupRefreshed.Store(0)
	groupEntries(entries)
	// a found group the user edited is kept in the file, Auto still
	if err := store(file{Groups: []Group{{ID: "auto-m", Name: "m", Auto: true, Members: []string{"b/m", "a/m"}}}}); err != nil {
		t.Fatal(err)
	}
	groupEntries(entries)
	if asked != 0 {
		t.Fatalf("refreshed %d times with found groups alone", asked)
	}
	if err := store(file{Groups: []Group{{ID: "mine", Name: "Mine", Members: []string{"a/m"}}}}); err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		return slices.ContainsFunc(groupEntries(entries), func(e Entry) bool { return e.ID == "group/mine" })
	}
	if has() || asked != 1 {
		t.Fatalf("a refresh that leaves it off: listed %v, asked %d", has(), asked)
	}
	has()
	if asked != 1 {
		t.Fatalf("asked again within a second: %d", asked)
	}
	groupRefreshed.Store(0)
	if !has() || asked != 2 {
		t.Fatalf("a refresh that turns it on: listed %v, asked %d", has(), asked)
	}
}
