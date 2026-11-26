package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A model one provider alone serves can be a found group too, when the
// user asks for it (realLemonawa on X: auto/<model> only came with a
// second provider). Off by default. On, it is a group of that one
// provider, a bare id is it, and a group of two keeps its name when a
// provider stops serving the model (#5799). The groups the user made or
// changed and the records of those removed stay as they are, on and off.
// Off, a request still naming a group of one goes to its model, and a
// group of the user's that has one in it holds it on until taken out.
func TestAutoGroupsOfOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, p := range []struct {
		id     string
		models []string
	}{{"a", []string{"kimi-k3", "solo", "x", "two", "gone"}}, {"b", []string{"kimi-k3", "x", "two"}}} {
		if err := Save(Provider{ID: p.id, Name: p.id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: p.models}); err != nil {
			t.Fatal(err)
		}
	}
	// a found group changed is the user's; one removed stays removed
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: []string{"b/x", "a/x"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("auto-kimi-k3"); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"a/solo", "b/x"}}); err != nil {
		t.Fatal(err)
	}
	stored := func() string {
		t.Helper()
		f, err := read()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(f.Groups)
		return string(b)
	}
	theirs := stored()
	shown := func() map[string][]string {
		out := map[string][]string{}
		for _, g := range Groups() {
			if !g.Hidden {
				out[g.ID] = g.Members
			}
		}
		return out
	}
	if AutoGroupsOfOneOn() {
		t.Fatal("groups of one are on before anyone turned them on")
	}
	if got := shown(); got["auto-solo"] != nil || !slices.Equal(got["auto-two"], []string{"a/two", "b/two"}) {
		t.Fatalf("off by default: %v", got)
	}

	if err := SetAutoGroupsOfOne(true); err != nil {
		t.Fatal(err)
	}
	got := shown()
	if !AutoGroupsOfOneOn() || !slices.Equal(got["auto-solo"], []string{"a/solo"}) || !slices.Equal(got["auto-gone"], []string{"a/gone"}) {
		t.Fatalf("on: %v", got)
	}
	if !slices.Equal(got["auto-x"], []string{"b/x", "a/x"}) || !slices.Equal(got["mine"], []string{"a/solo", "b/x"}) || got["auto-kimi-k3"] != nil {
		t.Fatalf("on touched the user's groups: %v", got)
	}
	if s := stored(); s != theirs {
		t.Fatalf("on rewrote the stored groups:\n%s\nwas\n%s", s, theirs)
	}
	if id, ok := GroupFor("solo"); !ok || id != "group/auto-solo" {
		t.Fatalf("solo: %q %v", id, ok)
	}
	if _, ms, ok := FindGroup("group/auto-solo"); !ok || len(ms) != 1 || ms[0].Provider.ID != "a" || ms[0].Model != "solo" {
		t.Fatalf("group/auto-solo: %v %+v", ok, ms)
	}
	if !slices.ContainsFunc(Catalog(), func(e Entry) bool { return e.ID == "auto/solo" && e.Group != "" }) {
		t.Fatal("auto/solo is not in the catalog")
	}
	// b stops serving two: its group stays, of a alone (#5799)
	if err := Save(Provider{ID: "b", Name: "b", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kimi-k3", "x"}}); err != nil {
		t.Fatal(err)
	}
	if got := shown(); !slices.Equal(got["auto-two"], []string{"a/two"}) {
		t.Fatalf("two lost a provider: %v", got)
	}
	// a provider kept out of found groups is in no group of one either
	if err := SetAutoGroupSkip([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got["auto-solo"] != nil || got["auto-two"] != nil {
		t.Fatalf("a kept out: %v", got)
	}
	if err := SetAutoGroupSkip(nil); err != nil {
		t.Fatal(err)
	}
	// one removed while on stays removed, off and on again
	if err := DeleteGroup("auto-gone"); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got["auto-gone"] != nil {
		t.Fatalf("auto-gone removed: %v", got)
	}
	theirs = stored()

	// a group of the user's has a group of one in it: off waits for it
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"a/solo", "group/auto-solo"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoGroupsOfOne(false); err == nil || !strings.Contains(err.Error(), "auto-solo is in Mine") {
		t.Fatalf("off with auto-solo in Mine: %v", err)
	}
	if !AutoGroupsOfOneOn() {
		t.Fatal("turned off while the user's group has one in it")
	}
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"a/solo", "b/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoGroupsOfOne(false); err != nil {
		t.Fatal(err)
	}
	got = shown()
	if AutoGroupsOfOneOn() || got["auto-solo"] != nil || got["auto-two"] != nil {
		t.Fatalf("off: %v", got)
	}
	if !slices.Equal(got["auto-x"], []string{"b/x", "a/x"}) || !slices.Equal(got["mine"], []string{"a/solo", "b/x"}) {
		t.Fatalf("off touched the user's groups: %v", got)
	}
	if s := stored(); s != theirs {
		t.Fatalf("off rewrote the stored groups:\n%s\nwas\n%s", s, theirs)
	}
	// a request still naming one goes to its model
	for in, want := range map[string]string{"group/auto-solo": "a/solo", "auto/two": "a/two"} {
		if got, ok := AutoStandIn(in); !ok || got != want {
			t.Fatalf("%s: %q %v, want %s", in, got, ok, want)
		}
	}
	if GoneAutoGroup("group/auto-solo") {
		t.Fatal("auto-solo is called gone while a serves solo")
	}
	if err := SetAutoGroupsOfOne(true); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got["auto-gone"] != nil || !slices.Equal(got["auto-solo"], []string{"a/solo"}) {
		t.Fatalf("on again: %v", got)
	}
	// found groups off: none of one either
	if err := SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got["auto-solo"] != nil {
		t.Fatalf("found groups off: %v", got)
	}
}
