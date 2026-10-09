package provider

import (
	"slices"
	"testing"
)

// A provider removed leaves the groups magpie found, the ones the user
// changed too, rather than staying in them to be taken out by hand
// (Discord); one switched off is skipped and back once it is on again.
// The user's own groups keep their members as they were, and a found
// group's manual pick stays even when its provider is gone.
func TestFoundGroupFollowsItsProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	add := func(id string) {
		t.Helper()
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		add(id)
	}
	members := func(id string) []string {
		t.Helper()
		g, ok := groupOf(Groups(), id)
		if !ok {
			t.Fatalf("no group %s", id)
		}
		return g.Members
	}
	routed := func(id string) (out []string) {
		_, ms, _ := FindGroup(GroupPrefix + id)
		for _, m := range ms {
			out = append(out, m.Provider.ID+"/"+m.Model)
		}
		return out
	}
	all := []string{"a/x", "b/x", "c/x"}
	if got := members("auto-x"); !slices.Equal(got, all) {
		t.Fatalf("found: %v", got)
	}

	// changed, it is the user's, stored with its members
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: all, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"a/y", "c/y"}}); err != nil {
		t.Fatal(err)
	}

	// switched off: kept in it, skipped, and sent to again once on
	if err := SetOff("c", true); err != nil {
		t.Fatal(err)
	}
	if got := members("auto-x"); !slices.Equal(got, all) {
		t.Fatalf("c off, auto-x's members: %v", got)
	}
	if got := routed("auto-x"); !slices.Equal(got, []string{"a/x", "b/x"}) {
		t.Fatalf("c off, auto-x routes to %v", got)
	}
	if err := SetOff("c", false); err != nil {
		t.Fatal(err)
	}
	if got := routed("auto-x"); !slices.Equal(got, all) {
		t.Fatalf("c on again, auto-x routes to %v", got)
	}

	// removed: out of the found group, not of the user's own
	if err := Delete("c"); err != nil {
		t.Fatal(err)
	}
	if got := members("auto-x"); !slices.Equal(got, []string{"a/x", "b/x"}) {
		t.Fatalf("c removed, auto-x's members: %v", got)
	}
	if got := members("mine"); !slices.Equal(got, []string{"a/y", "c/y"}) {
		t.Fatalf("c removed, the user's own group changed: %v", got)
	}
	// read so, not written: a provider back under its id is back in it
	add("c")
	if got := members("auto-x"); !slices.Equal(got, all) {
		t.Fatalf("c back, auto-x's members: %v", got)
	}

	// a manual group's pick stays, gone or not: the group would send
	// elsewhere unasked
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: all, Routing: Manual, Pick: "c/x"}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("c"); err != nil {
		t.Fatal(err)
	}
	g, _ := groupOf(Groups(), "auto-x")
	if !slices.Equal(g.Members, all) || g.Picked() != "c/x" {
		t.Fatalf("c removed, the manual pick: %v %s", g.Members, g.Picked())
	}
	// and a save of what was read keeps it as the user has it
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: []string{"a/x", "b/x", "c/x"}, Routing: Ordered, Off: []string{"c/x"}}); err != nil {
		t.Fatal(err)
	}
	if g, _ := groupOf(Groups(), "auto-x"); !slices.Equal(g.Members, []string{"a/x", "b/x"}) || len(g.Off) != 0 {
		t.Fatalf("ordered again, c's member is still read: %v off %v", g.Members, g.Off)
	}
}
