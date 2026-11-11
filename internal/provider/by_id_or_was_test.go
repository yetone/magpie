package provider

import (
	"fmt"
	"path/filepath"
	"testing"
)

// A model's price looks its provider up by id (byIDOrWas) and copied every
// provider to read one: each Sessions poll prices every model of every
// call, ~10 MB per 40 polls on a real history (#2278). It copies only the
// one it found, and that one is still the caller's to change.
func TestByIDOrWasCopiesOnlyItsOwn(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for i := range 40 {
		p := Provider{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("P %d", i), Chat: fmt.Sprintf("https://p%d.example/v1", i), Key: "k"}
		for j := range 5 {
			p.Keys = append(p.Keys, KeyAccount{Name: fmt.Sprintf("K%d", j), Key: fmt.Sprintf("k%d-%d", i, j)})
		}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// a Sessions poll holds the providers: built once, read by each price
	defer Hold()()
	p, ok := byIDOrWas("p7")
	if !ok || p.ID != "p7" || len(p.Keys) != 5 {
		t.Fatalf("p7: %t %+v", ok, p.Keys)
	}
	p.Keys[0].Key = "changed"
	if again, _ := byIDOrWas("p7"); again.Keys[0].Key == "changed" {
		t.Fatal("a caller's change reached the shared providers")
	}
	all := testing.AllocsPerRun(10, func() { All() })
	one := testing.AllocsPerRun(10, func() { byIDOrWas("p7") })
	if one > all/10 {
		t.Errorf("byIDOrWas: %.0f allocations, against %.0f for All", one, all)
	}
}
