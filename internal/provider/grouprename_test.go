package provider

import (
	"slices"
	"testing"
)

// A group can take another id: a found one drops its auto- prefix and
// stays removed under the old id, and the groups that have it in them
// name it by the new one.
func TestRenameGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-astra", "x"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{Name: "Top", Members: []string{"a/x", "group/auto-gpt-6-astra"},
		Rules: []Rule{{Use: "group/auto-gpt-6-astra", Tokens: 10}}}); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("auto-gpt-6-astra", "top"); err == nil {
		t.Fatal("renamed onto another group")
	}
	if err := RenameGroup("auto-gpt-6-astra", "Not A Slug"); err == nil {
		t.Fatal("renamed to a bad id")
	}
	if err := RenameGroup("auto-gpt-6-astra", "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	// the old id asks the renamed group, never the found one back beside it
	if g, _, ok := FindGroup("group/auto-gpt-6-astra"); !ok || g.ID != "gpt-6-astra" {
		t.Fatalf("the old id found %v %q", ok, g.ID)
	}
	g, ms, ok := FindGroup("group/gpt-6-astra")
	if !ok || g.Auto || len(ms) != 2 {
		t.Fatalf("%v %+v %d", ok, g, len(ms))
	}
	top, _, _ := FindGroup("group/top")
	if !slices.Contains(top.Members, "group/gpt-6-astra") || top.Rules[0].Use != "group/gpt-6-astra" {
		t.Fatalf("%+v", top)
	}
	if !slices.Contains(RemovedGroups(), "auto-gpt-6-astra") {
		t.Fatal(RemovedGroups())
	}
	// a user's group renamed leaves nothing behind
	if err := RenameGroup("top", "best"); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("group/top"); !ok || g.ID != "best" || slices.Contains(RemovedGroups(), "top") {
		t.Fatalf("top is still there, or doesn't lead to best: %v %q", ok, g.ID)
	}
	if _, _, ok := FindGroup("group/best"); !ok {
		t.Fatal("no best")
	}
	if err := RenameGroup("nothing", "else"); err == nil {
		t.Fatal("renamed a group that isn't")
	}
}

// A renamed group keeps answering to its old id (xinvxueyuan on Discord),
// through more renames, until a group takes that id for its own; renamed
// back, it holds the id again and lists it no more as former.
func TestRenamedGroupKeepsItsOldID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "work", Name: "Work", Members: []string{"a/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("work", "job"); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("job", "day"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"work", "job", "day"} {
		if g, ms, ok := FindGroup("group/" + id); !ok || g.ID != "day" || len(ms) != 1 {
			t.Fatalf("group/%s: %v %q %d", id, ok, g.ID, len(ms))
		}
	}
	// saved again by a caller that doesn't send Former, it keeps them
	g, _, _ := FindGroup("group/day")
	g.Former = nil
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("group/work"); !ok || g.ID != "day" {
		t.Fatalf("a save dropped the old ids: %v %q", ok, g.ID)
	}
	// a new group takes "work": it is that group's from then on, also after
	// the new one is gone
	if err := SaveGroup(Group{ID: "work", Name: "Work", Members: []string{"a/y"}}); err != nil {
		t.Fatal(err)
	}
	if g, ms, ok := FindGroup("group/work"); !ok || g.ID != "work" || ms[0].Model != "y" {
		t.Fatalf("group/work: %v %q", ok, g.ID)
	}
	if g, _, _ := FindGroup("group/day"); slices.Contains(g.Former, "work") {
		t.Fatalf("day still lists work: %v", g.Former)
	}
	if err := DeleteGroup("work"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/work"); ok {
		t.Fatal("work came back to day after it was given away")
	}
	// renamed back to an old id, it is its own id, not a former one
	if err := RenameGroup("day", "job"); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup("group/job")
	if g.ID != "job" || slices.Contains(g.Former, "job") || !slices.Contains(g.Former, "day") {
		t.Fatalf("%q %v", g.ID, g.Former)
	}
	// a rename onto a former id of another group takes it from that group
	if err := SaveGroup(Group{ID: "other", Name: "Other", Members: []string{"a/y"}}); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("other", "day"); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("group/day"); g.ID != "day" {
		t.Fatalf("group/day: %q", g.ID)
	}
	if g, _, _ := FindGroup("group/job"); slices.Contains(g.Former, "day") {
		t.Fatalf("job still lists day: %v", g.Former)
	}
}
