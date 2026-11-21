package usage

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// mu33410 on X: on ChatGPT Pro the Codex week's whole read about $600 with
// Fast and $1200 without. A window's whole is the routed calls' cost over
// the share they used, so it holds only while each call is weighed at the
// rate it draws the plan down. OpenAI's Codex pricing page gives the
// plan's included usage its own multipliers, apart from the API's and
// credits': GPT-6.2 Ultrafast 6x (3x in the API through 2027-02-28),
// GPT-6.1 and GPT-6 Fast 2.5x (2x in the API), GPT-6.2 Fast 2x. A week of
// the same calls in any tier, each drawing the plan down at that page's
// rate, reads the same whole as a week of them at the Standard tier. The
// catalog is models.dev's bytes for the models, as the live cache has them.
func TestCodexWholeWindowIsTheSameInEveryTier(t *testing.T) {
	holdsHome(t)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{`+
		`"gpt-6.2-sol":{"id":"gpt-6.2-sol","cost":{"input":2,"output":10,"cache_read":0.1,"cache_write":2.5,"tiers":[{"input":4,"output":15,"cache_read":0.2,"cache_write":5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":4,"output":15,"cache_read":0.2,"cache_write":5}},"experimental":{"modes":{"fast":{"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"provider":{"body":{"service_tier":"ultrafast"}}}}}},`+
		`"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":2,"output":10,"cache_read":0.1,"cache_write":2.5,"tiers":[{"input":4,"output":15,"cache_read":0.2,"cache_write":5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":4,"output":15,"cache_read":0.2,"cache_write":5}},"experimental":{"modes":{"fast":{"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"cost":{"input":12,"output":60,"cache_read":0.6,"cache_write":15},"provider":{"body":{"service_tier":"ultrafast"}}}}}},`+
		`"gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":10,"output":50,"cache_read":1,"cache_write":12.5,"tiers":[{"input":20,"output":75,"cache_read":2,"cache_write":25,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":20,"output":75,"cache_read":2,"cache_write":25}},"experimental":{"modes":{"fast":{"cost":{"input":20,"output":100,"cache_read":2,"cache_write":25},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"cost":{"input":60,"output":300,"cache_read":6,"cache_write":75},"provider":{"body":{"service_tier":"ultrafast"}}}}}}`+
		`}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * 24 * time.Hour)
	const user = "dee@example.com"
	week := func(used float64) []provider.SubscriptionQuota {
		return []provider.SubscriptionQuota{{Provider: "codex", User: user, Windows: []provider.QuotaWindow{
			{Name: "7 days", Used: used, ResetsAt: &reset, Span: 7 * 24 * time.Hour}}}}
	}
	call := func(model, tier string, in, out, cached int) Record {
		return Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: "codex", ProviderAccount: user, Status: 200,
			Model: model, TierAsked: tier, TierSent: tier, Input: in, Output: out, CacheRead: cached}
	}
	whole := func(used float64, r Record) float64 {
		t.Helper()
		got := windowHolds(week(used), now, now.Add(-30*24*time.Hour), apiPricer(), provider.Renames{}, nil, nil, func(fn func(Record)) { fn(r) })
		h := got[0].Windows[0].Holds
		if h == nil || !h.Priced {
			t.Fatalf("%s at %q: no priced whole (%+v, why %q)", r.Model, r.TierSent, h, got[0].Windows[0].HoldsWhy)
		}
		return h.Cost
	}
	for _, c := range []struct {
		model, tier string
		plan        float64 // how much faster the tier draws the plan down
	}{
		{"gpt-6.2-sol", "ultrafast", 6},
		{"gpt-6.2-sol", "priority", 2},
		{"gpt-6.1-sol", "priority", 2.5},
		{"gpt-6.1-sol", "ultrafast", 8},
		{"gpt-6-astra", "priority", 2.5},
		{"gpt-6-astra", "ultrafast", 8},
		{"gpt-6-astra", "fast", 2.5}, // Codex's own word for Fast
	} {
		in, out, cached := 40_000, 2_000, 100_000
		std := whole(10, call(c.model, "", in, out, cached))
		if got := whole(10*c.plan, call(c.model, c.tier, in, out, cached)); !near(got, std) {
			t.Errorf("%s at %s: the week holds $%.2f, at Standard $%.2f; the tier draws the plan %vx and must not move the whole", c.model, c.tier, got, std, c.plan)
		}
	}

	// the ledger and another vendor's window still price a call at its API
	// rate: GPT-6.2 Ultrafast is 3x in the API until March
	r := call("gpt-6.2-sol", "ultrafast", 40_000, 2_000, 100_000)
	std := r
	std.TierAsked, std.TierSent = "", ""
	if pr, sp := Pricer()(r), Pricer()(std); pr == nil || sp == nil || !near(r.CostAt(*pr), 3*std.CostAt(*sp)) {
		t.Errorf("the ledger's Ultrafast price moved: %+v vs standard %+v", pr, sp)
	}
	other := r
	other.Provider = "openai"
	qs := []provider.SubscriptionQuota{{Provider: "openai", User: user, Windows: week(10)[0].Windows}}
	got := windowHolds(qs, now, now.Add(-30*24*time.Hour), apiPricer(), provider.Renames{}, nil, nil, func(fn func(Record)) { fn(other) })
	if h := got[0].Windows[0].Holds; h == nil || !near(h.Cost, 10*3*std.CostAt(*Pricer()(std))) {
		t.Errorf("a card not a ChatGPT plan's weighed Ultrafast otherwise than its API rate: %+v", h)
	}
}
