package provider

// Starting the 5-hour windows at a time of day (settings' CodexWarmAt and
// ClaudeWarmAt), not only as the last one resets. A 5-hour window starts at
// an account's first request, so a day that begins at 9 fits two before the
// evening; one started at 6, while the user is asleep, runs out at 11, and
// the day fits three. Each day at that time, each account whose 5-hour
// window isn't running is sent the warm-up's one tiny request.
//
// Once a day an account: the day it was started for is kept with its
// windows in the warm-up's file, so a restart doesn't send it again. A
// machine asleep at the time sends it on waking, up to dailySlack late; one
// asleep longer than that leaves the day be, a window started hours late
// being the one the user didn't want.
//
// With the 5-hour warm-up on too ("all"), the windows follow one another
// from that time: a window that would still be running at it isn't started
// on its reset (the one ending at 2 is left, not started to run past 6),
// and the time starts the day's first.
//
// A weekly window's warm-up waits for the time as well while the 5-hour
// window isn't running and, started then, would run past it: the one
// request starts every window not running, the 5-hour one with it.
// Started with the day's first, the weekly window resets at the time from
// then on, clear of the wait.

import (
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// dailySlack is how late a day's start is still sent: a machine that
// wakes within it sends it then.
const dailySlack = time.Hour

// dayStart is the latest start at (a time of day, "06:00") at or before
// now, in now's own zone; false when at is none.
func dayStart(at string, now time.Time) (time.Time, bool) {
	h, m, ok := settings.Clock(at)
	if at == "" || !ok {
		return time.Time{}, false
	}
	y, mo, d := now.Date()
	t := time.Date(y, mo, d, h, m, 0, 0, now.Location())
	if t.After(now) {
		t = time.Date(y, mo, d-1, h, m, 0, 0, now.Location())
	}
	return t, true
}

// nextDayStart is the first start at after now; false when at is none.
func nextDayStart(at string, now time.Time) (time.Time, bool) {
	t, ok := dayStart(at, now)
	if !ok {
		return t, false
	}
	y, mo, d := t.Date()
	h, m, _ := t.Clock()
	return time.Date(y, mo, d+1, h, m, 0, 0, now.Location()), true
}

// dailyDue says whether a 5-hour window, as cur (as of now) and last
// started for the day last ("2006-01-02", "" never), wants starting for
// the day's start at: that start has passed by no more than dailySlack,
// the day hasn't had it, and the window isn't running. day is the day to
// keep once it is sent.
func dailyDue(at, last string, cur QuotaWindow, now time.Time) (day string, due bool) {
	t, ok := dayStart(at, now)
	if !ok || now.Sub(t) > dailySlack {
		return "", false
	}
	day = t.Format(time.DateOnly)
	return day, day != last && idle(cur, now)
}

// heldForDay says whether a 5-hour window span long, due on its reset now,
// is to wait for the day's start at instead: started now, it would still
// be running then.
func heldForDay(at string, span time.Duration, now time.Time) bool {
	next, ok := nextDayStart(at, now)
	return ok && next.Sub(now) < span
}

// dayStartPassed says whether a start at fell after last and by now, for
// the loop to look at the windows then rather than wait its turn.
func dayStartPassed(at string, last, now time.Time) bool {
	t, ok := dayStart(at, now)
	return ok && t.After(last)
}
