package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// holdClock stops Clock at at for the test and gives at back, for the test
// to stamp its calls by: midnight then never falls between a call and the
// period it is asked for in.
func holdClock(t *testing.T, at time.Time) time.Time {
	t.Helper()
	old := Clock
	Clock = func() time.Time { return at }
	t.Cleanup(func() { Clock = old })
	return at
}

// Every period is read by Clock, not by the machine's own time: a call made
// on a day Clock is held at is that day's in Summarize, the Requests page and
// its chart, the ledger and Direct, and the next day's Today has none of it.
// A path that reads time.Now itself fails here on any run.
func TestPeriodsReadTheClock(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	Append(Record{Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200}) // stamped by Clock
	calls := func(pts []SeriesPoint) (n int) {
		for _, p := range pts {
			n += p.Calls
		}
		return n
	}
	if s := Summarize(Today); s.Calls != 1 {
		t.Errorf("Summarize(Today): %d calls, want 1", s.Calls)
	}
	if p := QueryPage(Today, Filter{}, 0, 10); p.Total != 1 || calls(p.Series) != 1 {
		t.Errorf("QueryPage(Today): %d rows, %d in the chart, want 1 and 1", p.Total, calls(p.Series))
	}

	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now.Add(-time.Minute), Agent: "codex", Session: "c1", Model: "m", Tokens: sessions.Tokens{Input: 20}}}
	}
	t.Cleanup(func() { LogCalls = old })
	l := LedgerOf(Today, Filter{})
	if len(l.Rows) != 2 {
		t.Errorf("LedgerOf(Today): %d rows, want 2", len(l.Rows))
	}
	if _, pts := LedgerSeries(Today, l.Rows); calls(pts) != 2 {
		t.Errorf("LedgerSeries(Today): %d calls, want 2", calls(pts))
	}
	if d := Direct(Today); d.Calls != 1 || !d.Since.Equal(Today.Since(now)) {
		t.Errorf("Direct(Today): %d calls since %v, want 1 since %v", d.Calls, d.Since, Today.Since(now))
	}

	// the next day, today has none of it, though the answers for the day
	// before are cached
	LogCalls = old
	holdClock(t, now.AddDate(0, 0, 1))
	if s := Summarize(Today); s.Calls != 0 {
		t.Errorf("Summarize(Today) the next day: %d calls, want 0", s.Calls)
	}
	if p := QueryPage(Today, Filter{}, 0, 10); p.Total != 0 {
		t.Errorf("QueryPage(Today) the next day: %d rows, want 0", p.Total)
	}
}
