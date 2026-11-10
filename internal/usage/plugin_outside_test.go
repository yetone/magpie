package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// fakeOutside stands in for the plugin host: one plugin that lists its
// calls (Cursor's, as auth.calls gives them) with one account, and the
// since each reading asked from.
func fakeOutside(t *testing.T, now time.Time, calls func(since time.Time) ([]plugin.OutsideCall, error)) *[]time.Time {
	t.Helper()
	was := outsideIO
	t.Cleanup(func() { outsideIO = was })
	var asked []time.Time
	outsideIO.providers = func() []plugin.Provider {
		return []plugin.Provider{
			{ID: "cursor", Calls: true, Accounts: []plugin.Account{{Key: "cursor"}}},
			{ID: "other", Accounts: []plugin.Account{{Key: "other"}}},
		}
	}
	outsideIO.calls = func(_ context.Context, id, account string, since time.Time) ([]plugin.OutsideCall, error) {
		if id != "cursor" || account != "cursor" {
			t.Fatalf("asked %s/%s, which doesn't list its calls", id, account)
		}
		asked = append(asked, since)
		return calls(since)
	}
	outsideIO.now = func() time.Time { return now }
	return &asked
}

// Calls a plugin says were made outside magpie (Cursor's editor and
// cursor-agent, WaiooT on X) are counted on the Usage page as reported
// calls: by the plugin's agent and provider, tokens and the vendor's cost
// as given, each once however often it is read again.
func TestOutsideCallsCounted(t *testing.T) {
	reportedHome(t)
	now := time.Now().Truncate(time.Second)
	cost := 0.00415
	ev := []plugin.OutsideCall{
		{RID: "ev-1", T: now.Add(-2 * time.Hour).UnixMilli(), Model: "grok-4.7-medium", In: 1200, Out: 300, CacheRead: 45000, Cost: &cost, Session: "conv-1"},
		{RID: "ev-2", T: now.Add(-time.Hour).UnixMilli(), Model: "claude-haiku-5-5-thinking-high", In: 2, Out: 7, CacheWrite: 2052},
	}
	asked := fakeOutside(t, now, func(time.Time) ([]plugin.OutsideCall, error) { return ev, nil })
	ReadOutsideCalls(context.Background())
	ReadOutsideCalls(context.Background())
	forgetReported() // a restart reads them once more
	ReadOutsideCalls(context.Background())

	rows, _, _ := Ledger(All, Filter{Through: SourceReported})
	if len(rows) != 2 {
		t.Fatalf("%d reported rows, want 2: %+v", len(rows), rows)
	}
	recs := Load(time.Time{})
	var got []Record
	for _, r := range recs {
		if r.Source == SourceReported {
			got = append(got, r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("%d reported records in the log, want 2", len(got))
	}
	r := got[0]
	if r.Agent != "cursor" || r.Provider != "cursor" || r.Model != "grok-4.7-medium" || r.Input != 1200 || r.Output != 300 || r.CacheRead != 45000 ||
		r.RequestID != "cursor:ev-1" || r.Session != "conv-1" || r.OwnCost == nil || *r.OwnCost != cost || !r.Time.Equal(now.Add(-2*time.Hour)) || r.Status != 200 {
		t.Fatalf("first row %+v", r)
	}
	if got[1].CacheWrite != 2052 || got[1].OwnCost != nil {
		t.Fatalf("second row %+v", got[1])
	}
	// the first reading went 30 days back; the next from a little before
	// the last one ended
	if len(*asked) != 3 || !(*asked)[0].Equal(now.Add(-outsideBack)) || !(*asked)[1].Equal(now.Add(-outsideOverlap)) {
		t.Fatalf("asked from %v", *asked)
	}
}

// The first reading starts after the gateway's last call to the plugin's
// provider: those were sent before the plugin knew its own, and are in the
// log already. A reading that fails asks again from the same time.
func TestOutsideCallsStartAfterTheGateway(t *testing.T) {
	reportedHome(t)
	now := time.Now().Truncate(time.Second)
	through := now.Add(-3 * time.Hour)
	Append(Record{Time: through, Agent: "claude", Provider: "cursor", Model: "auto", Input: 10, Output: 1, Status: 200, RequestID: "gw-1"})
	Append(Record{Time: now.Add(-time.Hour), Agent: "claude", Provider: "claude", Model: "x", Status: 200, RequestID: "gw-2"})
	fail := true
	asked := fakeOutside(t, now, func(time.Time) ([]plugin.OutsideCall, error) {
		if fail {
			return nil, errors.New("503")
		}
		return nil, nil
	})
	ReadOutsideCalls(context.Background())
	fail = false
	ReadOutsideCalls(context.Background())
	want := through.Add(time.Minute)
	if len(*asked) != 2 || !(*asked)[0].Equal(want) || !(*asked)[1].Equal(want) {
		t.Fatalf("asked from %v, want %v twice", *asked, want)
	}
}
