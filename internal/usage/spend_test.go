package usage

import (
	"math"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// spendHome is a home with relay/gpt-9 priced at $1 in and $2 out a
// million tokens, and spend's sums forgotten.
func spendHome(t *testing.T) {
	t.Helper()
	reportedHome(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{
		"relay/gpt-9": {Input: new(1.0), Output: new(2.0), CacheRead: new(0.0), CacheWrite: new(0.0)},
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-a", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-9"}}); err != nil {
		t.Fatal(err)
	}
	spendMemo.Lock()
	spendMemo.path, spendMemo.sums = "", nil
	spendMemo.Unlock()
}

// KeySpent (sparkling_lemur_62868's spend cap) is what magpie's calls on a
// key cost from a start on, at the Usage page's prices: the key's own calls
// alone, not another key's, not one before the start, not one turned away
// before it went out nor one an app reported (another computer's calls are
// in the shared files, which the log read here never holds). A call Append
// writes after the sum is read counts at once, though the sum is kept for
// minutes.
func TestKeySpentCountsTheKeysCalls(t *testing.T) {
	spendHome(t)
	a, b := provider.KeyID("sk-a"), provider.KeyID("sk-b")
	now := time.Now()
	start := now.Add(-2 * time.Hour)
	call := func(at time.Time, key string, in int) Record {
		return Record{Time: at, Agent: "codex", Provider: "relay", Model: "gpt-9", ProviderKeyID: key, Input: in, Output: 0, Status: 200}
	}
	Append(call(now.Add(-time.Hour), a, 1e6))          // $1
	Append(call(now.Add(-50*time.Minute), a, 2e6))     // $2
	Append(call(now.Add(-40*time.Minute), b, 5e6))     // another key's
	Append(call(now.Add(-3*time.Hour), a, 7e6))        // before the start
	rejected := call(now.Add(-30*time.Minute), a, 9e6) // turned away
	rejected.Rejected = true
	Append(rejected)
	reported := call(now.Add(-20*time.Minute), a, 9e6)
	reported.Source = SourceReported
	Append(reported)
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	if got := KeySpent(a, start); !near(got, 3) {
		t.Fatalf("key a spent %v, want 3", got)
	}
	if got := KeySpent(b, start); !near(got, 5) {
		t.Fatalf("key b spent %v, want 5", got)
	}
	// counted from earlier, the call before the old start is in
	if got := KeySpent(a, now.Add(-4*time.Hour)); !near(got, 10) {
		t.Fatalf("key a from 4h back spent %v, want 10", got)
	}
	if got := KeySpent(a, start); !near(got, 3) {
		t.Fatalf("key a read again spent %v, want 3", got)
	}
	// a new call is counted in the sum kept, without a read of the log
	Append(call(now, a, 4e6))
	if got := KeySpent(a, start); !near(got, 7) {
		t.Fatalf("after a new call key a spent %v, want 7", got)
	}
	if got := KeySpent(provider.KeyID("sk-none"), start); got != 0 {
		t.Fatalf("a key never used spent %v", got)
	}
}
