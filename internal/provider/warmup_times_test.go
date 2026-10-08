package provider

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #1260 (Sinnhu): several times of day, each starting the 5-hour window
// once that day where it isn't running. A day read every five minutes,
// the read cached a while after each request (still showing the window
// unused): 06:00, 11:05 and 16:10 send one each, and 19:10, inside the
// window 16:10 started, none. The next day the same again.
func TestWarmAtSeveralTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	ats := []string{"06:00", "11:05", "16:10", "19:10"}
	f := &fakeWarm{errs: map[string]error{}}
	var started time.Time // the running window's start, as the backend sees it
	day := func(d int) []string {
		t.Helper()
		f.sent = nil
		var at []string
		for now := at8(d, 0, 0); now.Before(at8(d+1, 0, 0)); now = now.Add(5 * time.Minute) {
			f.now = now
			w := win("5 hours", fiveHours, 0, now.Add(fiveHours))
			// the read lags the request by ten minutes
			if !started.IsZero() && now.Sub(started) >= 10*time.Minute && now.Before(started.Add(fiveHours)) {
				w = win("5 hours", fiveHours, 3, started.Add(fiveHours))
			}
			f.ws = map[string][]QuotaWindow{"a@example.com": {w}}
			before := len(f.sent)
			f.warmer(path).warmNow(t.Context(), "", ats)
			if len(f.sent) > before {
				started = now
				at = append(at, now.Format("15:04"))
			}
		}
		return at
	}
	if got := strings.Join(day(28), ","); got != "06:00,11:05,16:10" {
		t.Fatalf("sent at %s", got)
	}
	if got := strings.Join(day(29), ","); got != "06:00,11:05,16:10" {
		t.Fatalf("the next day sent at %s", got)
	}
}

// A warm-up file from a magpie before several times keeps the day alone:
// the day it is updated on, a time already had isn't sent again, and the
// next day's are.
func TestWarmAtKeptDayFromBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	if err := writePrivate(path, []byte(`{"a@example.com":{"5 hours":{"used":0,"daily":"2026-09-28"}}}`)); err != nil {
		t.Fatal(err)
	}
	f := &fakeWarm{now: at8(28, 6, 5), errs: map[string]error{}}
	f.ws = map[string][]QuotaWindow{"a@example.com": {win("5 hours", fiveHours, 0, f.now.Add(fiveHours))}}
	if f.runAt(t, path, "", "06:00"); len(f.sent) != 0 {
		t.Fatalf("sent again: %v", f.sent)
	}
	f.now = at8(29, 6, 0)
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, f.now.Add(fiveHours))
	if f.runAt(t, path, "", "06:00"); len(f.sent) != 1 {
		t.Fatalf("the next day: %v", f.sent)
	}
}

// The loop looks at the windows as each of the times comes, and a window
// due on its reset waits for the next of them it would run past.
func TestWarmLookAndHoldAtSeveralTimes(t *testing.T) {
	ats := []string{"06:00", "15:05"}
	if !warmLook("", ats, nil, false, at8(28, 15, 4), at8(28, 15, 5)) {
		t.Error("15:05 not looked at")
	}
	if warmLook("", ats, nil, false, at8(28, 15, 5), at8(28, 15, 6)) {
		t.Error("looked at again a minute after")
	}
	if !heldForDay(ats, fiveHours, at8(28, 11, 0)) || heldForDay(ats, fiveHours, at8(28, 10, 0)) {
		t.Error("the reset at eleven is held for 15:05, the one at ten not")
	}
}
