package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A Grok id named at an effort is priced as its model; a fast, mini or
// build one no catalog lists stays unpriced (#224).
func TestGrokEffortListPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}},
	    "grok-4.6":{"id":"grok-4.6","cost":{"input":2,"output":6,"cache_read":0.5}}}},
	  "openai":{"id":"openai","models":{
	    "gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":10,"output":50}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	grok := Provider{ID: "grok", Name: "Grok (SuperGrok)"}
	for _, id := range []string{"grok-4.7", "grok-4.7-low", "grok-4.7-medium", "grok-4.7-high", "grok-4.7-xhigh",
		"GROK-4.7-High", "x-ai/grok-4.7-xhigh", "grok-4.6-minimal"} {
		for name, price := range map[string]func(string) (catalog.Price, bool){"MakerPrice": MakerPrice, "ListPrice": grok.ListPrice} {
			pr, ok := price(id)
			if !ok || pr.Input != 2 || pr.Output != 6 || pr.CacheRead != 0.5 {
				t.Errorf("%s(%q) = %+v, %v; want grok's $2/$6/$0.5", name, id, pr, ok)
			}
		}
	}
	for _, id := range []string{"grok-4.7-fast", "grok-4.7-low-fast", "grok-4.7-xhigh-fast", "grok-4.7-build",
		"grok-4.7-build-fast", "grok-4.7-mini", "grok-4.7-mini-high", "grok-4.7-max",
		// only Grok's ids are read this way
		"gpt-6-astra-high", "cursor-grok-4.7-high"} {
		if pr, ok := grok.ListPrice(id); ok {
			t.Errorf("ListPrice(%q) = %+v; want unpriced", id, pr)
		}
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
	if got := PricedName("grok-4.7-xhigh"); got != "grok-4.7" {
		t.Errorf("PricedName(grok-4.7-xhigh) = %q", got)
	}
	if got := PricedName("grok-4.7-low-fast"); got != "grok-4.7-low-fast" {
		t.Errorf("PricedName(grok-4.7-low-fast) = %q", got)
	}
}
