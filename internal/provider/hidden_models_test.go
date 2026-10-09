package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

func ids(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

// Models taken out of one agent's lists one by one: gone from its catalog,
// every other agent's untouched, a new model shown, and the agents told.
func TestHiddenModels(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"relay", "other"} {
		if err := Save(Provider{ID: id, Name: "My " + id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
			t.Fatal(err)
		}
	}
	told := 0
	was := catalog.Changed
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = was })

	all, _ := CatalogFor("codex")
	if err := SetHiddenModels("Codex", []string{" relay/m2 ", "other/m1", "relay/m2", ""}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().HiddenModels["codex"]; !slices.Equal(got, []string{"other/m1", "relay/m2"}) {
		t.Fatalf("saved %v", got)
	}
	shown, hidden := CatalogFor("codex")
	if slices.Contains(ids(shown), "relay/m2") || slices.Contains(ids(shown), "other/m1") ||
		len(shown) != len(all)-2 || !slices.Equal(slices.Sorted(slices.Values(ids(hidden))), []string{"other/m1", "relay/m2"}) {
		t.Fatalf("shown %v hidden %v", ids(shown), ids(hidden))
	}
	// what it may pick among is still every one
	if listed, _ := ListedFor("codex"); len(listed) != len(all) {
		t.Fatalf("listed %v", ids(listed))
	}
	if other, _ := CatalogFor("claude"); len(other) != len(all) {
		t.Fatalf("another agent's list narrowed: %v", ids(other))
	}
	if told != 1 {
		t.Fatalf("agents told %d times", told)
	}
	// the same again changes nothing and tells no one
	if err := SetHiddenModels("codex", []string{"relay/m2", "other/m1"}); err != nil || told != 1 {
		t.Fatalf("%v told %d", err, told)
	}
	// a model new to a provider is shown
	if err := Save(Provider{ID: "relay", Name: "My relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if shown, _ := CatalogFor("codex"); !slices.Contains(ids(shown), "relay/m3") {
		t.Fatalf("new model not shown: %v", ids(shown))
	}
	// a renamed provider's hidden models follow it
	if err := Rename("relay", "fast"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().HiddenModels["codex"]; !slices.Contains(got, "fast/m2") || slices.Contains(got, "relay/m2") {
		t.Fatalf("after rename %v", got)
	}
	// none puts every one back
	if err := SetHiddenModels("codex", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().HiddenModels["codex"]; ok {
		t.Fatal("empty list kept")
	}
	if _, hidden := CatalogFor("codex"); len(hidden) != 0 {
		t.Fatalf("still hidden %v", ids(hidden))
	}
}

// An agent shown only the models picked for it (nianlee-official, #1337):
// switched on, the models it is shown stay as they were; a model that comes
// later, of a provider it has or a new one, is not shown until it is
// picked; every other agent is shown it; a pick of a model gone from its
// provider's list for a while is kept; switched off, the ones not picked
// are hidden and new ones show again.
func TestOnlyPickedModels(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	save := func(id string, models ...string) {
		t.Helper()
		if err := Save(Provider{ID: id, Name: "My " + id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	// no model of one provider's is another's, so no group of them is made
	save("relay", "a1", "a2")
	save("other", "b1", "b2")
	told := 0
	was := catalog.Changed
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = was })
	shownIDs := func(agent string) []string {
		shown, _ := CatalogFor(agent)
		return slices.Sorted(slices.Values(ids(shown)))
	}

	if err := SetHiddenModels("codex", []string{"relay/a2"}); err != nil {
		t.Fatal(err)
	}
	before := shownIDs("codex")
	if err := SetOnlyPicked("Codex", true); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	if got := s.PickedModels["codex"]; !slices.Equal(got, []string{"other/b1", "other/b2", "relay/a1"}) {
		t.Fatalf("picked %v", got)
	}
	if _, ok := s.HiddenModels["codex"]; ok {
		t.Fatalf("hidden kept beside the picks: %v", s.HiddenModels)
	}
	if got := shownIDs("codex"); !slices.Equal(got, before) {
		t.Fatalf("switched on, shown %v, was %v", got, before)
	}
	if told != 2 {
		t.Fatalf("agents told %d times", told)
	}
	// on again changes nothing
	if err := SetOnlyPicked("codex", true); err != nil || told != 2 {
		t.Fatalf("%v told %d", err, told)
	}

	// a new model of a provider it has, and a new provider's: not shown to
	// it, shown to another agent
	save("relay", "a1", "a2", "a3")
	save("fresh", "x1")
	if got := shownIDs("codex"); !slices.Equal(got, before) {
		t.Fatalf("new models shown: %v", got)
	}
	if _, hidden := CatalogFor("codex"); !slices.Contains(ids(hidden), "relay/a3") || !slices.Contains(ids(hidden), "fresh/x1") {
		t.Fatalf("hidden %v", ids(hidden))
	}
	if other := shownIDs("claude"); !slices.Contains(other, "relay/a3") || !slices.Contains(other, "fresh/x1") {
		t.Fatalf("another agent's list narrowed: %v", other)
	}
	// what it may pick among has them
	if listed, _ := ListedFor("codex"); !slices.Contains(ids(listed), "fresh/x1") {
		t.Fatalf("listed %v", ids(listed))
	}

	// picked, one shows
	if err := SetPickedModels("codex", []string{"other/b1", "other/b2", "relay/a1", "fresh/x1"}); err != nil {
		t.Fatal(err)
	}
	if got := shownIDs("codex"); !slices.Equal(got, []string{"fresh/x1", "other/b1", "other/b2", "relay/a1"}) {
		t.Fatalf("after a pick, shown %v", got)
	}

	// other/b2 gone from its provider's list a while: picks sent meanwhile
	// don't name it, and it is still picked when it is back
	save("other", "b1")
	if err := SetPickedModels("codex", []string{"other/b1", "relay/a1"}); err != nil {
		t.Fatal(err)
	}
	save("other", "b1", "b2")
	if got := shownIDs("codex"); !slices.Equal(got, []string{"other/b1", "other/b2", "relay/a1"}) {
		t.Fatalf("back, shown %v", got)
	}

	// a renamed provider's picks follow it
	if err := Rename("relay", "fast"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().PickedModels["codex"]; !slices.Contains(got, "fast/a1") || slices.Contains(got, "relay/a1") {
		t.Fatalf("after rename %v", got)
	}

	// none picked: none shown, and still only the picks
	if err := SetPickedModels("codex", nil); err != nil {
		t.Fatal(err)
	}
	if got := shownIDs("codex"); len(got) != 0 {
		t.Fatalf("none picked, shown %v", got)
	}
	if _, only := PickedModels("codex"); !only {
		t.Fatal("none picked read as every model shown")
	}
	if err := SetPickedModels("codex", []string{"fast/a1"}); err != nil {
		t.Fatal(err)
	}

	// off: the models shown stay, the rest are hidden, and a new one shows
	if err := SetOnlyPicked("codex", false); err != nil {
		t.Fatal(err)
	}
	if got := shownIDs("codex"); !slices.Equal(got, []string{"fast/a1"}) {
		t.Fatalf("switched off, shown %v", got)
	}
	if _, ok := settings.Load().PickedModels["codex"]; ok {
		t.Fatal("picks kept after switching off")
	}
	save("fresh", "x1", "x2")
	if got := shownIDs("codex"); !slices.Contains(got, "fresh/x2") {
		t.Fatalf("switched off, a new model not shown: %v", got)
	}
	// and picks can't be set for an agent shown new models
	if err := SetPickedModels("codex", []string{"fast/a1"}); err == nil {
		t.Fatal("picks set for an agent shown every model")
	}
}
