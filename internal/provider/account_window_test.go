package provider

import (
	"sync/atomic"
	"testing"
	"time"
)

// A key's limit that follows an account's window (ljayic on X) finds it
// in the account's last good reading, read again from disk after a
// restart: when it began and when it resets. Past its reset, or with no
// reading, it is unknown, never a window guessed, and the accounts are
// asked to be read again without waiting.
func TestAccountWindowFromLastReading(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	restart()
	defer restart()
	asked := 0
	was := readAccountWindows
	readAccountWindows = func(time.Time) { asked++ }
	defer func() { readAccountWindows = was }()

	now := time.Now().Round(time.Second)
	reset := now.Add(2 * time.Hour)
	week := now.Add(3 * 24 * time.Hour)
	keepLast(SubscriptionQuota{Provider: "codex", Name: "Codex", User: "Me@example.com", Windows: []QuotaWindow{
		{Name: "5h", Used: 40, ResetsAt: &reset, Span: 5 * time.Hour},
		{Name: "Weekly", Used: 10, ResetSecs: int64(week.Sub(now) / time.Second)},
		{Name: "Credits", Display: "12", Text: true},
	}}, "")
	restart() // from disk, as after a restart: the span isn't in the window's json

	start, end, known := AccountWindow("codex/me@example.com", "5H", now)
	if !known || !end.Equal(reset) || !start.Equal(reset.Add(-5*time.Hour)) {
		t.Fatalf("5h: %v – %v known %v", start, end, known)
	}
	start, end, known = AccountWindow("codex/me@example.com", "weekly", now)
	if !known || end.Sub(week).Abs() > 2*time.Second || end.Sub(start) != 7*24*time.Hour {
		t.Fatalf("weekly: %v – %v known %v", start, end, known)
	}
	if asked != 0 {
		t.Fatalf("read again with every window known: %d", asked)
	}

	// past the reset with no reading since: unknown, from the reset that
	// passed, which no later window begins before
	start, end, known = AccountWindow("codex/me@example.com", "5h", reset.Add(time.Minute))
	if known || !end.IsZero() || !start.Equal(reset) {
		t.Fatalf("over: %v – %v known %v", start, end, known)
	}
	// a window with no clock, an account with no reading, a window it hasn't
	for _, c := range [][2]string{{"codex/me@example.com", "Credits"}, {"codex/other@example.com", "5h"}, {"codex/me@example.com", "Monthly"}, {"codex", "5h"}} {
		if start, end, known := AccountWindow(c[0], c[1], now); known || !start.IsZero() || !end.IsZero() {
			t.Fatalf("%v: %v – %v known %v", c, start, end, known)
		}
	}
	if asked == 0 {
		t.Fatal("an unknown window wasn't read again")
	}
}

// readAccountWindows reads the accounts again at most once a minute,
// however many requests find a window unknown.
func TestReadAccountWindowsOnceAMinute(t *testing.T) {
	var reads atomic.Int32
	was := readAccounts
	readAccounts = func() { reads.Add(1) }
	defer func() { readAccounts = was }()
	accountWindowsAsked.Store(0)
	now := time.Now()
	for range 5 {
		readAccountWindows(now)
	}
	readAccountWindows(now.Add(30 * time.Second))
	readAccountWindows(now.Add(61 * time.Second))
	deadline := time.Now().Add(10 * time.Second)
	for reads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d reads, want 2", n)
	}
}
