package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// mythosCatalog lists Fable 5 and 5.2, and Mythos 5 at a price of its
// own (models.dev's azure lists it at Fable 5's; here it differs, to tell
// them apart); no catalog lists Mythos 5.2.
const mythosCatalog = `{
  "anthropic":{"id":"anthropic","models":{
    "claude-fable-5":{"id":"claude-fable-5","cost":{"input":10,"output":50,"cache_read":1,"cache_write":12.5}},
    "claude-fable-5-2":{"id":"claude-fable-5-2","cost":{"input":8,"output":40,"cache_read":0.2,"cache_write":10}},
    "claude-mythos-5":{"id":"claude-mythos-5","cost":{"input":11,"output":55,"cache_read":1.1,"cache_write":13}}}}}`

// A call to claude-mythos-5.2 showed no price in Usage → Requests
// (shangyuan0818 on X): no catalog lists it. Anthropic prices a Mythos as
// the Fable of its version, so it is priced at claude-fable-5-2's, however
// the id is written; a Mythos a catalog does list keeps its own price.
func TestMythosPricedAsItsFable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(mythosCatalog), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	claude := Provider{ID: "anthropic", Name: "Claude"}
	for _, c := range []struct {
		id      string
		in, out float64
	}{
		{"claude-mythos-5.2", 8, 40},
		{"claude-mythos-5-2", 8, 40},
		{"Claude-Mythos-5.2", 8, 40},
		{"claude-mythos-5-2[1m]", 8, 40},
		{"anthropic/claude-mythos-5.2", 8, 40},
		{"claude-mythos-5", 11, 55},
	} {
		if pr, ok := MakerPrice(c.id); !ok || pr.Input != c.in || pr.Output != c.out {
			t.Errorf("MakerPrice(%q) = %+v, %v; want $%v / $%v", c.id, pr, ok, c.in, c.out)
		}
		if pr, ok := claude.ListPrice(c.id); !ok || pr.Input != c.in || pr.Output != c.out {
			t.Errorf("ListPrice(%q) = %+v, %v; want $%v / $%v", c.id, pr, ok, c.in, c.out)
		}
	}
	// the row's "priced as" and Sessions' price name the Fable it took
	if got := PricedName("claude-mythos-5.2"); got != "claude-fable-5.2" {
		t.Errorf("PricedName(claude-mythos-5.2) = %q, want claude-fable-5.2", got)
	}
	if got := PricedName("claude-mythos-5"); got != "claude-mythos-5" {
		t.Errorf("PricedName(claude-mythos-5) = %q, want itself", got)
	}
	if _, ok := MakerPrice("claude-mythos-9"); ok {
		t.Error("claude-mythos-9 priced; no Fable 9 is listed")
	}
}
