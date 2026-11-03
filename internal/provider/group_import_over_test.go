package provider

import (
	"encoding/json"
	"slices"
	"testing"
)

// issennknight on Discord: pasting a shared group whose id is a group of
// the user's updates that group instead of adding another. Only that group
// changes; what the text can't carry (accounts' folder rules, the order,
// the switch) stays; an id the user has no group of adds one as before.
func TestImportGroupOverReplacesSameID(t *testing.T) {
	shareHome(t, Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y", "z"}})
	for _, g := range []Group{
		{ID: "one", Name: "One", Members: []string{"a/x"}, Rules: []Rule{{Use: "a/x", Folders: []string{"~/mine"}}}},
		{ID: "two", Name: "Two", Members: []string{"a/z"}},
		{ID: "three", Name: "Three", Members: []string{"a/z"}},
	} {
		if err := SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := SwitchGroup("one", false); err != nil {
		t.Fatal(err)
	}
	text, _ := json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{
		ID: "one", Name: "Three", Members: []string{"a/y", "a/x"}, Routing: Rotate}})
	before := len(Groups())
	res, err := ImportGroupOver(string(text))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced || res.ID != "one" || len(Groups()) != before {
		t.Fatalf("replaced=%v id=%q groups %d -> %d", res.Replaced, res.ID, before, len(Groups()))
	}
	g := Groups()[slices.IndexFunc(Groups(), func(g Group) bool { return g.ID == "one" })]
	if !slices.Equal(g.Members, []string{"a/y", "a/x"}) || g.Routing != Rotate {
		t.Fatalf("not replaced: %+v", g)
	}
	if g.Name != "Three 2" {
		t.Errorf("a name another group has: %q", g.Name)
	}
	if !g.Disabled || len(g.Rules) != 1 || !g.Rules[0].ByFolder() {
		t.Errorf("the switch or the folder rule did not stay: %+v", g)
	}
	if two := sharedAt(t, "two"); !slices.Equal(two.Members, []string{"a/z"}) || two.Name != "Two" {
		t.Errorf("another group changed: %+v", two)
	}
	i, j := slices.IndexFunc(Groups(), func(g Group) bool { return g.ID == "one" }), slices.IndexFunc(Groups(), func(g Group) bool { return g.ID == "two" })
	if i > j {
		t.Errorf("it moved in the list: %d %d", i, j)
	}

	// no group of that id: added, as ImportGroup does
	text, _ = json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{
		ID: "elsewhere", Name: "Elsewhere", Members: []string{"a/y"}}})
	res, err = ImportGroupOver(string(text))
	if err != nil || res.Replaced || len(Groups()) != before+1 {
		t.Fatalf("added: %+v %v groups %d", res, err, len(Groups()))
	}

	// without asking to replace, the same id adds another group
	text, _ = json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{
		ID: "one", Name: "One", Members: []string{"a/y"}}})
	res, err = ImportGroup(string(text))
	if err != nil || res.Replaced || res.ID == "one" {
		t.Fatalf("plain import: %+v %v", res, err)
	}
}
