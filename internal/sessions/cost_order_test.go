package sessions

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A session's cost is its models' costs summed in one order, so it reads
// the same each time: summed in a map's order, a session of several models
// came out 15225.3933882 one read and 15225.393388199998 the next, and the
// Sessions tab was sent its row again on every poll for it.
func TestSessionCostSummedInOneOrder(t *testing.T) {
	f := file{agent: "claude", key: "claude:s", path: "/s.jsonl", main: true, mod: modOf(time.Unix(1790000000, 0))}
	var s state
	for i, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		*s.Models.at(name) = Tokens{Input: 100000 + 33333*i}
	}
	cache = map[string]*state{f.path: &s}
	t.Cleanup(func() { cache = nil })
	price := func(string) *catalog.Price { return &catalog.Price{Input: 0.1} }
	costs := map[uint64]bool{}
	for range 200 {
		got, ok := assembleListed([]file{f}, price, func(string) string { return "" })
		if !ok {
			t.Fatal("the session isn't listed")
		}
		costs[math.Float64bits(got.Cost)] = true
	}
	if len(costs) != 1 {
		t.Errorf("one session's cost read as %d different numbers", len(costs))
	}
}

// So is a session's summary in the stats, which the Sessions tab reads
// again every poll too.
func TestSummaryCostSummedInOneOrder(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	was := PriceOf
	PriceOf = func(settings.Settings, string) (catalog.Price, bool) { return catalog.Price{Input: 0.1}, true }
	t.Cleanup(func() { PriceOf = was })
	dir := filepath.Join(claude, "projects", "-work-alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body string
	for i, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1", "claude-opus-4-1", "claude-sonnet-4-5"} {
		body += aliasMsg("m"+itoa(i), model, "2026-09-25T10:0"+itoa(i)+":00Z", 100003+33331*i)
	}
	if err := os.WriteFile(filepath.Join(dir, "44444444-2222-3333-4444-555555555555.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	costs := map[uint64]bool{}
	for range 50 {
		for _, sum := range statsAt(0, statsNow).Sessions {
			if sum.Cwd == "/work/alias" {
				costs[math.Float64bits(sum.Cost)] = true
			}
		}
	}
	if len(costs) != 1 {
		t.Errorf("one session's summary cost read as %d different numbers", len(costs))
	}
}
