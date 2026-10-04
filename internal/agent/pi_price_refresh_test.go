package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

type piPriceEntry struct {
	ID   string `json:"id"`
	Cost *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cacheRead"`
		CacheWrite float64 `json:"cacheWrite"`
	} `json:"cost"`
}

func piPriceEntries(t *testing.T, home string) map[string]piPriceEntry {
	t.Helper()
	var file struct {
		Providers map[string]struct {
			Models []piPriceEntry `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(readFile(filepath.Join(home, ".pi", "agent", "models.json"))), &file); err != nil {
		t.Fatal(err)
	}
	out := map[string]piPriceEntry{}
	for _, m := range file.Providers["magpie"].Models {
		out[m.ID] = m
	}
	return out
}

// The same catalog-priced model can cost a different amount through each
// relay. A price change and its reset must reach Pi's saved model without
// reconnecting, including zero rather than mistaking it for an unknown price.
func TestPiCostsFollowProviderTariffs(t *testing.T) {
	home := syncHome(t)
	writeFile(t, catalog.CachePath(), `{"anthropic":{"models":{"claude-sonnet-5":{"id":"claude-sonnet-5","cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}}}}}`)
	catalog.Reset()
	for _, p := range []provider.Provider{
		{ID: "direct", Name: "Direct", Key: "k", Anthropic: "https://api.anthropic.com", Models: []string{"claude-sonnet-5", "unknown"}},
		{ID: "relay-anth", Name: "Relay Anth", Key: "k", Anthropic: "https://relay.example", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := pi(home).Field("model").Set("magpie/direct/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })
	cost := func(id string) float64 {
		t.Helper()
		m := piPriceEntries(t, home)[id]
		if m.Cost == nil {
			t.Fatalf("%s has no price for Pi to calculate a cost", id)
		}
		// One million tokens of each tier, as Pi's calculateCost counts them.
		return m.Cost.Input + m.Cost.Output + m.Cost.CacheRead + m.Cost.CacheWrite
	}
	if got := cost("direct/claude-sonnet-5"); got != 22.05 {
		t.Errorf("list-priced call costs %g", got)
	}
	if err := provider.SetModelPrice("relay-anth/*", &catalog.Price{Input: 1, Output: 2, CacheRead: 0.25, CacheWrite: 1.5}); err != nil {
		t.Fatal(err)
	}
	if got := cost("relay-anth/claude-sonnet-5"); got != 4.75 {
		t.Errorf("relay tariff costs %g", got)
	}
	if got := cost("direct/claude-sonnet-5"); got != 22.05 {
		t.Errorf("another provider's price changed to %g", got)
	}
	if err := provider.SetModelPrice("relay-anth/claude-sonnet-5", &catalog.Price{}); err != nil {
		t.Fatal(err)
	}
	if got := cost("relay-anth/claude-sonnet-5"); got != 0 {
		t.Errorf("explicitly free call costs %g", got)
	}
	if err := provider.SetModelPrice("relay-anth/claude-sonnet-5", nil); err != nil {
		t.Fatal(err)
	}
	if got := cost("relay-anth/claude-sonnet-5"); got != 4.75 {
		t.Errorf("reset did not restore provider tariff: %g", got)
	}
	if err := provider.SetModelPrice("relay-anth/*", nil); err != nil {
		t.Fatal(err)
	}
	if got := cost("relay-anth/claude-sonnet-5"); got != 22.05 {
		t.Errorf("reset did not restore catalog price: %g", got)
	}
	if m := piPriceEntries(t, home)["direct/unknown"]; m.Cost != nil {
		t.Errorf("unknown price declared free: %+v", m.Cost)
	}
}

// A group has no single tariff when its active members disagree. A manual
// pick or an Off member changes the models it can actually route to.
func TestPiGroupCostsUseActiveMembers(t *testing.T) {
	home := syncHome(t)
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "https://" + id + ".example/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetModelPrice(id+"/m", &catalog.Price{Input: 1, Output: 2, CacheRead: 0.25, CacheWrite: 1.5}); err != nil {
			t.Fatal(err)
		}
	}
	g := provider.Group{ID: "priced", Name: "Priced", Members: []string{"a/m", "b/m"}}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if err := pi(home).Field("model").Set("magpie/group/priced"); err != nil {
		t.Fatal(err)
	}
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })
	if m := piPriceEntries(t, home)["group/priced"]; m.Cost == nil || m.Cost.Input != 1 {
		t.Fatalf("common tariff unavailable: %+v", m)
	}
	if err := provider.SetModelPrice("b/m", &catalog.Price{Input: 5, Output: 10, CacheRead: 1, CacheWrite: 6}); err != nil {
		t.Fatal(err)
	}
	if m := piPriceEntries(t, home)["group/priced"]; m.Cost != nil {
		t.Errorf("mixed group priced as one member: %+v", m.Cost)
	}
	g.Off = []string{"b/m"}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if m := piPriceEntries(t, home)["group/priced"]; m.Cost == nil || m.Cost.Input != 1 {
		t.Errorf("Off member still changes price: %+v", m)
	}
	g.Off, g.Routing, g.Pick = nil, provider.Manual, "b/m"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if m := piPriceEntries(t, home)["group/priced"]; m.Cost == nil || m.Cost.Input != 5 {
		t.Errorf("manual pick priced as another member: %+v", m)
	}
}

// A model-wide price applies to every provider that has no tariff of its own.
// Resetting it must restore each provider's preceding effective price.
func TestPiCostsFollowAnyProviderTariff(t *testing.T) {
	home := syncHome(t)
	writeFile(t, catalog.CachePath(), `{"anthropic":{"models":{"claude-sonnet-5":{"id":"claude-sonnet-5","cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}}}}}`)
	catalog.Reset()
	for _, id := range []string{"direct", "relay"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Anthropic: "https://" + id + ".example", Models: []string{"claude-sonnet-5"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pi(home).Field("model").Set("magpie/direct/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })
	cost := func(id string) float64 {
		t.Helper()
		m := piPriceEntries(t, home)[id]
		if m.Cost == nil {
			t.Fatalf("%s has no price for Pi to calculate a cost", id)
		}
		return m.Cost.Input + m.Cost.Output + m.Cost.CacheRead + m.Cost.CacheWrite
	}
	if err := provider.SetModelPrice("direct/*", &catalog.Price{Input: 9, Output: 9, CacheRead: 9, CacheWrite: 9}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelPrice("*/claude-sonnet-5", &catalog.Price{Input: 4, Output: 4, CacheRead: 4, CacheWrite: 4}); err != nil {
		t.Fatal(err)
	}
	if got := cost("relay/claude-sonnet-5"); got != 16 {
		t.Errorf("any-provider tariff costs %g", got)
	}
	if got := cost("direct/claude-sonnet-5"); got != 36 {
		t.Errorf("provider tariff lost precedence: %g", got)
	}
	if dropped, err := provider.DropModelPrice("*/claude-sonnet-5"); err != nil || !dropped {
		t.Fatalf("DropModelPrice = %v, %v", dropped, err)
	}
	if got := cost("relay/claude-sonnet-5"); got != 22.05 {
		t.Errorf("reset did not restore catalog price: %g", got)
	}
	if got := cost("direct/claude-sonnet-5"); got != 36 {
		t.Errorf("reset changed another provider's tariff: %g", got)
	}
}
