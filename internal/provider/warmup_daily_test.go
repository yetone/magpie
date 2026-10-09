package provider

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// a zone with no daylight saving, as a day's start is the local clock's
var east8 = time.FixedZone("UTC+8", 8*60*60)

func at8(day, h, m int) time.Time { return time.Date(2026, 9, day, h, m, 0, 0, east8) }

func TestDailyDue(t *testing.T) {
	idleAt := func(now time.Time) QuotaWindow { return win("5 hours", fiveHours, 0, now.Add(fiveHours)) }
	for _, c := range []struct {
		name     string
		at, last string
		now      time.Time
		cur      func(time.Time) QuotaWindow
		want     string // the day it is due for, "" not due
	}{
		{"off", "", "", at8(28, 6, 0), idleAt, ""},
		{"a minute before", "06:00", "", at8(28, 5, 59), idleAt, ""},
		{"on the minute", "06:00", "", at8(28, 6, 0), idleAt, "2026-09-28 06:00"},
		{"the reset not known", "06:00", "", at8(28, 6, 0), func(time.Time) QuotaWindow { return QuotaWindow{Name: "5 hours", Span: fiveHours} }, "2026-09-28 06:00"},
		{"woken half an hour late", "06:00", "", at8(28, 6, 30), idleAt, "2026-09-28 06:00"},
		{"woken just within the hour", "06:00", "2026-09-27", at8(28, 7, 0), idleAt, "2026-09-28 06:00"},
		{"woken hours late", "06:00", "", at8(28, 7, 1), idleAt, ""},
		{"already started today", "06:00", "2026-09-28", at8(28, 6, 5), idleAt, ""},
		{"started yesterday", "06:00", "2026-09-27", at8(28, 6, 5), idleAt, "2026-09-28 06:00"},
		{"a window running", "06:00", "", at8(28, 6, 0),
			func(now time.Time) QuotaWindow { return win("5 hours", fiveHours, 12, now.Add(2*time.Hour)) }, ""},
		{"a window just started, nothing counted yet", "06:00", "", at8(28, 6, 0),
			func(now time.Time) QuotaWindow { return win("5 hours", fiveHours, 0, now.Add(4*time.Hour)) }, ""},
		{"late in the evening, woken after midnight", "23:30", "", at8(29, 0, 20), idleAt, "2026-09-28 23:30"},
		{"late in the evening, the day before's", "23:30", "2026-09-28", at8(29, 0, 20), idleAt, ""},
	} {
		day, due := dailyDue(times(c.at), c.last, asOf(c.cur(c.now), c.now), c.now)
		if !due {
			day = ""
		}
		if day != c.want {
			t.Errorf("%s: due for %q, want %q", c.name, day, c.want)
		}
	}
	// the local clock's six: 06:00 in UTC+8 is 22:00 the day before in UTC
	now := at8(28, 6, 0)
	if _, due := dailyDue(times("06:00"), "", asOf(idleAt(now), now), now.UTC()); due {
		t.Error("06:00 taken in another zone")
	}
}

func TestHeldForDay(t *testing.T) {
	for _, c := range []struct {
		name string
		at   string
		now  time.Time
		want bool
	}{
		{"off", "", at8(28, 2, 0), false},
		{"would run past the start", "06:00", at8(28, 2, 0), true},
		{"ends right on it", "06:00", at8(28, 1, 0), false},
		{"the evening's, ending in the night", "06:00", at8(28, 21, 0), false},
		{"at the start itself", "06:00", at8(28, 6, 0), false},
	} {
		if got := heldForDay(times(c.at), fiveHours, c.now); got != c.want {
			t.Errorf("%s: held %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDayStartPassed(t *testing.T) {
	for _, c := range []struct {
		name      string
		last, now time.Time
		want      bool
	}{
		{"the minute it comes", at8(28, 5, 59), at8(28, 6, 0), true},
		{"looked at since", at8(28, 6, 0), at8(28, 6, 1), false},
		{"slept through the night", at8(27, 23, 0), at8(28, 7, 30), true},
		{"not yet", at8(28, 4, 0), at8(28, 5, 0), false},
	} {
		if got := dayStartPassed(times("06:00"), c.last, c.now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A day with daylight saving starting: six is still six, the day an
// hour short.
func TestDayStartAcrossDaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	before := time.Date(2026, 3, 7, 6, 0, 0, 0, ny)
	next, _ := nextDayStart(times("06:00"), before)
	if want := time.Date(2026, 3, 8, 6, 0, 0, 0, ny); !next.Equal(want) || next.Sub(before) != 23*time.Hour {
		t.Fatalf("next %v, want %v", next, want)
	}
	now := next.Add(10 * time.Minute)
	if day, due := dailyDue(times("06:00"), "2026-03-07", QuotaWindow{Name: "5 hours", Span: fiveHours}, now); !due || day != "2026-03-08 06:00" {
		t.Fatalf("due %v for %q", due, day)
	}
}

func (f *fakeWarm) runAt(t *testing.T, path, which, at string) []CodexWarm {
	t.Helper()
	return f.warmer(path).warmNow(t.Context(), which, times(at))
}

// The day's start with the reset warm-up off: an account whose 5-hour
// window isn't running is sent one request at six, once, and a restart
// doesn't send it again; one in a window is left be, and the weekly
// windows too.
func TestWarmAtTheDaysStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	f := &fakeWarm{now: at8(28, 5, 0), errs: map[string]error{}}
	idle := func() QuotaWindow { return win("5 hours", fiveHours, 0, f.now.Add(fiveHours)) }
	f.ws = map[string][]QuotaWindow{
		"a@example.com": {idle(), win("7 days", week, 0, f.now.Add(week))},
		"b@example.com": {win("5 hours", fiveHours, 30, at8(28, 8, 0)), win("7 days", week, 40, at8(30, 0, 0))},
	}
	if rs := f.runAt(t, path, "", "06:00"); len(rs) != 0 {
		t.Fatalf("before six: %+v", rs)
	}
	f.now = at8(28, 6, 0)
	f.ws["a@example.com"][0] = idle()
	rs := f.runAt(t, path, "", "06:00")
	if len(rs) != 1 || rs[0].User != "a@example.com" || strings.Join(rs[0].Windows, ",") != "5 hours" {
		t.Fatalf("at six: %+v", rs)
	}
	// the next read, cached from before, still has it unused; then a restart
	for range 3 {
		f.now = f.now.Add(5 * time.Minute)
		f.ws["a@example.com"][0] = idle()
		f.runAt(t, path, "", "06:00")
	}
	if strings.Join(f.sent, ",") != "a@example.com" {
		t.Fatalf("sent %v", f.sent)
	}
	if !codexWarmedIn(path)["a@example.com"].Equal(at8(28, 6, 0)) {
		t.Fatal("not kept")
	}
	// b's window ends at eight, past the hour: left for the day
	f.now = at8(28, 8, 5)
	f.ws["b@example.com"][0] = win("5 hours", fiveHours, 0, f.now.Add(fiveHours))
	f.runAt(t, path, "", "06:00")
	// the next day, both
	f.sent = nil
	f.now = at8(29, 6, 1)
	f.ws["a@example.com"][0] = idle()
	f.ws["b@example.com"][0] = idle()
	f.runAt(t, path, "", "06:00")
	if strings.Join(f.sent, ",") != "a@example.com,b@example.com" {
		t.Fatalf("the next day sent %v", f.sent)
	}
}

// A failed start is tried again while the hour lasts, and kept once it goes.
func TestWarmAtRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	f := &fakeWarm{now: at8(28, 6, 0), errs: map[string]error{"a@example.com": errors.New("503")}}
	idle := func() QuotaWindow { return win("5 hours", fiveHours, 0, f.now.Add(fiveHours)) }
	f.ws = map[string][]QuotaWindow{"a@example.com": {idle()}}
	if rs := f.runAt(t, path, "", "06:00"); len(rs) != 1 || rs[0].Err == "" {
		t.Fatalf("failed: %+v", rs)
	}
	f.now = at8(28, 6, 5)
	f.ws["a@example.com"][0] = idle()
	f.errs = map[string]error{}
	f.runAt(t, path, "", "06:00")
	f.now = at8(28, 6, 10)
	f.ws["a@example.com"][0] = idle()
	f.runAt(t, path, "", "06:00")
	if len(f.sent) != 2 {
		t.Fatalf("sent %v", f.sent)
	}
	if !codexWarmedIn(path)["a@example.com"].Equal(at8(28, 6, 5)) {
		t.Fatal("not kept")
	}
}

// With the 5-hour warm-up on too, the windows follow one another from six:
// the one ending at one isn't started to run past six, and six starts the
// day's first with one request.
func TestWarmAtWithTheResetWarmUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	f := &fakeWarm{now: at8(28, 0, 30), errs: map[string]error{}}
	f.ws = map[string][]QuotaWindow{"a@example.com": {win("5 hours", fiveHours, 40, at8(28, 1, 0))}}
	f.runAt(t, path, "all", "06:00")
	// it resets at one: held
	for _, hm := range [][2]int{{1, 5}, {3, 0}, {5, 55}} {
		f.now = at8(28, hm[0], hm[1])
		f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, f.now.Add(fiveHours))
		if rs := f.runAt(t, path, "all", "06:00"); len(rs) != 0 {
			t.Fatalf("%v: %+v", f.now, rs)
		}
	}
	f.now = at8(28, 6, 0)
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, f.now.Add(fiveHours))
	f.runAt(t, path, "all", "06:00")
	f.now = at8(28, 6, 5)
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, at8(28, 11, 0))
	f.runAt(t, path, "all", "06:00")
	if len(f.sent) != 1 {
		t.Fatalf("sent %v", f.sent)
	}
	// from then on each reset starts the next: eleven, four, nine at night
	for _, h := range []int{11, 16, 21} {
		f.now = at8(28, h, 1)
		f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, f.now.Add(fiveHours))
		if rs := f.runAt(t, path, "all", "06:00"); len(rs) != 1 {
			t.Fatalf("%d:01: %+v", h, rs)
		}
		f.now = at8(28, h, 6)
		f.ws["a@example.com"][0] = win("5 hours", fiveHours, 1, at8(28, h, 1).Add(fiveHours))
		f.runAt(t, path, "all", "06:00")
	}
	if len(f.sent) != 4 {
		t.Fatalf("sent %d times", len(f.sent))
	}
}

// A weekly window resetting while the 5-hour one isn't running, less than
// five hours before the daily time, waits for it: its request would start
// the 5-hour window too, to run past the time (#246). With no daily time,
// or the 5-hour window running, it goes at once.
func TestWeeklyWarmUpWaitsForTheDay(t *testing.T) {
	for _, which := range []string{"week", "all"} {
		path := filepath.Join(t.TempDir(), "warmup.json")
		f := &fakeWarm{now: at8(29, 8, 32), errs: map[string]error{}}
		idleWindows := func() []QuotaWindow {
			return []QuotaWindow{win("5 hours", fiveHours, 0, f.now.Add(fiveHours)), win("7 days", week, 0, f.now.Add(week))}
		}
		f.ws = map[string][]QuotaWindow{"a@example.com": idleWindows()}
		if rs := f.runAt(t, path, which, "09:30"); len(rs) != 0 {
			t.Errorf("%s: at 08:32 sent %+v, starting the 5-hour window before 09:30", which, rs)
		}
		f.now = at8(29, 9, 30)
		f.ws["a@example.com"] = idleWindows()
		if rs := f.runAt(t, path, which, "09:30"); len(rs) != 1 || len(rs[0].Windows) != 2 {
			t.Errorf("%s: at 09:30 sent %+v, want one request for both windows", which, rs)
		}

		// no daily time: at once
		f = &fakeWarm{now: at8(29, 8, 32), errs: map[string]error{}}
		f.ws = map[string][]QuotaWindow{"a@example.com": idleWindows()}
		if rs := f.runAt(t, filepath.Join(t.TempDir(), "warmup.json"), which, ""); len(rs) != 1 {
			t.Errorf("%s: no daily time, sent %+v, want the weekly warm-up at once", which, rs)
		}

		// the 5-hour window running: the weekly one at once
		f = &fakeWarm{now: at8(29, 8, 32), errs: map[string]error{}}
		f.ws = map[string][]QuotaWindow{"a@example.com": {win("5 hours", fiveHours, 30, f.now.Add(2*time.Hour)), win("7 days", week, 0, f.now.Add(week))}}
		if rs := f.runAt(t, filepath.Join(t.TempDir(), "warmup.json"), which, "09:30"); len(rs) != 1 || len(rs[0].Windows) != 1 || rs[0].Windows[0] != "7 days" {
			t.Errorf("%s: 5-hour window running, sent %+v, want the weekly warm-up at once", which, rs)
		}
	}
}

// times is at as warmNow takes it, none for "".
func times(at string) []string {
	if at == "" {
		return nil
	}
	return []string{at}
}
