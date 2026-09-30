package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// priceHome keeps a test off the machine's own settings, providers and
// catalogue, and forgets what an earlier test cached.
func priceHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// priceCatalog writes a models.dev cache in which one model is priced.
func priceCatalog(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"openai":{"models":{"sol":{"id":"sol","name":"Sol",` +
		`"cost":{"input":2,"output":10,"cache_read":0.25,"cache_write":2.5}}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
}

func statePrice(v float64) settings.ModelPrice {
	return settings.ModelPrice{Input: new(v), Output: new(v), CacheRead: new(v), CacheWrite: new(v)}
}

func TestEffectivePrice(t *testing.T) {
	priceHome(t)
	priceCatalog(t)

	// models.dev knows what the model costs its own vendor
	if pr, ok := EffectivePrice("openai", "sol"); !ok || pr.Input != 2 || pr.Output != 10 {
		t.Fatalf("list price: %+v %v", pr, ok)
	}

	// the user says what a relay charges, and that is what a call costs
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/model-2": statePrice(0.5),
	}}); err != nil {
		t.Fatal(err)
	}
	pr, ok := EffectivePrice("relay", "model-2")
	if !ok || pr.Input != 0.5 || pr.Output != 0.5 || pr.CacheRead != 0.5 || pr.CacheWrite != 0.5 {
		t.Fatalf("stated price: %+v %v", pr, ok)
	}

	// and models.dev's own price is left exactly where it was
	if pr, ok := EffectivePrice("openai", "sol"); !ok || pr.Input != 2 {
		t.Fatalf("list price changed: %+v %v", pr, ok)
	}

	// a model served at no cost has a price of zero, which is not the same
	// as having no price: the caller must be able to bill it
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/free": statePrice(0),
	}}); err != nil {
		t.Fatal(err)
	}
	if pr, ok := EffectivePrice("relay", "free"); !ok || pr.Input != 0 {
		t.Fatalf("a free model has a price of zero: %+v %v", pr, ok)
	}

	// one price for every model of a provider
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/*": statePrice(1.5),
	}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"anything", "model-2"} {
		if pr, ok := EffectivePrice("relay", m); !ok || pr.Input != 1.5 {
			t.Fatalf("%s: %+v %v", m, pr, ok)
		}
	}

	// but not for another provider serving the same model
	if pr, _ := EffectivePrice("other", "model-2"); pr.Input == 1.5 {
		t.Fatalf("another provider took the first one's price: %+v", pr)
	}

	// a model's own price wins over its provider's
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/*":         statePrice(1.5),
		"relay/cheap":     statePrice(0.25),
		"relay/expensive": statePrice(9),
	}}); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePrice("relay", "cheap"); pr.Input != 0.25 {
		t.Fatalf("the model's own price lost to the provider's: %+v", pr)
	}
	if pr, _ := EffectivePrice("relay", "expensive"); pr.Input != 9 {
		t.Fatalf("the model's own price lost to the provider's: %+v", pr)
	}
	if pr, _ := EffectivePrice("relay", "neither"); pr.Input != 1.5 {
		t.Fatalf("the provider's price lost to nothing: %+v", pr)
	}

	// a price no vendor could charge is refused where it is set
	if err := SetModelPrice("relay/bad", &catalog.Price{Input: -1, Output: 1}); err == nil {
		t.Fatal("a negative price was set")
	}
	if _, bad := settings.Load().ModelPrices["relay/bad"].Price(); bad == "" {
		t.Fatal("a price that was refused was written anyway")
	}
}

// A price that got into the file anyway — a hand edit, a merge — is skipped
// rather than billing the parts it does name at zero. Setting one is refused,
// so the file is written past Save here; that is the only way this state is
// reachable, and it is reachable.
func TestEffectivePriceSkipsAHandWrittenBadPrice(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	if err := os.MkdirAll(filepath.Dir(settings.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"modelPrices":{"openai/sol":{"input":1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, bad := settings.Load().ModelPrices["openai/sol"].Price(); bad == "" {
		t.Fatal("a half-given price was taken for a usable one")
	}
	pr, ok := EffectivePrice("openai", "sol")
	if !ok || pr.Input != 2 || pr.Output != 10 {
		t.Fatalf("a broken price was used: %+v %v; want the catalogue's", pr, ok)
	}
}

// A provider's own catalogue price wins over its maker's. The two are
// distinguished here by giving two presets different prices for one model and
// pointing the provider at the second: with the provider's catalogue
// consulted first it gets that one, where the maker fallback — the first
// preset that lists the model — would have got the first.
func TestEffectivePricePrefersTheProvidersOwnPrice(t *testing.T) {
	priceHome(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "anthropic":{"id":"anthropic","models":{"sol":{"id":"sol","cost":{"input":7,"output":70}}}},
	  "openai":{"id":"openai","models":{"sol":{"id":"sol","cost":{"input":2,"output":10}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// no catalogue of its own: it is priced as the first preset's model is
	if err := Save(Provider{ID: "nocat", Name: "No Cat", Key: "k", Chat: "https://nocat.example/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if pr, ok := EffectivePrice("nocat", "sol"); !ok || pr.Input != 7 {
		t.Fatalf("without a catalogue: %+v %v; want the first preset's $7", pr, ok)
	}

	// its own catalogue is consulted before any maker's
	if err := Save(Provider{ID: "withcat", Name: "With Cat", Key: "k", Chat: "https://withcat.example/v1",
		Models: []string{"sol"}, Catalog: "openai"}); err != nil {
		t.Fatal(err)
	}
	if pr, ok := EffectivePrice("withcat", "sol"); !ok || pr.Input != 2 {
		t.Fatalf("with a catalogue: %+v %v; want its own $2, not the maker's $7", pr, ok)
	}
}

// A price is keyed by the id a provider has now, not by what it is called.
// Find answers a display name as well, and a call counted at a provider
// under its display name — a record of one that has since been deleted, a
// name a hand edit left in a file — was counted at the price of whichever
// provider that name belongs to, which is a different provider's.
func TestEffectivePriceIsNotKeyedByDisplayName(t *testing.T) {
	priceHome(t)
	priceCatalog(t) // models.dev lists openai's sol at $2 an M in, $10 out

	for _, p := range []Provider{
		// x is Alpha's id, and also the name the second provider goes by
		{ID: "x", Name: "Alpha", Key: "k", Chat: "https://alpha.example/v1", Models: []string{"sol"}},
		{ID: "relay", Name: "x", Key: "k", Chat: "https://relay.example/v1", Models: []string{"sol"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for key, in := range map[string]float64{"x/sol": 7, "relay/sol": 0.5} {
		if err := SetModelPrice(key, &catalog.Price{Input: in, Output: in * 10, CacheRead: in / 10, CacheWrite: in}); err != nil {
			t.Fatal(key, err)
		}
	}

	// asked by the id, a model is counted at that provider's own price, and
	// not at the price of the provider that goes by the same name
	if pr, ok := EffectivePrice("x", "sol"); !ok || pr.Input != 7 || pr.Output != 70 {
		t.Fatalf("x: %+v %v; want Alpha's own $7/$70, not the $0.50 of the provider called x", pr, ok)
	}
	// a display name is not an id, in either spelling: what such a record
	// costs is left where such calls were always counted, at what the
	// model costs elsewhere
	for _, name := range []string{"Alpha", "alpha"} {
		if pr, ok := EffectivePrice(name, "sol"); !ok || pr.Input != 2 || pr.Output != 10 {
			t.Errorf("%q: %+v %v; want models.dev's $2/$10, not a provider's own price", name, pr, ok)
		}
	}
	// and the provider called x is still priced under the id it has
	if pr, ok := EffectivePrice("relay", "sol"); !ok || pr.Input != 0.5 {
		t.Fatalf("relay: %+v %v; want its own $0.50", pr, ok)
	}
}

// A price written before a provider was renamed is read under the id it
// has now, and an id it was renamed from still reaches it — even where a
// second provider goes by that name, which is the one Find would answer.
func TestEffectivePriceResolvesAnIDARenamedFrom(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	for _, p := range []Provider{
		// Yankee is the name this one goes by
		{ID: "relay", Name: "Yankee", Key: "k", Chat: "https://relay.example/v1", Models: []string{"sol"}},
		// and yankee the id the second had before it was renamed
		{ID: "yankee", Name: "Yankee Relay", Key: "k", Chat: "https://yankee.example/v1", Models: []string{"sol"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for key, in := range map[string]float64{"yankee/sol": 3, "relay/sol": 0.5} {
		if err := SetModelPrice(key, &catalog.Price{Input: in, Output: in * 10, CacheRead: in / 10, CacheWrite: in}); err != nil {
			t.Fatal(key, err)
		}
	}
	if err := Rename("yankee", "renamed"); err != nil {
		t.Fatal(err)
	}

	// the id it has now, and the one it was renamed from, are its own
	for _, id := range []string{"renamed", "yankee"} {
		if pr, ok := EffectivePrice(id, "sol"); !ok || pr.Input != 3 || pr.Output != 30 {
			t.Errorf("%s: %+v %v; want the $3/$30 it was priced at, not the $0.50 of the provider called Yankee", id, pr, ok)
		}
	}
	if pr, ok := EffectivePrice("relay", "sol"); !ok || pr.Input != 0.5 {
		t.Fatalf("relay: %+v %v; want its own $0.50", pr, ok)
	}
}

// What a call costs is not what an agent picks a model by, and the model
// lists magpie keeps in the agents' own files are not rewritten over a
// number in a cost report. Unlike a name, a window or a reply limit, a
// price is not told to anybody; the counter that hears a name below is one
// that could have heard it.
func TestSetModelPriceTellsNoAgent(t *testing.T) {
	priceHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Key: "k", Chat: "https://relay.example/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err) // saving a provider does tell them, so it is done first
	}
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })

	if err := SetModelPrice("relay/sol", &catalog.Price{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 0.2}); err != nil {
		t.Fatal(err)
	}
	if told != 0 {
		t.Errorf("a price told the agents %d times; their files keep the model lists they had", told)
	}
	// taking it away is as quiet as setting it
	if err := SetModelPrice("relay/sol", nil); err != nil {
		t.Fatal(err)
	}
	if told != 0 {
		t.Errorf("taking a price away told the agents %d times", told)
	}
	// a name is told, as every other thing agents see is
	if err := SetModelName("relay/sol", "My Sol"); err != nil {
		t.Fatal(err)
	}
	if told == 0 {
		t.Error("a name did not tell the agents either, so nothing above shows a price stays quiet")
	}
}

// A price for a model the provider does not serve never applies, and nothing
// says so afterwards: the entry is simply there, and the model is counted at
// the catalogue's price as if the user had never said anything. Refused at the
// point of writing, the way `magpie model name` refuses the same id. A removal
// is exempt, so an entry left for a model that has since gone can be cleared.
func TestSetModelPriceRefusesAModelTheProviderDoesNotServe(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Key: "k", Chat: "https://relay.example/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	whole := catalog.Price{Input: 1, Output: 2}
	err := SetModelPrice("relay/typo", &whole)
	if err == nil {
		t.Fatal("a price for a model the provider does not serve was accepted")
	}
	if !strings.Contains(err.Error(), "has no model typo") {
		t.Errorf("error %q should name the model it does not have", err)
	}
	if len(settings.Load().ModelPrices) != 0 {
		t.Error("the refused price was written anyway")
	}
	// the one it does serve is fine, and so is a provider-wide price
	if err := SetModelPrice("relay/sol", &whole); err != nil {
		t.Errorf("a price for a model it does serve: %v", err)
	}
	if err := SetModelPrice("relay/*", &whole); err != nil {
		t.Errorf("a provider-wide price: %v", err)
	}
	// a removal is not held to it, so a stale entry can be cleared
	if err := SetModelPrice("relay/typo", nil); err != nil {
		t.Errorf("removing a price for a model it does not serve: %v", err)
	}
}

// A price is written under the id the provider has now, the way a name is.
// Keyed by a display name it would be a price the provider is never asked
// for: EffectivePrice resolves the caller to an id first, so the entry would
// sit in the file looking set and the model would still be counted at the
// catalogue's price. Nothing else would say so.
func TestSetModelPriceKeysByIdNotByTheNameUsedToAsk(t *testing.T) {
	priceHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay A", Key: "k", Chat: "https://relay.example/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err) // saving a provider does tell them, so it is done first
	}
	pr := catalog.Price{Input: 7, Output: 70, CacheRead: 0.7, CacheWrite: 7}
	if err := SetModelPrice("Relay A/sol", &pr); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	if _, under := s.ModelPrices["Relay A/sol"]; under {
		t.Errorf("the price is keyed by the display name: %v", s.ModelPrices)
	}
	got, ok := s.ModelPrices["relay/sol"]
	if !ok {
		t.Fatalf("the price is not keyed by the provider's id either: %v", s.ModelPrices)
	}
	if p, bad := got.Price(); bad != "" || p != pr {
		t.Errorf("the price stored under the id reads back as %+v (bad part %q), want %+v", p, bad, pr)
	}
	// and it is the price the model is counted at, which was the point
	if eff, ok := EffectivePrice("relay", "sol"); !ok || eff != pr {
		t.Errorf("the model is counted at %+v, %v; want the price just set %+v", eff, ok, pr)
	}
	// taking it away by either name clears the one entry
	if err := SetModelPrice("Relay A/sol", nil); err != nil {
		t.Fatal(err)
	}
	if len(settings.Load().ModelPrices) != 0 {
		t.Errorf("taking a price away left %v", settings.Load().ModelPrices)
	}
}

// The key a price is kept at outlives the provider it was set for, and
// nothing rewrites it when that provider goes, so the name it went by can be
// given to another provider. Asking Find for one — which answers a display
// name — then cleared that second provider's key, where nothing was stored:
// nothing changed, the caller was told it had, and the price the user meant
// stayed in the file to be counted at.
func TestDropModelPriceTakesTheKeyItIsStoredAt(t *testing.T) {
	priceHome(t)
	if err := Save(Provider{ID: "b", Name: "Beta", Key: "k", Chat: "https://beta.example/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelPrice("b/vendor/m", &catalog.Price{Input: 0.5, Output: 1.5, CacheRead: 0.05, CacheWrite: 0.1}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("b"); err != nil {
		t.Fatal(err)
	}
	// a provider of another id now goes by the dead one's id as its display
	// name, which is what Find answers "b" with
	if err := Save(Provider{ID: "c", Name: "b", Key: "k", Chat: "https://c.example/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}

	dropped, err := DropModelPrice("b/vendor/m")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Error("nothing was reported taken away, though a price was there under that key")
	}
	if left := settings.Load().ModelPrices; len(left) != 0 {
		t.Errorf("the price is still counted at what the user paid: %v", left)
	}
	// and a key nothing is stored under says there was none, which is what
	// tells a mistyped id from a price that was cleared
	dropped, err = DropModelPrice("b/vendor/m")
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Error("taking the same price away twice reported a second removal")
	}
}
