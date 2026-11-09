package provider

import (
	"slices"
	"testing"
)

// #1783 (cybershape): a group can keep its models out of the lists agents
// and the gateway's /v1/models show, so a client lists the group alone.
// The models it names, fixed at an effort too, and those its patterns
// match leave the lists; a group in it keeps its own; the models stay
// served (a request naming one, the group itself) and are kept for the
// GUI as the unlisted provider's are. Switched off, the group keeps
// nothing out.
func TestGroupUnlistsItsModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, p := range []Provider{
		{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}},
		{ID: "b", Name: "b", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"n1", "n2"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{Name: "Inner", Members: []string{"a/m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "G", Members: []string{"a/m1:high", "group/inner"}, Match: []string{"b/*"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	ids := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		slices.Sort(out)
		return out
	}
	if got, want := ids(Catalog()), []string{"a/m2", "a/m3", "group/g", "group/inner"}; !slices.Equal(got, want) {
		t.Fatalf("Catalog %v, want %v", got, want)
	}
	if shown, _ := CatalogFor("opencode"); !slices.Equal(ids(shown), []string{"a/m2", "a/m3", "group/g", "group/inner"}) {
		t.Fatalf("an agent's list: %v", ids(shown))
	}
	if got, want := ids(Unlisted()), []string{"a/m1", "b/n1", "b/n2"}; !slices.Equal(got, want) {
		t.Fatalf("Unlisted %v, want %v", got, want)
	}
	if got := ids(Served()); !slices.Contains(got, "a/m1") || !slices.Contains(got, "b/n2") {
		t.Fatalf("served: %v", got)
	}
	if _, ms, ok := FindGroup("group/g"); !ok || len(ms) != 4 {
		t.Fatalf("the group's members: %v %v", ms, ok)
	}
	if g, _, _ := FindGroup("group/g"); !g.Unlists {
		t.Fatal("Unlists not kept by the save")
	}

	// switched off, its models are listed again
	if err := SwitchGroup("g", false); err != nil {
		t.Fatal(err)
	}
	if got := ids(Catalog()); !slices.Contains(got, "a/m1") || !slices.Contains(got, "b/n1") {
		t.Fatalf("with the group off: %v", got)
	}
	if err := SwitchGroup("g", true); err != nil {
		t.Fatal(err)
	}
	// listed again by the user
	g, _, _ := FindGroup("group/g")
	g.Unlists = false
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := ids(Catalog()); len(got) != 7 || len(Unlisted()) != 0 {
		t.Fatalf("listed again: %v, unlisted %v", got, ids(Unlisted()))
	}
}

// CatalogGroups, which the Providers page reads on every poll, is
// Catalog's groups and Catalog's length, with a group listing alone and
// a provider kept unlisted, and with neither.
func TestCatalogGroupsIsCatalogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, p := range []Provider{
		{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}},
		{ID: "b", Name: "b", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"n1", "n2"}},
		{ID: "c", Name: "c", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"o1"}, Unlisted: true},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	same := func(when string) {
		t.Helper()
		cat := Catalog()
		groups, n := CatalogGroups()
		var want []string
		for _, e := range cat {
			if e.Group != "" {
				want = append(want, e.ID)
			}
		}
		var got []string
		for _, e := range groups {
			got = append(got, e.ID)
		}
		if n != len(cat) || !slices.Equal(got, want) {
			t.Errorf("%s: CatalogGroups %v, %d; Catalog's groups %v of %d", when, got, n, want, len(cat))
		}
	}
	same("no groups")
	if err := SaveGroup(Group{Name: "Inner", Members: []string{"a/m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "G", Members: []string{"a/m1:high", "group/inner"}, Match: []string{"b/*"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	same("a group listing alone")
	if err := SwitchGroup("g", false); err != nil {
		t.Fatal(err)
	}
	same("that group off")
}
