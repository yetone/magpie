package provider

import (
	"slices"
	"strings"
	"testing"
)

// Several groups are removed at once (lc on Discord: they could only be
// removed one at a time): the user's go, the found ones are kept removed,
// a group held by another being removed with it goes after that one, and
// the whole is refused, with none removed, when one isn't there or a group
// left holds one.
func TestDeleteGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y", "z"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []Group{
		{Name: "Inner", Members: []string{"a/x", "b/y"}},
		{Name: "Outer", Members: []string{"group/inner", "a/z"}},
		{Name: "Keep", Members: []string{"group/auto-z", "b/x"}},
	} {
		if err := SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	shown := func() (out []string) {
		for _, g := range Groups() {
			if !g.Hidden {
				out = append(out, g.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	all := []string{"auto-x", "auto-y", "auto-z", "inner", "keep", "outer"}
	if got := shown(); !slices.Equal(got, all) {
		t.Fatalf("before: %v", got)
	}

	for _, tc := range []struct {
		ids []string
		err string
	}{
		{[]string{"auto-x", "nothing"}, `no group "nothing"`},
		{[]string{"auto-x", "inner"}, "inner is in Outer"},
		{[]string{"auto-z", "auto-y"}, "auto-z is in Keep"},
	} {
		if err := DeleteGroups(tc.ids); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%v: %v, want …%s", tc.ids, err, tc.err)
		}
		if got := shown(); !slices.Equal(got, all) {
			t.Fatalf("%v refused, yet %v are left", tc.ids, got)
		}
	}

	// inner first, though Outer holds it: Outer goes with it
	if err := DeleteGroups([]string{"inner", "auto-x", "outer", "auto-y", "inner"}); err != nil {
		t.Fatal(err)
	}
	if got := shown(); !slices.Equal(got, []string{"auto-z", "keep"}) {
		t.Fatalf("after: %v", got)
	}
	if got := RemovedGroups(); !slices.Equal(got, []string{"auto-x", "auto-y"}) {
		t.Fatalf("removed found groups: %v", got)
	}
}

// Removing every group magpie found says the user wants none: a model a
// provider serves later brings no new one (xwang_sh on X), and found groups
// on again brings the removed ones back. Removing only some leaves them on.
func TestDeletingEveryFoundGroupKeepsThemOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	add := func(id string, models ...string) {
		t.Helper()
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	shown := func() (out []string) {
		for _, g := range Groups() {
			if !g.Hidden {
				out = append(out, g.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	add("a", "x", "y")
	add("b", "x", "y")
	if err := DeleteGroups([]string{"auto-x"}); err != nil {
		t.Fatal(err)
	}
	if !AutoGroupsOn() || !slices.Equal(shown(), []string{"auto-y"}) {
		t.Fatalf("some removed: on %v, shown %v", AutoGroupsOn(), shown())
	}
	if err := DeleteGroups([]string{"auto-y"}); err != nil {
		t.Fatal(err)
	}
	add("a", "x", "y", "z")
	add("b", "x", "y", "z")
	if AutoGroupsOn() || len(shown()) != 0 {
		t.Fatalf("all removed, then a new model: on %v, shown %v", AutoGroupsOn(), shown())
	}
	if err := SetAutoGroups(true); err != nil {
		t.Fatal(err)
	}
	if got := shown(); !slices.Equal(got, []string{"auto-z"}) {
		t.Fatalf("on again: %v (the removed stay removed)", got)
	}
}
