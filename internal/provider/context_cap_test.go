package provider

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// One longest context for every model (WJaunita45604 on X): a window above
// it is told as the cap, a shorter or unknown one as it was, and one the
// user typed for the model or for all the provider's models is theirs.
func TestContextCapHoldsEveryModelButTheOnesTheUserSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	catalog.Changed = nil
	long := catalog.Model{ID: "long", Context: 1_000_000}
	short := catalog.Model{ID: "short", Context: 128_000}
	none := catalog.Model{ID: "no-window-anywhere-xyz"}
	plain := Provider{ID: "a"}

	win := func(p Provider, m catalog.Model) int { return p.WindowOf(m) }
	if got := win(plain, long); got != 1_000_000 {
		t.Fatalf("no cap: %d", got)
	}
	if err := SetContextCap(200_000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		p    Provider
		m    catalog.Model
		want int
	}{
		{"longer comes down", plain, long, 200_000},
		{"shorter stays", plain, short, 128_000},
		{"unknown stays unknown", plain, none, 0},
		{"the model's own wins", Provider{ID: "a", Contexts: map[string]int{"long": 500_000}}, long, 500_000},
		{"its provider's own wins", Provider{ID: "a", Contexts: map[string]int{"*": 400_000}}, long, 400_000},
		{"another model of that provider is capped", Provider{ID: "a", Contexts: map[string]int{"other": 500_000}}, long, 200_000},
	} {
		if got := win(tc.p, tc.m); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
		// the catalog's entry says what the page does
		if got := entryFor(tc.p, tc.m, settings.Load()).Context; got != tc.want {
			t.Errorf("%s: entry %d, want %d", tc.name, got, tc.want)
		}
	}
	if ListedWindow(long) != 1_000_000 {
		t.Errorf("the vendor's own window changed: %d", ListedWindow(long))
	}
	if err := SetContextCap(-1); err == nil {
		t.Error("took a negative cap")
	}
	if err := SetContextCap(0); err != nil {
		t.Fatal(err)
	}
	if got := win(plain, long); got != 1_000_000 {
		t.Fatalf("cap off: %d", got)
	}
}
