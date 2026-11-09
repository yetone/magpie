package provider

import (
	"fmt"
	"testing"
	"time"
)

// Each profile's in-use check on a state poll asks for the renames; with
// providers.json unchanged they are worked out once, not parsed from the
// whole file again, and a rename after still shows at once.
func TestUsageRenamesParsedOnceAChange(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for i := range 60 {
		if err := Save(Provider{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("P %d", i), Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a", "b", "c"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Rename("p1", "one"); err != nil {
		t.Fatal(err)
	}
	if got := UsageRenames().Of("p1", time.Now()); got != "one" {
		t.Fatalf("p1 went to %q", got)
	}
	if n := testing.AllocsPerRun(20, func() { UsageRenames() }); n > 10 {
		t.Errorf("renames of an unchanged file: %.0f allocations, want a few", n)
	}
	if err := Rename("p2", "two"); err != nil {
		t.Fatal(err)
	}
	if got := UsageRenames().Of("p2", time.Now()); got != "two" {
		t.Errorf("a rename since: p2 went to %q", got)
	}
}
