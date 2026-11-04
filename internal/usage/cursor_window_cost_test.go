package usage

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A call the Cursor plugin served on a model listed at a context size
// (claude-opus-5-5@300k) is counted at its model's price on the request
// log, as a call on the plain id is, not at no cost (RyuJeXu on X, whose
// dashboard reads magpie's usage). A call on a model no catalog prices
// (Cursor's auto) stays unpriced.
func TestCursorWindowCallIsPriced(t *testing.T) {
	pageHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}}}`), 0600)
	now := holdClock(t, time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local))
	at := Today.Since(now).Add(time.Second)
	for i, m := range []string{"claude-opus-5-5@300k", "claude-opus-5-5", "auto"} {
		Append(Record{Time: at.Add(time.Duration(i) * time.Second), Agent: "claude-code", Provider: "cursor", Model: m, Requested: "cursor/" + m,
			Served: m, Input: 1_000_000, Output: 100_000, Status: 200})
	}
	p := QueryPage(Today, Filter{}, 0, 10)
	if len(p.Rows) != 3 {
		t.Fatalf("rows = %+v", p.Rows)
	}
	cost := map[string]Row{}
	for _, r := range p.Rows {
		cost[r.Model] = r
	}
	// $4 for 1M in, $2 for 100K out
	for _, m := range []string{"claude-opus-5-5@300k", "claude-opus-5-5"} {
		if r := cost[m]; !r.Priced || r.Cost < 5.999 || r.Cost > 6.001 {
			t.Errorf("%s: priced %v, cost %v; want $6", m, r.Priced, r.Cost)
		}
	}
	if r := cost["auto"]; r.Priced {
		t.Errorf("auto: priced at %v; want unpriced", r.Cost)
	}
	if p.Sum.Cost < 11.999 || p.Sum.Cost > 12.001 {
		t.Errorf("total cost = %v; want $12", p.Sum.Cost)
	}
}
