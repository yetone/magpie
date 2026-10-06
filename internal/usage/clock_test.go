package usage

import (
	"os"
	"path/filepath"
	"sync/atomic"
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

// midnightClock sets Clock to read a tenth of a second before midnight the
// first time and a tenth of a second after it from then on, as when midnight
// falls while an answer is worked out. It is set again for each answer.
func midnightClock(t *testing.T, midnight time.Time) {
	t.Helper()
	var read atomic.Bool
	old := Clock
	Clock = func() time.Time {
		if read.Swap(true) {
			return midnight.Add(100 * time.Millisecond)
		}
		return midnight.Add(-100 * time.Millisecond)
	}
	t.Cleanup(func() { Clock = old })
}

// An answer reads the time once. A Requests page asked for as midnight falls
// is of the day it was asked on: its rows, a session file written that day
// among them, are under that day's chart, whether the page comes from the
// indexed log or from the ledger, and it is kept for that day only. Direct's
// period starts on the day of the calls it counts. Read more than once, the
// rows were of one day and the chart or the period of the next.
func TestAnswerReadsTheClockOnce(t *testing.T) {
	pageHome(t)
	midnight := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	day := midnight.AddDate(0, 0, -1)
	for _, at := range []time.Time{day.Add(12 * time.Hour), midnight.Add(-time.Second)} {
		Append(Record{Time: at, Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200})
	}
	// a session file last written at 23:00, with a call of that time
	at := day.Add(23 * time.Hour)
	path := filepath.Join(sessions.ClaudeDir(), "projects", "p", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	call := []sessions.Call{{Time: at, Agent: "codex", Session: "s", Model: "m", Tokens: sessions.Tokens{Input: 20}}}
	page := func(want int, from time.Time) {
		t.Helper()
		p := queryPage(Today, Filter{}, 0, 10, func(sessions.CallSource) []sessions.Call { return call })
		var calls int
		var first time.Time
		for i, pt := range p.Series {
			if i == 0 {
				first = pt.Time
			}
			calls += pt.Calls
		}
		if p.Total != want || calls != want || !first.Equal(from) {
			t.Errorf("QueryPage(Today): %d rows, %d in a chart from %v; want %d under one from %v", p.Total, calls, first, want, from)
		}
	}
	midnightClock(t, midnight)
	page(3, day)
	// asked again after midnight, the next day's page, not the one kept for
	// the day before
	page(0, midnight)

	// the same page from the ledger
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call { return call }
	t.Cleanup(func() { LogCalls = old })
	midnightClock(t, midnight)
	page(3, day)

	midnightClock(t, midnight)
	if d := Direct(Today); d.Calls != 1 || !d.Since.Equal(day) {
		t.Errorf("Direct(Today): %d calls since %v, want 1 since %v", d.Calls, d.Since, day)
	}
}
