package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// The provider editor's Names & levels are made with its Save, all at
// once: each is made as SetModelName, SetModelEfforts and SetModelImage
// make it, and the agents' files are rewritten once, not once a change
// (ARNO on Discord: every reasoning level ticked was applied on its own,
// so picking them lagged).
func TestSetModelPrefs(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	name, yes := "My Sol", true
	efforts := []string{"low", "high"}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name, Efforts: &efforts, Images: &yes}}); err != nil {
		t.Fatal(err)
	}
	if touched != 1 {
		t.Fatalf("agents told %d times, want once", touched)
	}
	s := settings.Load()
	if s.ModelNames["a/sol"] != "My Sol" || !slices.Equal(s.ModelEfforts["a/sol"], efforts) || !s.ModelImages["a/sol"] {
		t.Fatalf("names %v, efforts %v, images %v", s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
	if _, ok := s.ModelNames["b/sol"]; ok {
		t.Fatal("b's sol was named too")
	}
	// nothing changed: nothing to tell
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name}}); err != nil || touched != 1 {
		t.Fatalf("%v, told %d times", err, touched)
	}
	// Restore default: its own name, every level, the vendor's answer
	own, all := "", []string{}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &own, Efforts: &all, OwnImages: true}}); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	if len(s.ModelNames)+len(s.ModelEfforts)+len(s.ModelImages) != 0 || touched != 2 {
		t.Fatalf("names %v, efforts %v, images %v, told %d times", s.ModelNames, s.ModelEfforts, s.ModelImages, touched)
	}
	// a level it hasn't is refused, the change before it still told
	bad := []string{"xhigh"}
	err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name, Efforts: &bad}})
	if err == nil || !strings.Contains(err.Error(), "xhigh") {
		t.Fatalf("got %v", err)
	}
	if settings.Load().ModelNames["a/sol"] != "My Sol" || touched != 3 {
		t.Fatalf("names %v, told %d times", settings.Load().ModelNames, touched)
	}
}
