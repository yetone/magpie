package provider

import (
	"slices"
	"strings"
	"testing"
)

// The groups magpie finds can be turned off all at once (蓝猫 on Discord),
// not only removed one by one: off, none is listed, served or picked for a
// bare model id; the user's own groups, a found one they changed among
// them, and the records of those removed stay; on again, every found group
// is back as it was.
func TestAutoGroupsOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kimi-k3", "glm-5.3", "x", "y"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"a/x", "b/y"}}); err != nil {
		t.Fatal(err)
	}
	// a found group changed is the user's
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: []string{"b/x", "a/x"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("auto-y"); err != nil {
		t.Fatal(err)
	}
	ids := func() (out []string) {
		for _, g := range Groups() {
			if !g.Hidden {
				out = append(out, g.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	catalog := func() (out []string) {
		for _, e := range Catalog() {
			if e.Group != "" {
				out = append(out, e.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	if !AutoGroupsOn() {
		t.Fatal("found groups are off before anyone turned them off")
	}
	on := []string{"auto-glm-5-3", "auto-kimi-k3", "auto-x", "mine"}
	if got := ids(); !slices.Equal(got, on) {
		t.Fatalf("on: %v", got)
	}

	if err := SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	if AutoGroupsOn() {
		t.Fatal("still on")
	}
	if got := ids(); !slices.Equal(got, []string{"auto-x", "mine"}) {
		t.Fatalf("off: %v", got)
	}
	if got := catalog(); !slices.Equal(got, []string{"group/auto-x", "group/mine"}) {
		t.Fatalf("off, the catalog's groups: %v", got)
	}
	if _, _, ok := FindGroup("group/auto-kimi-k3"); ok {
		t.Fatal("a found group is served while they are off")
	}
	if id, ok := GroupFor("kimi-k3"); ok {
		t.Fatalf("a bare model is the group %s while found groups are off", id)
	}
	// a bare model is one provider's then, as it is when no group has it
	if p, m, ok := Resolve("kimi-k3"); !ok || p.ID != "a" || m != "kimi-k3" {
		t.Fatalf("kimi-k3: %v %s %v", p.ID, m, ok)
	}
	if !slices.Contains(RemovedGroups(), "auto-y") {
		t.Fatalf("a removed group's record went: %v", RemovedGroups())
	}

	if err := SetAutoGroups(true); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, on) {
		t.Fatalf("on again: %v", got)
	}
	if g, _, ok := FindGroup("group/auto-x"); !ok || g.Auto || g.Routing != Ordered {
		t.Fatalf("the user's auto-x: %+v %v", g, ok)
	}
}

// While found groups are off, a request for one — an agent left on it, a
// session begun before — goes to its model from the first provider that
// serves it, however that one spells it; never one the user has of that id,
// and nothing while found groups are on.
func TestAutoStandIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, p := range []struct {
		id     string
		models []string
	}{{"a", []string{"claude-opus-5.5", "x"}}, {"b", []string{"claude-opus-5-5-20260801", "x"}}} {
		if err := Save(Provider{ID: p.id, Name: p.id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: p.models}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := AutoStandIn("group/auto-claude-opus-5-5"); ok {
		t.Fatal("a stand-in while found groups are on")
	}
	if err := SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"group/auto-claude-opus-5-5":     "a/claude-opus-5.5",
		"group/auto-claude-opus-5-5[1m]": "a/claude-opus-5.5",
		"group/auto-x":                   "a/x",
		"group/auto-nothing":             "",
		"group/mine":                     "",
		"auto-x":                         "",
		"a/claude-opus-5.5":              "",
	} {
		got, ok := AutoStandIn(in)
		if got != want || ok != (want != "") {
			t.Errorf("AutoStandIn(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: []string{"b/x"}}); err != nil {
		t.Fatal(err)
	}
	if got, ok := AutoStandIn("group/auto-x"); ok {
		t.Fatalf("the user's auto-x stood in for by %s", got)
	}
}

// A found group one of the user's has in it, or classifies with, keeps
// found groups on: turned off, the user's group would lose it unsaid.
func TestAutoGroupsOffHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "n"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{Name: "Top", Members: []string{"group/auto-m", "a/n"}}); err != nil {
		t.Fatal(err)
	}
	err := SetAutoGroups(false)
	if err == nil || !strings.Contains(err.Error(), "auto-m is in Top") {
		t.Fatalf("turned off under Top: %v", err)
	}
	if !AutoGroupsOn() {
		t.Fatal("off after all")
	}
	if err := SaveGroup(Group{ID: "top", Name: "Top", Members: []string{"a/m", "a/n"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
}
