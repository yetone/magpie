package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A model's price is set from the provider editor's Names, prices & levels, with
// its Save, as `magpie model price` sets it (ITea312 on #819: only the
// price rate was in the window); OwnPrice gives it its list price back.
// The agents aren't told: a price isn't what they pick a model by.
func TestSetModelPrefsPrice(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	want := catalog.Price{Input: 0.5, Output: 2, CacheRead: 0.05, CacheWrite: 0}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Price: &want}}); err != nil {
		t.Fatal(err)
	}
	if got, ok := EffectivePrice("a", "sol"); !ok || !got.Same(want) {
		t.Fatalf("a/sol costs %+v (%v), want %+v", got, ok, want)
	}
	if _, ok := settings.Load().ModelPrices["b/sol"]; ok {
		t.Fatal("b's sol was priced too")
	}
	if touched != 0 {
		t.Fatalf("agents told %d times of a price", touched)
	}
	// a price no vendor could charge is refused
	bad := catalog.Price{Input: -1}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Price: &bad}}); err == nil {
		t.Fatal("a negative price was taken")
	}
	// Restore default: its list price again
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {OwnPrice: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelPrices["a/sol"]; ok {
		t.Fatal("the price set is still there")
	}
}
