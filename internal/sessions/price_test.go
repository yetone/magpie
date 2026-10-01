package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A session's Grok model named at an effort is priced as its model; one no
// catalog lists (Grok Build's grok-4.7-build, grok-4.7-mini, a fast one)
// stays unpriced (#224).
func TestPriceOfGrokEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, m := range []string{"grok-4.7", "grok-4.7-low", "grok-4.7-xhigh", "x-ai/grok-4.7-high"} {
		if pr, ok := priceOf(settings.Load(), m); !ok || pr.Input != 2 || pr.Output != 6 || pr.CacheRead != 0.5 {
			t.Errorf("priceOf(%q) = %+v, %v; want $2/$6/$0.5", m, pr, ok)
		}
	}
	for _, m := range []string{"grok-4.7-build", "grok-4.7-mini", "grok-4.7-low-fast", "grok-4.7-build-fast"} {
		if pr, ok := priceOf(settings.Load(), m); ok {
			t.Errorf("priceOf(%q) = %+v; want unpriced", m, pr)
		}
	}
}

// A session naming a model through a provider is priced at what the user set
// for that provider and model; the same model named bare is still its maker's
// list price, which is a different question from what one provider charges.
func TestPriceOfStatedProviderPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// a models.dev provider, because a model's maker is looked up among the
	// presets: a made-up vendor would have no maker to fall back to, which is
	// half of what this is checking
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{
	    "gpt-5.5":{"id":"gpt-5.5","cost":{"input":2,"output":10,"cache_read":0.25,"cache_write":2.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/gpt-5.5": {Input: new(float64(0.2)), Output: new(float64(1)),
			CacheRead: new(float64(0.05)), CacheWrite: new(float64(0.25))},
	}}); err != nil {
		t.Fatal(err)
	}

	pr, ok := priceOf(settings.Load(), "relay/gpt-5.5")
	if !ok || pr.Input != 0.2 || pr.Output != 1 || pr.CacheRead != 0.05 || pr.CacheWrite != 0.25 {
		t.Errorf("priceOf(%q) = %+v, %v; want the stated $0.2/$1", "relay/gpt-5.5", pr, ok)
	}

	// bare, it is still what models.dev lists for the model's own maker
	if pr, ok := priceOf(settings.Load(), "gpt-5.5"); !ok || pr.Input != 2 {
		t.Errorf("priceOf(%q) = %+v, %v; want the maker's $2", "gpt-5.5", pr, ok)
	}

	// and the same model through a provider nobody priced is its maker's too
	if pr, ok := priceOf(settings.Load(), "other/gpt-5.5"); !ok || pr.Input != 2 {
		t.Errorf("priceOf(%q) = %+v, %v; want the maker's $2", "other/gpt-5.5", pr, ok)
	}
}

// A session recorded before a provider was renamed still gets the configured
// price. The record names the id the provider had then; the price is keyed by
// the one it has now, and the lookup has to resolve the old id to get there —
// otherwise an old session quietly falls back to a list price and reports a
// different number from every session since the rename.
func TestPriceOfAfterProviderRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{
	    "gpt-5.5":{"id":"gpt-5.5","cost":{"input":2,"output":10}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	if err := provider.Save(provider.Provider{ID: "old", Name: "Old", Key: "k",
		Chat: "https://old.example/v1", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"new/gpt-5.5": {Input: new(float64(0.2)), Output: new(float64(1)),
			CacheRead: new(float64(0)), CacheWrite: new(float64(0))},
	}}); err != nil {
		t.Fatal(err)
	}

	pr, ok := priceOf(settings.Load(), "old/gpt-5.5")
	if !ok || pr.Input != 0.2 {
		t.Fatalf("a session recorded under the old provider id: %+v %v; want the stated $0.2", pr, ok)
	}
	if pr, ok := priceOf(settings.Load(), "new/gpt-5.5"); !ok || pr.Input != 0.2 {
		t.Fatalf("under the new id: %+v %v; want the stated $0.2", pr, ok)
	}
}

// A listing reads the settings once and prices every model of it against
// that one copy. It used to read the file for each model instead, which is
// a read and a parse per row of a list that names the same handful of
// models hundreds of times over. One read for the whole listing also means
// one snapshot: a price written while the listing is being put together
// shows up in the next listing, not halfway through this one.
func TestListingPricesEveryModelAtTheSettingsItReadOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	// the default price rather than a swapped one, and models no catalog
	// lists, so what the user stated is the only thing that can price them
	PriceOf = priceOf
	t.Cleanup(func() { PriceOf = priceOf })
	state := func(model string, in float64) {
		t.Helper()
		s := settings.Load()
		if s.ModelPrices == nil {
			s.ModelPrices = map[string]settings.ModelPrice{}
		}
		s.ModelPrices["relay/"+model] = settings.ModelPrice{
			Input: new(in), Output: new(in * 10), CacheRead: new(in / 10), CacheWrite: new(in),
		}
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	state("widget-a", 0.2)

	listing := pricer()
	if p := listing("relay/widget-a"); p == nil || p.Input != 0.2 {
		t.Fatalf("the model is priced at %v; want the $0.2 stated", p)
	}
	// written after the listing began, so not in the copy it read
	state("widget-b", 3)
	if p := listing("relay/widget-b"); p != nil {
		t.Errorf("a model the listing has not asked for yet is priced at %v; want it unpriced until the next listing reads the file again", p)
	}
	// the price it did read still answers with it, and the next listing
	// sees the one written since
	if p := listing("relay/widget-a"); p == nil || p.Input != 0.2 {
		t.Errorf("the listing now prices the first model at %v; want the $0.2 it read", p)
	}
	if p := pricer()("relay/widget-b"); p == nil || p.Input != 3 {
		t.Errorf("the next listing prices the second at %v; want the $3 just written", p)
	}
}
