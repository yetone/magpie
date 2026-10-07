package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClaudeUsage has Claude Code's /usage print out (or fail with err),
// counting its runs, and fails the test on any request magpie makes to
// Anthropic itself.
func fakeClaudeUsage(t *testing.T, out *atomic.Value, fail *atomic.Bool) *atomic.Int32 {
	t.Helper()
	var runs atomic.Int32
	old, oldAsked := claudeCLIUsage, claudeAsked.Load()
	UsageClaudeVia(func(context.Context) (string, error) {
		runs.Add(1)
		if fail != nil && fail.Load() {
			return "", errors.New("Claude Code: network is unreachable")
		}
		return out.Load().(string), nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("magpie asked Anthropic itself: %s", r.URL)
	}))
	oldBase, oldWait, oldUsed := claudeBase, claudeUsageWait, claudeUsedSince
	claudeBase = srv.URL
	claudeUsageWait = func() time.Duration { return usageTestWait }
	claudeUsedSince = func(time.Time) bool { return true }
	claudeAsked.Store(0)
	claudeUsage.Lock()
	claudeUsage.m = nil
	claudeUsage.Unlock()
	t.Cleanup(func() {
		claudeCLIUsage, claudeBase, claudeUsageWait, claudeUsedSince = old, oldBase, oldWait, oldUsed
		claudeAsked.Store(oldAsked)
		srv.Close()
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.asked = false
		subscriptionUsageCache.Unlock()
	})
	return &runs
}

// Claude's usage is Claude Code's /usage, run only when the user asks,
// once an ask, and only for the account Claude Code is signed in to.
func TestClaudeWindowsAsked(t *testing.T) {
	var out atomic.Value
	out.Store("Current session: 40% used · resets " + sessionReset() + "\nCurrent week (all models): 10% used · resets " + soon(3) + " at 2pm (UTC)\n")
	var fail atomic.Bool
	runs := fakeClaudeUsage(t, &out, &fail)
	ctx := context.Background()

	// a saved account is never read, asked or not
	if _, err := claudeWindows(ctx, "saved@x", false); err != errClaudeSaved || runs.Load() != 0 {
		t.Fatalf("saved unasked: %v %d", err, runs.Load())
	}
	// a saved account is never read, asked or not
	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "saved@x", false); err != errClaudeSaved || runs.Load() != 0 {
		t.Fatalf("saved: %v %d", err, runs.Load())
	}

	// asked: one run, then what it told
	ws, err := claudeWindows(ctx, "A@x", true)
	if err != nil || len(ws) != 2 || ws[0].Used != 40 || ws[1].Used != 10 || runs.Load() != 1 {
		t.Fatalf("asked: %v %+v %d", err, ws, runs.Load())
	}
	for range 3 {
		if ws, err = claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 2 || runs.Load() != 1 {
			t.Fatalf("kept: %v %d", err, runs.Load())
		}
	}
	// asked again at once: still the one run
	AskClaudeUsage()
	if _, err = claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("too soon: %v %d", err, runs.Load())
	}

	// its wait on, unasked: run again by itself, once
	age := func(d time.Duration) {
		claudeUsage.Lock()
		e := claudeUsage.m["a@x"]
		e.tried = e.tried.Add(-d)
		claudeUsage.m["a@x"] = e
		claudeUsage.Unlock()
	}
	claudeAsked.Store(0) // the ask above was answered by the run before it
	age(usageTestWait - time.Minute)
	if _, err = claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("ran before its wait: %v %d", err, runs.Load())
	}
	age(time.Minute)
	out.Store("Current session: 55% used · resets " + sessionReset() + "\n")
	for range 3 {
		if ws, err = claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 1 || ws[0].Used != 55 || runs.Load() != 2 {
			t.Fatalf("every: %v %+v %d", err, ws, runs.Load())
		}
	}

	// a failed run says why, and isn't run again until asked or its wait is up
	claudeUsage.Lock()
	claudeUsage.m["b@x"] = claudeUsageEntry{tried: time.Now().Add(-claudeUsageWaitMax)}
	claudeUsage.Unlock()
	fail.Store(true)
	if _, err = claudeWindows(ctx, "b@x", true); err == nil || runs.Load() != 3 {
		t.Fatalf("failed: %v %d", err, runs.Load())
	}
	if _, err = claudeWindows(ctx, "b@x", true); err == nil || err == errClaudeNotAsked || runs.Load() != 3 {
		t.Fatalf("after a failure: %v %d", err, runs.Load())
	}
}

func TestParseClaudeUsage(t *testing.T) {
	now := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	text := `You are currently using your subscription to power your Claude Code usage

Current session: 13% used · resets Oct 1 at 3:30pm (Asia/Shanghai)
Current week (all models): 4% used · resets Oct 3 at 2pm (Asia/Shanghai)
Current week (Opus): 12.5% used · resets Jan 2 at 2pm (Asia/Shanghai)
Current week (Fable): 0% used

What's contributing to your limits usage?
  96% of your usage came from sessions active for 8+ hours`
	ws, err := parseClaudeUsage(text, now)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	want := []struct {
		name, model string
		used        float64
		span        time.Duration
		reset       time.Time
	}{
		{"5 hours", "", 13, 5 * time.Hour, time.Date(2026, 10, 1, 15, 30, 0, 0, sh)},
		{"7 days", "", 4, 7 * 24 * time.Hour, time.Date(2026, 10, 3, 14, 0, 0, 0, sh)},
		// a week can't end three months on: it ended last Jan 2, and reads
		// as renewed
		{"7 days · Opus", "opus", 12.5, 7 * 24 * time.Hour, time.Date(2026, 1, 2, 14, 0, 0, 0, sh)},
		{"7 days · Fable", "fable", 0, 7 * 24 * time.Hour, time.Time{}},
	}
	if len(ws) != len(want) {
		t.Fatalf("windows %+v", ws)
	}
	for i, w := range want {
		g := ws[i]
		if g.Name != w.name || g.Model != w.model || g.Used != w.used || g.Span != w.span ||
			(w.reset.IsZero() != (g.ResetsAt == nil)) || (g.ResetsAt != nil && !g.ResetsAt.Equal(w.reset)) {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}
	// a time alone is the next one
	if r, ok := claudeResetTime("3am (UTC)", now, claudeWeek); !ok || !r.Equal(time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("time alone: %v %v", r, ok)
	}
	// Claude Code 2.1.285 on puts a comma where "at" was (#631), read
	// within the week before Oct 9
	seen := time.Date(2026, 10, 3, 4, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"Oct 9, 2:59pm (UTC)":        time.Date(2026, 10, 9, 14, 59, 0, 0, time.UTC),
		"Oct 2, 8pm (UTC)":           time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC),
		"Jan 2, 9:05am (UTC)":        time.Date(2026, 1, 2, 9, 5, 0, 0, time.UTC),
		"Jan 2, 2027, 9am (UTC)":     time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC),
		"Oct 3, 2pm (Asia/Shanghai)": time.Date(2026, 10, 3, 14, 0, 0, 0, sh),
	} {
		if r, ok := claudeResetTime(in, seen, claudeWeek); !ok || !r.Equal(want) {
			t.Errorf("%q: %v %v, want %v", in, r, ok, want)
		}
	}
	if _, err := parseClaudeUsage("Error: not logged in", now); err == nil {
		t.Fatal("nothing told, no error")
	}
}

// Where the clocks go back and read a reset's time twice, the reading not yet
// past is taken: west of UTC time.Date gives the first reading, east of it
// the second. A time alone is the next day's when the clocks skip it, as
// they skip 2:30am in New York and Berlin or 12:30am in Santiago. Claude
// Code 2.1.290 prints a date on every line of /usage, with "at" or a comma
// by platform; a time alone is the older form.
func TestClaudeResetTimeAroundClockChanges(t *testing.T) {
	zone := func(name string) *time.Location {
		l, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	ny, berlin, santiago := zone("America/New_York"), zone("Europe/Berlin"), zone("America/Santiago")
	fixed := func(name string, hours float64) *time.Location {
		return time.FixedZone(name, int(hours*3600))
	}
	edt, est, cest, cet := fixed("EDT", -4), fixed("EST", -5), fixed("CEST", 2), fixed("CET", 1)
	aedt, aest, lhdt, lhst := fixed("AEDT", 11), fixed("AEST", 10), fixed("+11", 11), fixed("+1030", 10.5)
	for _, c := range []struct {
		now  time.Time
		in   string
		want time.Time
	}{
		// New York reads 2026-11-01 01:00-01:59 twice, EDT then EST
		{time.Date(2026, 11, 1, 1, 20, 0, 0, est), "Nov 1, 1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, est)},
		{time.Date(2026, 11, 1, 1, 20, 0, 0, est), "Nov 1 at 1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, est)},
		{time.Date(2026, 11, 1, 1, 20, 0, 0, est), "1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, est)},
		{time.Date(2026, 11, 1, 1, 0, 0, 0, est), "1am (America/New_York)", time.Date(2026, 11, 1, 1, 0, 0, 0, est)},
		{time.Date(2026, 11, 1, 1, 20, 0, 0, edt), "Nov 1, 1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, edt)},
		{time.Date(2026, 11, 1, 1, 20, 0, 0, edt), "1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, edt)},
		{time.Date(2026, 11, 1, 1, 50, 0, 0, est), "1:30am (America/New_York)", time.Date(2026, 11, 2, 1, 30, 0, 0, ny)},
		{time.Date(2026, 11, 3, 1, 20, 0, 0, ny), "Nov 3, 1:30am (America/New_York)", time.Date(2026, 11, 3, 1, 30, 0, 0, ny)},
		{time.Date(2026, 11, 3, 1, 20, 0, 0, ny), "1:30am (America/New_York)", time.Date(2026, 11, 3, 1, 30, 0, 0, ny)},
		// a date less than a day past keeps that day's reading
		{time.Date(2026, 11, 2, 1, 0, 0, 0, est), "Nov 1, 1:30am (America/New_York)", time.Date(2026, 11, 1, 1, 30, 0, 0, est)},
		// Berlin reads 2026-10-25 02:00-02:59 twice, CEST then CET
		{time.Date(2026, 10, 25, 2, 20, 0, 0, cest), "Oct 25, 2:30am (Europe/Berlin)", time.Date(2026, 10, 25, 2, 30, 0, 0, cest)},
		{time.Date(2026, 10, 25, 2, 20, 0, 0, cest), "2:30am (Europe/Berlin)", time.Date(2026, 10, 25, 2, 30, 0, 0, cest)},
		{time.Date(2026, 10, 24, 23, 0, 0, 0, cest), "2:30am (Europe/Berlin)", time.Date(2026, 10, 25, 2, 30, 0, 0, cest)},
		{time.Date(2026, 10, 25, 2, 20, 0, 0, cet), "Oct 25, 2:30am (Europe/Berlin)", time.Date(2026, 10, 25, 2, 30, 0, 0, cet)},
		{time.Date(2026, 10, 26, 2, 0, 0, 0, cet), "Oct 25, 2:15am (Europe/Berlin)", time.Date(2026, 10, 25, 2, 15, 0, 0, cet)},
		{time.Date(2026, 4, 5, 2, 20, 0, 0, aedt), "Apr 5, 2:30am (Australia/Sydney)", time.Date(2026, 4, 5, 2, 30, 0, 0, aedt)},
		{time.Date(2026, 4, 5, 2, 20, 0, 0, aest), "Apr 5, 2:30am (Australia/Sydney)", time.Date(2026, 4, 5, 2, 30, 0, 0, aest)},
		{time.Date(2026, 4, 5, 1, 40, 0, 0, lhdt), "Apr 5, 1:45am (Australia/Lord_Howe)", time.Date(2026, 4, 5, 1, 45, 0, 0, lhdt)},
		{time.Date(2026, 4, 5, 1, 40, 0, 0, lhst), "Apr 5, 1:45am (Australia/Lord_Howe)", time.Date(2026, 4, 5, 1, 45, 0, 0, lhst)},
		// the clocks skipped 2:00-2:59 this morning
		{time.Date(2026, 3, 8, 22, 0, 0, 0, ny), "2:30am (America/New_York)", time.Date(2026, 3, 9, 2, 30, 0, 0, ny)},
		{time.Date(2026, 3, 29, 23, 0, 0, 0, berlin), "2:30am (Europe/Berlin)", time.Date(2026, 3, 30, 2, 30, 0, 0, berlin)},
		// Santiago's clocks skip from 2026-09-06 00:00 to 01:00
		{time.Date(2026, 9, 5, 23, 31, 0, 0, santiago), "12:30am (America/Santiago)", time.Date(2026, 9, 7, 0, 30, 0, 0, santiago)},
		{time.Date(2026, 9, 5, 23, 31, 0, 0, santiago), "1:30am (America/Santiago)", time.Date(2026, 9, 6, 1, 30, 0, 0, santiago)},
	} {
		if r, ok := claudeResetTime(c.in, c.now, claudeWeek); !ok || !r.Equal(c.want) {
			t.Errorf("%q at %v: %v %v (in %v), want %v", c.in, c.now, r, ok, r.Sub(c.now), c.want)
		}
	}
}

// A date with no year is in the first year it is less than a day past and
// no more than its window ahead. Claude Code prints the year when it isn't
// this one, so a Dec 31 with none is read past New Year only when /usage ran
// just before midnight.
func TestClaudeResetTimeAtNewYear(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		now  time.Time
		in   string
		want time.Time
	}{
		{time.Date(2027, 1, 1, 0, 0, 5, 0, ny), "Dec 31, 11:59pm (America/New_York)", time.Date(2026, 12, 31, 23, 59, 0, 0, ny)},
		{time.Date(2027, 1, 1, 0, 0, 5, 0, ny), "Dec 31 at 11:59pm (America/New_York)", time.Date(2026, 12, 31, 23, 59, 0, 0, ny)},
		{time.Date(2027, 1, 1, 1, 0, 0, 0, ny), "Dec 31, 2026, 11pm (America/New_York)", time.Date(2026, 12, 31, 23, 0, 0, 0, ny)},
		// more than a day past is the reading read late, not next year's
		{time.Date(2027, 1, 1, 1, 0, 0, 0, ny), "Dec 30, 11pm (America/New_York)", time.Date(2026, 12, 30, 23, 0, 0, 0, ny)},
		{time.Date(2027, 7, 1, 1, 0, 0, 0, ny), "Jun 30, 11pm (America/New_York)", time.Date(2027, 6, 30, 23, 0, 0, 0, ny)},
		{time.Date(2026, 12, 31, 22, 0, 0, 0, ny), "Jan 1, 2027, 3pm (America/New_York)", time.Date(2027, 1, 1, 15, 0, 0, 0, ny)},
		{time.Date(2026, 12, 31, 22, 0, 0, 0, ny), "Jan 1, 3pm (America/New_York)", time.Date(2027, 1, 1, 15, 0, 0, 0, ny)},
		{time.Date(2026, 12, 31, 22, 0, 0, 0, ny), "Dec 31, 9pm (America/New_York)", time.Date(2026, 12, 31, 21, 0, 0, 0, ny)},
	} {
		if r, ok := claudeResetTime(c.in, c.now, claudeWeek); !ok || !r.Equal(c.want) {
			t.Errorf("%q at %v: %v %v (in %v), want %v", c.in, c.now, r, ok, r.Sub(c.now), c.want)
		}
	}
}

// A reset ends a window of known length, so it is never further ahead than
// that: a reading too far ahead for its window, as /usage's text read days
// late is, is the latest one past, and the window reads as renewed rather
// than full for a year.
func TestClaudeResetTimeWithinItsWindow(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	session := 5 * time.Hour
	oct5 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		now  time.Time
		in   string
		span time.Duration
		want time.Time
	}{
		// four days late: last Oct 1, not next year's
		{oct5, "Oct 1, 3pm (UTC)", claudeWeek, time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)},
		{oct5, "Oct 1 at 3pm (UTC)", claudeWeek, time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)},
		{oct5, "Oct 1, 3pm (UTC)", session, time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)},
		// six days on is a week's, not five hours'
		{oct5, "Oct 11, 3pm (UTC)", claudeWeek, time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)},
		{oct5, "Oct 11, 3pm (UTC)", session, time.Date(2025, 10, 11, 15, 0, 0, 0, time.UTC)},
		// a window's own length on, and a little over, is still ahead;
		// further is not
		{oct5, "Oct 5, 5pm (UTC)", session, time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC)},
		{oct5, "Oct 5, 7pm (UTC)", session, time.Date(2026, 10, 5, 19, 0, 0, 0, time.UTC)},
		{oct5, "Oct 5, 7:01pm (UTC)", session, time.Date(2025, 10, 5, 19, 1, 0, 0, time.UTC)},
		{oct5, "Oct 5, 7:30pm (UTC)", session, time.Date(2025, 10, 5, 19, 30, 0, 0, time.UTC)},
		{oct5, "Oct 12, 12pm (UTC)", claudeWeek, time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)},
		{oct5, "Oct 12, 2pm (UTC)", claudeWeek, time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)},
		{oct5, "Oct 12, 3pm (UTC)", claudeWeek, time.Date(2025, 10, 12, 15, 0, 0, 0, time.UTC)},
		// a week across the night New York's clocks go back ends an hour
		// earlier on the clock
		{time.Date(2026, 10, 29, 14, 0, 0, 0, ny), "Nov 5, 1pm (America/New_York)", claudeWeek, time.Date(2026, 11, 5, 13, 0, 0, 0, ny)},
		// a time alone is the next one within the window, else today's past
		{oct5, "3pm (UTC)", session, time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)},
		{oct5, "11am (UTC)", session, time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC)},
		{oct5, "11am (UTC)", claudeWeek, time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)},
		{oct5, "7pm (UTC)", session, time.Date(2026, 10, 5, 19, 0, 0, 0, time.UTC)},
		{oct5, "7:01pm (UTC)", session, time.Date(2026, 10, 4, 19, 1, 0, 0, time.UTC)},
		// the year said is kept
		{oct5, "Oct 1, 2027, 3pm (UTC)", session, time.Date(2027, 10, 1, 15, 0, 0, 0, time.UTC)},
	} {
		if r, ok := claudeResetTime(c.in, c.now, c.span); !ok || !r.Equal(c.want) {
			t.Errorf("%q for %v at %v: %v %v (in %v), want %v", c.in, c.span, c.now, r, ok, r.Sub(c.now), c.want)
		}
	}

	// /usage giving a reset four days past: the full session and week read
	// as renewed, fresh and kept, and routing doesn't hold the account
	var out atomic.Value
	out.Store("Current session: 100% used · resets " + soon(-4) + ", 3pm (UTC)\nCurrent week (all models): 100% used · resets " + soon(-4) + ", 3pm (UTC)\n")
	fakeClaudeUsage(t, &out, nil)
	AskClaudeUsage()
	for _, read := range []bool{true, false} {
		ws, err := claudeWindows(context.Background(), "late@x", read)
		if err != nil || len(ws) != 2 {
			t.Fatalf("read %v: %v %+v", read, err, ws)
		}
		for _, w := range ws {
			if w.Used != 0 || w.ResetsAt != nil {
				t.Errorf("read %v: %s %+v", read, w.Name, w)
			}
		}
		if full := allowanceOf(ws, time.Now()).Full("claude-fable-5-1", SpentShare, time.Now()); !full.IsZero() {
			t.Errorf("read %v: held until %v", read, full)
		}
	}
}

// claudeWeek is the length of /usage's "Current week" windows.
const claudeWeek = 7 * 24 * time.Hour

// usageTestWait is the wait between unasked runs of /usage in tests.
const usageTestWait = 7 * time.Minute

// An unasked run of /usage waits a whole number of minutes from 5 to 15,
// drawn afresh each time, so it isn't run on a clock.
func TestClaudeWaitRandom(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 2000 {
		w := claudeUsageWait()
		if w < claudeUsageWaitMin || w > claudeUsageWaitMax || w%time.Minute != 0 {
			t.Fatalf("wait %v", w)
		}
		seen[w] = true
	}
	if len(seen) != 11 {
		t.Fatalf("waits drawn: %v", seen)
	}
}

// Unasked, /usage is run again only once Claude Code has been used since
// it last was; asked, it is run whether it was or not.
func TestClaudeUsageIdle(t *testing.T) {
	var out atomic.Value
	out.Store("Current session: 40% used · resets " + sessionReset() + "\n")
	runs := fakeClaudeUsage(t, &out, nil)
	var used atomic.Bool
	claudeUsedSince = func(time.Time) bool { return used.Load() }
	ctx := context.Background()

	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("asked: %v %d", err, runs.Load())
	}
	claudeAsked.Store(0)
	claudeUsage.Lock()
	e := claudeUsage.m["a@x"]
	e.tried = e.tried.Add(-usageTestWait)
	claudeUsage.m["a@x"] = e
	claudeUsage.Unlock()

	// its wait is up, but nobody used Claude Code
	if ws, err := claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 1 || runs.Load() != 1 {
		t.Fatalf("idle: %v %d", err, runs.Load())
	}
	// asked, it runs all the same
	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 2 {
		t.Fatalf("asked while idle: %v %d", err, runs.Load())
	}
	claudeAsked.Store(0)
	claudeUsage.Lock()
	e = claudeUsage.m["a@x"]
	e.tried = e.tried.Add(-usageTestWait)
	claudeUsage.m["a@x"] = e
	claudeUsage.Unlock()
	// used since: it runs by itself again
	used.Store(true)
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 3 {
		t.Fatalf("used: %v %d", err, runs.Load())
	}
}

// Claude Code was used since a time when one of its sessions was written
// to since then; its other files don't count.
func TestClaudeUsedSince(t *testing.T) {
	claudeHome(t)
	project := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-Users-x-proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(project, "s.jsonl")
	os.WriteFile(session, []byte("{}\n"), 0o600)
	os.WriteFile(filepath.Join(project, "notes.txt"), nil, 0o600)
	now := time.Now()
	old := now.Add(-time.Hour)
	os.Chtimes(session, old, old)
	if claudeUsedSince(now.Add(-time.Minute)) {
		t.Fatal("used, with no session written to")
	}
	if !claudeUsedSince(now.Add(-2 * time.Hour)) {
		t.Fatal("not used, with a session written to")
	}
	os.Chtimes(session, now, now)
	if !claudeUsedSince(now.Add(-time.Minute)) {
		t.Fatal("not used, with a session just written to")
	}
}

// soon is the day n days from now as /usage writes it ("Oct 3"), so a
// window the tests read hasn't reset whatever day they run.
func soon(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("Jan 2") }

// sessionReset is a "Current session" reset as /usage prints it: within the
// five hours.
func sessionReset() string {
	return time.Now().UTC().Add(3*time.Hour).Format("Jan 2 at 3:04pm") + " (UTC)"
}
