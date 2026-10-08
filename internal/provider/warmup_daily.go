package provider

// Starting the 5-hour windows at times of day (settings' CodexWarmAts and
// ClaudeWarmAts, one or several, #1260), not only as the last one resets.
// A 5-hour window starts at an account's first request, so a day that
// begins at 9 fits two before the evening; one started at 6, while the
// user is asleep, runs out at 11, and the day fits three. Each day at each
// of those times, each account whose 5-hour window isn't running is sent
// the warm-up's one tiny request.
//
// Once a day for each time, an account: the start (day and time) it was
// last started for is kept with its windows in the warm-up's file, so a
// restart doesn't send it again. A machine asleep at the time sends it on
// waking, up to dailySlack late; one asleep longer than that leaves it be,
// a window started hours late being the one the user didn't want.
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
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// dailySlack is how late a day's start is still sent: a machine that
// wakes within it sends it then.
const dailySlack = time.Hour

// dayStart is the latest start of any of ats (times of day, "06:00") at
// or before now, in now's own zone; false when ats has none.
func dayStart(ats []string, now time.Time) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, at := range ats {
		h, m, ok := settings.Clock(at)
		if at == "" || !ok {
			continue
		}
		y, mo, d := now.Date()
		t := time.Date(y, mo, d, h, m, 0, 0, now.Location())
		if t.After(now) {
			t = time.Date(y, mo, d-1, h, m, 0, 0, now.Location())
		}
		if !found || t.After(latest) {
			latest, found = t, true
		}
	}
	return latest, found
}

// nextDayStart is the first start of any of ats after now; false when ats
// has none.
func nextDayStart(ats []string, now time.Time) (time.Time, bool) {
	var first time.Time
	found := false
	for _, at := range ats {
		t, ok := dayStart([]string{at}, now)
		if !ok {
			continue
		}
		y, mo, d := t.Date()
		h, m, _ := t.Clock()
		if t = time.Date(y, mo, d+1, h, m, 0, 0, now.Location()); !found || t.Before(first) {
			first, found = t, true
		}
	}
	return first, found
}

// startKey names a day's start as warmWindow.Daily keeps it: the day and
// the time, "2006-01-02 15:04", so each of a day's times is had once.
func startKey(t time.Time) string { return t.Format("2006-01-02 15:04") }

// hadStart says whether last, the start a window was last started for,
// is key. A magpie before several times kept the day alone
// ("2006-01-02"): that day's starts all count as had, so the day it is
// updated on sends nothing twice.
func hadStart(last, key string) bool {
	return last == key || len(last) == len(time.DateOnly) && strings.HasPrefix(key, last+" ")
}

// dailyDue says whether a 5-hour window, as cur (as of now) and last
// started for the start last (startKey, "" never), wants starting for the
// latest of the day's starts ats: that start has passed by no more than
// dailySlack, the window hasn't had it, and the window isn't running.
// day is the start to keep once it is sent.
func dailyDue(ats []string, last string, cur QuotaWindow, now time.Time) (day string, due bool) {
	t, ok := dayStart(ats, now)
	if !ok || now.Sub(t) > dailySlack {
		return "", false
	}
	day = startKey(t)
	return day, !hadStart(last, day) && idle(cur, now)
}

// heldForDay says whether a 5-hour window span long, due on its reset now,
// is to wait for the next of the day's starts ats instead: started now, it
// would still be running then.
func heldForDay(ats []string, span time.Duration, now time.Time) bool {
	next, ok := nextDayStart(ats, now)
	return ok && next.Sub(now) < span
}

// dayStartPassed says whether one of the starts ats fell after last and
// by now, for the loop to look at the windows then rather than wait its
// turn.
func dayStartPassed(ats []string, last, now time.Time) bool {
	t, ok := dayStart(ats, now)
	return ok && t.After(last)
}
