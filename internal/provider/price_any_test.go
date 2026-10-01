package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// dottedCatalog is a models.dev cache with Anthropic's models spelled as
// Anthropic spells them, 4-6, and Gemini 3 Pro's preview gone from Google's.
func dottedCatalog(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"anthropic":{"models":{
	    "claude-opus-4-6":{"id":"claude-opus-4-6","cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}},
	    "claude-haiku-4-5":{"id":"claude-haiku-4-5","cost":{"input":1,"output":5,"cache_read":0.1,"cache_write":1.25}}}},
	  "google":{"models":{"gemini-3-pro-image":{"id":"gemini-3-pro-image","cost":{"input":2,"output":120}}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
}

// A call whose provider has gone, or never had a models.dev id, is priced at
// its maker's list price even when its id spells the version with a dot as
// Copilot does (claude-opus-4.6) and Anthropic's entry with a dash (tony on
// Discord: copilot/claude 都 no known price).
func TestDottedIDPricedAtItsMakers(t *testing.T) {
	priceHome(t)
	dottedCatalog(t)
	for _, c := range []struct {
		pid, model string
		in         float64
	}{
		{"gone", "claude-opus-4.6", 5},
		{"gone", "claude-haiku-4.5", 1},
		{"anthropic", "claude-opus-4.6", 5},
		{"gone", "claude-opus-4-6", 5},
	} {
		if pr, ok := EffectivePrice(c.pid, c.model); !ok || pr.Input != c.in {
			t.Errorf("EffectivePrice(%q, %q) = %+v, %v; want $%v in", c.pid, c.model, pr, ok, c.in)
		}
	}
	// a model models.dev no longer lists stays unpriced: nothing is made up
	if pr, ok := EffectivePrice("gone", "gemini-3-pro-preview"); ok {
		t.Errorf("gemini-3-pro-preview priced at %+v; models.dev lists no price for it", pr)
	}
}

// A price the user gives a model from any provider (*/model) prices it from a
// provider that is gone, or that no longer lists it, and loses to the price
// they gave that provider; it is kept lower-cased and taken away again.
func TestAnyProviderPrice(t *testing.T) {
	priceHome(t)
	dottedCatalog(t)

	if err := SetModelPrice("*/Gemini-3-Pro-Preview", &catalog.Price{Input: 2, Output: 12, CacheRead: 0.2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelPrices["*/gemini-3-pro-preview"]; !ok {
		t.Fatalf("not kept under */gemini-3-pro-preview: %v", settings.Load().ModelPrices)
	}
	for _, pid := range []string{"gone", "copilot", "anthropic"} {
		if pr, ok := EffectivePrice(pid, "gemini-3-pro-preview"); !ok || pr.Input != 2 || pr.Output != 12 {
			t.Errorf("EffectivePrice(%q) = %+v, %v; want the user's $2/$12", pid, pr, ok)
		}
	}
	// a provider's own price of the user's still wins
	s := settings.Load()
	s.ModelPrices["relay/gemini-3-pro-preview"] = statePrice(0.5)
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePrice("relay", "gemini-3-pro-preview"); pr.Input != 0.5 {
		t.Errorf("the provider's price lost to the any-provider one: %+v", pr)
	}
	// and an any-provider price beats a list price: it is the user's word
	if err := SetModelPrice("*/claude-opus-4.6", &catalog.Price{Input: 3, Output: 15}); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePrice("anthropic", "claude-opus-4.6"); pr.Input != 3 {
		t.Errorf("the list price beat the user's: %+v", pr)
	}

	if dropped, err := DropModelPrice("*/gemini-3-pro-preview"); err != nil || !dropped {
		t.Fatalf("DropModelPrice = %v, %v", dropped, err)
	}
	if pr, ok := EffectivePrice("gone", "gemini-3-pro-preview"); ok {
		t.Errorf("still priced after the reset: %+v", pr)
	}
	if err := SetModelPrice("*/*", &catalog.Price{Input: 1}); err == nil {
		t.Error("a price for every model of every provider was taken")
	}
}
