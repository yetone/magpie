package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// stubCheckin stands in for the WorkBuddy accounts signed in here: rs is
// what checking them in comes to, none signed in when rs is nil.
func stubCheckin(t *testing.T, rs []provider.WorkBuddyCheckin) *int {
	t.Helper()
	stubTrae(t, nil)
	oldHere, oldHas := checkinHere, hasWorkBuddy
	t.Cleanup(func() { checkinHere, hasWorkBuddy = oldHere, oldHas })
	calls := 0
	hasWorkBuddy = func() bool { return rs != nil }
	checkinHere = func(context.Context) []provider.WorkBuddyCheckin {
		calls++
		return rs
	}
	return &calls
}

// stubTrae stands in for the Trae CN accounts, as stubCheckin does
// WorkBuddy's.
func stubTrae(t *testing.T, rs []provider.WorkBuddyCheckin) *int {
	t.Helper()
	oldHere, oldHas := checkinTrae, hasTrae
	t.Cleanup(func() { checkinTrae, hasTrae = oldHere, oldHas })
	calls := 0
	hasTrae = func() bool { return rs != nil }
	checkinTrae = func(context.Context) []provider.WorkBuddyCheckin {
		calls++
		return rs
	}
	return &calls
}

// c on the Usage page presses WorkBuddy's daily check-in (签到) for every
// WorkBuddy (China) account signed in here at once, as the app's "Check in now" and magpie accounts
// checkin do, and says how each stands: the credits and streak, in already,
// or why not (akic404 on Discord: the TUI had no way to check in).
func TestTUIChecksWorkBuddyIn(t *testing.T) {
	home(t)
	usagePage := model{w: 200, h: 40, page: pageUsage}

	// nothing signed in: said so, nothing asked
	calls := stubCheckin(t, nil)
	m := press(t, usagePage, "c")
	wantFlash(t, m, false, "no WorkBuddy (China) or Trae CN account is signed in")
	if *calls != 0 {
		t.Fatal("checked in with no account")
	}

	calls = stubCheckin(t, []provider.WorkBuddyCheckin{{User: "旅行者", Outcome: provider.CheckinClaimed, Credit: 100, Streak: 4, Asked: true}})
	m = press(t, usagePage, "c")
	wantFlash(t, m, true, "旅行者 checked in today +100 · a 4-day streak")
	if strings.Contains(m.flash, "already") || *calls != 1 {
		t.Fatalf("claimed now: %q, %d calls", m.flash, *calls)
	}
	if !strings.Contains(m.View(), "daily check-in") {
		t.Fatalf("the footer doesn't name c:\n%s", m.View())
	}

	// one in already today, one the vendor refused: the error, in red
	stubCheckin(t, []provider.WorkBuddyCheckin{
		{User: "旅行者", Outcome: provider.CheckinDone, Credit: 100, Streak: 4},
		{User: "second", Outcome: provider.CheckinFailed, Msg: "401: token expired"},
	})
	m = press(t, usagePage, "c")
	wantFlash(t, m, false, "旅行者 checked in today +100 · a 4-day streak · already; second couldn't check in: 401: token expired")

	stubCheckin(t, []provider.WorkBuddyCheckin{{User: "a", Outcome: provider.CheckinIneligible}, {User: "b", Outcome: provider.CheckinInactive}})
	m = press(t, usagePage, "c")
	wantFlash(t, m, true, "a isn't eligible for the check-in; b: no check-in event now")
}

// c checks the Trae CN accounts in too, after WorkBuddy's, each said as
// Trae CN's (#694); with only Trae CN signed in, it is checked in alone.
func TestTUIChecksTraeIn(t *testing.T) {
	home(t)
	usagePage := model{w: 200, h: 40, page: pageUsage}

	wb := stubCheckin(t, []provider.WorkBuddyCheckin{{User: "旅行者", Outcome: provider.CheckinDone, Credit: 100, Streak: 4}})
	tr := stubTrae(t, []provider.WorkBuddyCheckin{{User: "hu", By: "trae", Outcome: provider.CheckinClaimed, Credit: 100, Asked: true}})
	m := press(t, usagePage, "c")
	wantFlash(t, m, true, "旅行者 checked in today +100 · a 4-day streak · already; Trae CN hu checked in today +100")
	if *wb != 1 || *tr != 1 {
		t.Fatalf("calls: workbuddy %d, trae %d", *wb, *tr)
	}

	stubCheckin(t, nil)
	stubTrae(t, []provider.WorkBuddyCheckin{{User: "hu", By: "trae", Outcome: provider.CheckinIneligible, Msg: "device checked in"}})
	m = press(t, usagePage, "c")
	wantFlash(t, m, true, "Trae CN hu isn't eligible for the check-in")
}

// A WorkBuddy (China) account's line on the Usage page says how today's
// check-in went, as its card in the app does, and points at c while it
// isn't in.
func TestCheckinOnTheUsageLine(t *testing.T) {
	now := time.Now()
	today := provider.CheckinDay(now)
	win := []provider.QuotaWindow{{Name: "Month", Used: 10}}
	qs := []provider.SubscriptionQuota{
		{Name: "WorkBuddy", User: "旅行者", Checkins: true, Windows: win, Checkin: &provider.WorkBuddyCheckin{Day: today, Outcome: provider.CheckinClaimed, Credit: 100, Streak: 4}},
		{Name: "WorkBuddy", User: "second", Checkins: true, Windows: win, Checkin: &provider.WorkBuddyCheckin{Day: "2020-01-01", Outcome: provider.CheckinClaimed}},
		{Name: "WorkBuddy", User: "third", Checkins: true, Error: "sign-in has expired", Checkin: &provider.WorkBuddyCheckin{Day: today, Outcome: provider.CheckinFailed}},
		{Name: "Codex", User: "me@example.com", Windows: win},
	}
	got := strings.Join(quotaLines(qs, true, false, 200, now), "\n")
	for _, want := range []string{"签到 ✓ +100 · 4-day streak", "second", "签到 not yet today · c", "sign-in has expired   签到 failed · c tries again"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "Codex") && strings.Contains(l, "签到") {
			t.Errorf("a check-in on a Codex line: %s", l)
		}
	}
}
