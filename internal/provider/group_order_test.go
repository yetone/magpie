package provider

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// #779: the routing groups can be put in an order of the user's, a found
// one among them, and every list of them follows it: Groups (the Routing
// page, the CLI, the TUI) and the catalog, which /v1/models is. It is kept
// in providers.json; a group made after goes last, a renamed one keeps its
// place, and sync brings the other computer's order first.
func TestGroupOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "sk-" + id, Chat: "https://" + id + ".example.com/v1", Models: []string{"m", "x-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []Group{{ID: "one", Members: []string{"a/x-a"}}, {ID: "two", Members: []string{"b/x-b"}}} {
		if err := SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	found := AutoGroupID("m")
	groups := func() []string {
		var out []string
		for _, g := range Groups() {
			out = append(out, g.ID)
		}
		return out
	}
	// the groups at the head of the catalog, as /v1/models lists them
	catalogGroups := func() []string {
		var out []string
		for _, e := range Catalog() {
			if id, ok := strings.CutPrefix(e.ID, GroupPrefix); ok {
				out = append(out, id)
			}
		}
		return out
	}
	if got := groups(); !slices.Equal(got, []string{"one", "two", found}) {
		t.Fatalf("as made: %v", got)
	}
	if err := SetGroupOrder([]string{found, "two"}); err != nil {
		t.Fatal(err)
	}
	want := []string{found, "two", "one"}
	if got := groups(); !slices.Equal(got, want) {
		t.Fatalf("Groups after SetGroupOrder(%s, two): %v", found, got)
	}
	if got := catalogGroups(); !slices.Equal(got, want) {
		t.Fatalf("catalog after SetGroupOrder(%s, two): %v", found, got)
	}
	if e := Catalog(); len(e) == 0 || e[0].ID != GroupPrefix+found {
		t.Fatalf("the catalog's first model isn't %s", GroupPrefix+found)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		GroupOrder []string `json:"groupOrder"`
	}
	if err := json.Unmarshal(b, &f); err != nil || !slices.Equal(f.GroupOrder, want) {
		t.Fatalf("providers.json's groupOrder: %v (%v)", f.GroupOrder, err)
	}
	if err := SaveGroup(Group{ID: "three", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if got := groups(); !slices.Equal(got, []string{found, "two", "one", "three"}) {
		t.Fatalf("three made: %v", got)
	}
	if err := RenameGroup("two", "deux"); err != nil {
		t.Fatal(err)
	}
	if got := groups(); !slices.Equal(got, []string{found, "deux", "one", "three"}) {
		t.Fatalf("two renamed: %v", got)
	}
	for _, bad := range [][]string{{"one", "one"}, {"nope"}} {
		if err := SetGroupOrder(bad); err == nil {
			t.Fatalf("SetGroupOrder(%v) taken", bad)
		}
	}
	if err := MirrorGroupOrder([]string{"three", "one"}); err != nil {
		t.Fatal(err)
	}
	if got := groups(); !slices.Equal(got, []string{"three", "one", found, "deux"}) {
		t.Fatalf("mirrored (three, one): %v", got)
	}
}
