package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Archivemylife on X asked whether a Fast call is counted right. It was
// counted at the standard price: OpenAI bills Fast (service_tier
// "priority") at twice gpt-6.1-sol's standard and Ultrafast at six times,
// Anthropic Claude Opus 5.5's fast speed at twice. A call is now priced at
// the tier its reply named, else the one sent (the ChatGPT backend names
// none that tells), at the price models.dev lists under the model's
// modes, the same as OpenAI's and Anthropic's pricing pages. A price that
// isn't the maker's standard one (a relay's rate, a user's own) is left
// as it is, and so is a call OpenAI said it ran at the default tier.
// The catalog below is models.dev's own bytes for the two models.
func TestFastCallsPricedAtTheirTier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{`+
		`"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":2,"output":10,"cache_read":0.1,"cache_write":2.5,"tiers":[{"input":4,"output":15,"cache_read":0.2,"cache_write":5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":4,"output":15,"cache_read":0.2,"cache_write":5}},"experimental":{"modes":{"fast":{"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"provider":{"body":{"service_tier":"priority"}}},"flex":{"cost":{"input":1,"output":5,"cache_read":0.05,"cache_write":1.25},"provider":{"body":{"service_tier":"flex"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"cost":{"input":12,"output":60,"cache_read":0.6,"cache_write":15},"provider":{"body":{"service_tier":"ultrafast"}}}}}}}},`+
		`"anthropic":{"id":"anthropic","models":{`+
		`"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"experimental":{"modes":{"fast":{"cost":{"input":8,"output":40,"cache_read":0.4,"cache_write":10},"provider":{"body":{"speed":"fast"},"headers":{"anthropic-beta":"fast-mode-2026-02-01"}}}}}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-relay", Chat: "https://relay.example/v1", PriceRate: 0.8}); err != nil {
		t.Fatal(err)
	}
	in, out, cached := 40_000, 2_000, 100_000
	sol := (float64(in)*2 + float64(out)*10 + float64(cached)*0.1) / 1e6
	opus := (float64(in)*4 + float64(out)*20 + float64(cached)*0.2) / 1e6
	calls := []struct {
		name, prov, model, sent, served string
		want                            float64
	}{
		{"Codex Fast, the backend naming no tier", "codex", "gpt-6.1-sol", "priority", "", 2 * sol},
		{"Codex Ultrafast", "codex", "gpt-6.1-sol", "ultrafast", "", 6 * sol},
		{"Codex standard", "codex", "gpt-6.1-sol", "", "", sol},
		{"API Fast", "openai", "gpt-6.1-sol", "priority", "priority", 2 * sol},
		{"API Fast run at the default tier", "openai", "gpt-6.1-sol", "priority", "default", sol},
		{"API Flex", "openai", "gpt-6.1-sol", "flex", "flex", sol / 2},
		{"a relay's own rate", "relay", "gpt-6.1-sol", "priority", "", 0.8 * sol},
		{"Claude fast", "anthropic", "claude-opus-5-5", "fast", "", 2 * opus},
		{"Claude standard", "anthropic", "claude-opus-5-5", "", "", opus},
	}
	now := time.Now()
	for i, c := range calls {
		Append(Record{Time: now.Add(-time.Duration(len(calls)-i) * time.Minute), Agent: "codex", Provider: c.prov, Model: c.model,
			TierSent: c.sent, TierServed: c.served, Input: in, Output: out, CacheRead: cached, Status: 200})
	}
	rows, sum, _ := Ledger(Month, Filter{})
	if len(rows) != len(calls) {
		t.Fatalf("rows %d", len(rows))
	}
	var total float64
	cost, api := NewCoster(), apiPricer()
	for i, c := range calls {
		r := rows[len(calls)-1-i]
		if r.Model != c.model || !r.Priced || math.Abs(r.Cost-c.want) > 1e-9 {
			t.Errorf("%s: priced=%v cost=%v, want %v", c.name, r.Priced, r.Cost, c.want)
		}
		total += c.want
		rec := Record{Provider: c.prov, Model: c.model, TierSent: c.sent, TierServed: c.served, Input: in, Output: out, CacheRead: cached}
		if got, ok := cost(rec); !ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: a key limit counts it at %v (%v), want %v", c.name, got, ok, c.want)
		}
		// a subscription window's calls at their API price, a Fast one at Fast's
		if c.prov == "codex" {
			if pr := api(rec); pr == nil || math.Abs(rec.CostAt(*pr)-c.want) > 1e-9 {
				t.Errorf("%s: API price %+v, want cost %v", c.name, pr, c.want)
			}
		}
	}
	if math.Abs(sum.Cost-total) > 1e-9 {
		t.Errorf("ledger total %v, want %v", sum.Cost, total)
	}
	// a long Fast prompt: models.dev lists no long-context Fast price, so
	// the standard long-context one ($4 in, $15 out) is scaled by Fast's
	// own ratio to the standard (2x in, 2x out)
	long := Record{Provider: "codex", Model: "gpt-6.1-sol", TierSent: "priority", Input: 300_000, Output: 1_000}
	if got, want := func() float64 { c, _ := cost(long); return c }(), (300_000*8.0+1_000*30.0)/1e6; math.Abs(got-want) > 1e-9 {
		t.Errorf("a long Fast prompt costs %v, want %v", got, want)
	}
}
