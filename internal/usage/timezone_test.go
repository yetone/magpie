package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func zoneSet(t *testing.T, name string) {
	t.Helper()
	s := settings.Load()
	s.TimeZone = name
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// A magpie set to a time zone of its own (JackyFill on X: a magpie on a
// server and one on the desk adding up the same days) cuts its days there,
// whatever zone the browser names and whatever zone the computer keeps. A
// call at 03:00 UTC on Sep 30 is Sep 29's in Los Angeles and Sep 30's in
// Shanghai, in UTC and in Kiritimati; at noon UTC it is Sep 30 in Los
// Angeles and in UTC, so a page with no zone asked for before the change
// has the same date and must not be served again. Every clock is held;
// run under TZ=UTC and TZ=Pacific/Kiritimati.
func TestTimeZoneSettingCutsTheDays(t *testing.T) {
	pageHome(t)
	if _, err := time.LoadLocation("America/Los_Angeles"); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { zoneSet(t, "") })
	call := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	holdClock(t, call)
	Append(Record{Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200})
	holdClock(t, call.Add(9*time.Hour)) // 05:00 on Sep 30 in Los Angeles

	days := func(f Filter) map[string]int {
		out := map[string]int{}
		for _, d := range HeatmapOf(f).Days {
			out[d.Date] = d.Calls
		}
		return out
	}
	run := func(set, want string) {
		zoneSet(t, set)
		for _, browser := range []string{"Asia/Shanghai", ""} {
			v := Zone(browser)
			if got := days(Filter{Zone: v}); len(got) != 1 || got[want] != 1 {
				t.Errorf("set %q, browser %q: heatmap days %v, want %s", set, browser, got, want)
			}
			if got := QueryPage(All, Filter{Zone: v, Day: want}, 0, 10).Total; got != 1 {
				t.Errorf("set %q, browser %q: day %s has %d calls, want 1", set, browser, want, got)
			}
			wantToday := 0 // Sep 29's in Los Angeles
			if want == "2026-09-30" {
				wantToday = 1
			}
			if got := SummarizeTopIn(Today, 10, v).Calls; got != wantToday {
				t.Errorf("set %q, browser %q: Today has %d calls, want %d", set, browser, got, wantToday)
			}
		}
		// a page asked for with no zone (the tray, the TUI, the CLI) is
		// in the set one too, and not served from before the change
		if got := QueryPage(All, Filter{Day: want}, 0, 10).Total; got != 1 {
			t.Errorf("set %q: day %s with no zone has %d calls, want 1", set, want, got)
		}
		if got := days(Filter{}); len(got) != 1 || got[want] != 1 {
			t.Errorf("set %q: heatmap with no zone %v, want %s", set, got, want)
		}
	}
	// the computer's days before (UTC's or Kiritimati's): Sep 29 empty
	QueryPage(All, Filter{Day: "2026-09-29"}, 0, 10)
	QueryPage(All, Filter{Day: "2026-09-30"}, 0, 10)
	days(Filter{})
	run("America/Los_Angeles", "2026-09-29")
	run("Asia/Shanghai", "2026-09-30")
	run("America/Los_Angeles", "2026-09-29")
}

// An empty zone follows the system, and only a name the zone database
// knows is taken.
func TestTimeZoneSettingIsAZoneName(t *testing.T) {
	pageHome(t)
	t.Cleanup(func() { zoneSet(t, "") })
	if Zone("") != time.Local {
		t.Fatalf("unset: Zone(\"\") = %v, want Local", Zone(""))
	}
	for _, bad := range []string{"--help", "Mars/Olympus", "UTC+8", "../etc/passwd", "Local"} {
		s := settings.Load()
		s.TimeZone = bad
		if err := settings.Save(s); err == nil {
			t.Errorf("%q was taken as a time zone", bad)
		}
	}
	zoneSet(t, "Asia/Kolkata")
	if got := Zone("").String(); got != "Asia/Kolkata" {
		t.Errorf("set: Zone(\"\") = %s, want Asia/Kolkata", got)
	}
	zoneSet(t, "")
	if Zone("Asia/Kolkata").String() != "Asia/Kolkata" || Zone("") != time.Local {
		t.Errorf("unset again: the browser's zone and the computer's are back")
	}
}
