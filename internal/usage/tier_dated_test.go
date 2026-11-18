package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// mu33410 on X asked whether Fast and the other modes are priced as such.
// OpenAI's pricing page prices GPT-6.2 Sol's and Astra's Ultrafast at 3
// times the standard rate through 2027-02-28 and 6 times from 2027-03-01
// (Pacific time), and models.dev lists the mode with no cost, so an
// Ultrafast call there was priced at the standard rate, below Fast's. The
// catalog is models.dev's own bytes for gpt-6.2-sol and claude-opus-5-5.
func TestDatedUltrafastPricedAtItsDay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{`+
		`"gpt-6.2-sol":{"id":"gpt-6.2-sol","cost":{"input":2,"output":10,"cache_read":0.1,"cache_write":2.5,"tiers":[{"input":4,"output":15,"cache_read":0.2,"cache_write":5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":4,"output":15,"cache_read":0.2,"cache_write":5}},"experimental":{"modes":{"fast":{"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"provider":{"body":{"service_tier":"ultrafast"}}}}}}}},`+
		`"anthropic":{"id":"anthropic","models":{`+
		`"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"experimental":{"modes":{"fast":{"cost":{"input":8,"output":40,"cache_read":0.4,"cache_write":10},"provider":{"body":{"speed":"fast"},"headers":{"anthropic-beta":"fast-mode-2026-02-01"}}}}}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	pt, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	change := time.Date(2027, 3, 1, 0, 0, 0, 0, pt)
	before, after := change.Add(-time.Second), change
	in, out, cached := 40_000, 2_000, 100_000
	sol := (float64(in)*2 + float64(out)*10 + float64(cached)*0.1) / 1e6
	opus := (float64(in)*4 + float64(out)*20 + float64(cached)*0.2) / 1e6
	calls := []struct {
		name, prov, model, sent, served string
		at                              time.Time
		want                            float64
	}{
		{"API Ultrafast in the promotion", "openai", "gpt-6.2-sol", "ultrafast", "ultrafast", before, 3 * sol},
		{"API Ultrafast from March 1st", "openai", "gpt-6.2-sol", "ultrafast", "ultrafast", after, 6 * sol},
		{"Codex Ultrafast in the promotion", "codex", "gpt-6.2-sol", "ultrafast", "", before, 3 * sol},
		{"Codex Ultrafast from March 1st", "codex", "gpt-6.2-sol", "ultrafast", "", after, 6 * sol},
		{"API Ultrafast run at the default tier", "openai", "gpt-6.2-sol", "ultrafast", "default", before, sol},
		{"API Ultrafast run at priority", "openai", "gpt-6.2-sol", "ultrafast", "priority", after, 2 * sol},
		{"API Fast after the change", "openai", "gpt-6.2-sol", "priority", "priority", after, 2 * sol},
		{"API standard", "openai", "gpt-6.2-sol", "", "", after, sol},
		{"Claude fast, unchanged", "anthropic", "claude-opus-5-5", "fast", "", after, 2 * opus},
		{"Claude standard", "anthropic", "claude-opus-5-5", "", "", before, opus},
	}
	// one Pricer and one Coster across every call: a cache keyed without
	// the day would price the second Ultrafast call as the first
	cost, api, priceOf := NewCoster(), apiPricer(), Pricer()
	for _, c := range calls {
		rec := Record{Time: c.at, Provider: c.prov, Model: c.model, TierSent: c.sent, TierServed: c.served, Input: in, Output: out, CacheRead: cached}
		if pr := priceOf(rec); pr == nil || math.Abs(rec.CostAt(*pr)-c.want) > 1e-9 {
			t.Errorf("%s: the ledger prices it %+v, want cost %v", c.name, pr, c.want)
		}
		if got, ok := cost(rec); !ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: a key limit counts it at %v (%v), want %v", c.name, got, ok, c.want)
		}
		if c.prov == "codex" {
			if pr := api(rec); pr == nil || math.Abs(rec.CostAt(*pr)-c.want) > 1e-9 {
				t.Errorf("%s: API price %+v, want cost %v", c.name, pr, c.want)
			}
		}
	}
	// a long Ultrafast prompt: the standard long-context rate ($4 in, $15
	// out) three times over
	long := Record{Time: before, Provider: "openai", Model: "gpt-6.2-sol", TierSent: "ultrafast", TierServed: "ultrafast", Input: 300_000, Output: 1_000}
	if got, want := func() float64 { c, _ := cost(long); return c }(), 3*(300_000*4.0+1_000*15.0)/1e6; math.Abs(got-want) > 1e-9 {
		t.Errorf("a long Ultrafast prompt costs %v, want %v", got, want)
	}
}
