package provider

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A Claude account's windows as its usage endpoint tells them: an unused
// one with no reset, a model's weekly one beside the account's.
func TestClaudeWarmStartsTheAccountsWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-warmup.json")
	now := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	f := &fakeWarm{now: now, errs: map[string]error{}}
	f.ws = map[string][]QuotaWindow{
		"a@example.com": {
			{Name: "5 hours", Span: fiveHours},
			win("7 days", week, 20, now.Add(2*24*time.Hour)),
			{Name: "7 days · Opus", Span: week, Model: "opus"},
		},
	}
	// weekly only: the 5-hour window is left, the running week noted
	if rs := f.run(t, path, "week"); len(rs) != 0 {
		t.Fatalf("week: %+v", rs)
	}
	rs := f.run(t, path, "all")
	if len(rs) != 1 || strings.Join(rs[0].Windows, ",") != "5 hours" {
		t.Fatalf("all: %+v", rs)
	}
	if !codexWarmedIn(path)["a@example.com"].Equal(now) {
		t.Fatal("not kept")
	}
}

// #178: an account never used has its windows not started, no reset
// known, and routing leaves it for last; warm-ups that didn't start them,
// given up on or not, are sent again, each a while after the last.
func TestReproIdleWindowNeverWarmedAgain(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	idle := SubscriptionQuota{Windows: []QuotaWindow{{Name: "5 hours", Span: 5 * time.Hour}, {Name: "7 days", Span: 7 * 24 * time.Hour}}}
	fail, sent := true, 0
	var warmed []string
	w := codexWarmer{path: filepath.Join(t.TempDir(), "w.json"), now: func() time.Time { return now },
		usage: func(context.Context) map[string]SubscriptionQuota { return map[string]SubscriptionQuota{"a@x": idle} },
		send: func(context.Context, string) error {
			sent++
			if fail {
				return errors.New("boom")
			}
			return nil
		},
		expect: claudeWindowsExpected, warmed: func(user string) { warmed = append(warmed, user) }}
	for i := 0; i < 3; i++ { // three failures: given up
		w.warmNow(context.Background(), "all", "")
		now = now.Add(5 * time.Minute)
	}
	if sent != 3 || len(warmed) != 0 {
		t.Fatalf("sent %d, warmed %v", sent, warmed)
	}
	fail = false
	before := sent
	for i := 0; i < 10; i++ { // the account is still idle, and sending works now
		w.warmNow(context.Background(), "all", "")
		now = now.Add(5 * time.Minute)
	}
	// 15 minutes after giving up, then 30 after that: not every round
	if sent-before != 2 {
		t.Errorf("idle, never-started account warmed %d times in 50 minutes, want 2", sent-before)
	}
	if strings.Join(warmed, ",") != "a@x,a@x" {
		t.Errorf("warmed told %v", warmed)
	}
	// its windows start: nothing more
	idle = SubscriptionQuota{Windows: []QuotaWindow{win("5 hours", fiveHours, 1, now.Add(fiveHours)), win("7 days", week, 1, now.Add(week))}}
	before = sent
	for i := 0; i < 20; i++ {
		w.warmNow(context.Background(), "all", "")
		now = now.Add(5 * time.Minute)
	}
	if sent != before {
		t.Errorf("a running window warmed %d times", sent-before)
	}

	sent = 0
	w2 := w
	w2.path = filepath.Join(t.TempDir(), "w2.json")
	w2.usage = func(context.Context) map[string]SubscriptionQuota {
		return map[string]SubscriptionQuota{"b@x": {Windows: []QuotaWindow{}}}
	}
	if rs := w2.warmNow(context.Background(), "all", ""); sent == 0 || len(rs) != 1 || strings.Join(rs[0].Windows, ",") != "5 hours,7 days" {
		t.Errorf("windows absent from the usage reply: %+v", rs)
	}
	// weekly only: the week; and none but those an account is known to have
	w2.path = filepath.Join(t.TempDir(), "w3.json")
	if rs := w2.warmNow(context.Background(), "week", ""); len(rs) != 1 || strings.Join(rs[0].Windows, ",") != "7 days" {
		t.Errorf("weekly: %+v", rs)
	}
	w2.path, w2.expect = filepath.Join(t.TempDir(), "w4.json"), nil
	if rs := w2.warmNow(context.Background(), "all", ""); len(rs) != 0 {
		t.Errorf("no windows expected: %+v", rs)
	}
}

// A window a warm-up leaves not started waits twice as long each time for
// the next, up to warmRetryMax.
func TestWarmRetryBacksOff(t *testing.T) {
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	now := start
	var at []string
	w := codexWarmer{path: filepath.Join(t.TempDir(), "w.json"), now: func() time.Time { return now },
		usage: func(context.Context) map[string]SubscriptionQuota {
			return map[string]SubscriptionQuota{"a@x": {Windows: []QuotaWindow{{Name: "7 days", Span: week}}}}
		},
		send: func(context.Context, string) error { at = append(at, now.Sub(start).String()); return nil }}
	for now.Sub(start) < 16*time.Hour {
		w.warmNow(context.Background(), "week", "")
		now = now.Add(5 * time.Minute)
	}
	if want := "0s,15m0s,45m0s,1h45m0s,3h45m0s,7h45m0s,11h45m0s,15h45m0s"; strings.Join(at, ",") != want {
		t.Fatalf("sent at %v, want %s", at, want)
	}
}

// A usage read finding an account's window not started has the warm-up
// look at once, not at its next round, and each account at most every
// warmKickEvery; with the warm-up off, nothing.
func TestUsageReadKicksTheWarmUp(t *testing.T) {
	var mu sync.Mutex
	which := "all"
	prefs := func() (string, string) { mu.Lock(); defer mu.Unlock(); return which, "" }
	sent := make(chan string, 8)
	usage := map[string]SubscriptionQuota{"a@x": {Windows: []QuotaWindow{}}}
	w := codexWarmer{path: filepath.Join(t.TempDir(), "w.json"), now: time.Now,
		usage:  func(context.Context) map[string]SubscriptionQuota { return usage },
		send:   func(_ context.Context, user string) error { sent <- user; return nil },
		expect: claudeWindowsExpected}
	hooked := func() bool {
		notStartedHooks.Lock()
		defer notStartedHooks.Unlock()
		_, ok := notStartedHooks.m["claude-test"]
		return ok
	}
	read := func(u map[string]SubscriptionQuota) {
		for i := 0; i < 200 && !hooked(); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		usageRead("claude-test", u)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { keepWarm(ctx, "claude-test", w, prefs); close(done) }()
	// a running account is left alone
	now := time.Now()
	read(map[string]SubscriptionQuota{"b@x": {Windows: []QuotaWindow{win("5 hours", fiveHours, 3, now.Add(time.Hour)), win("7 days", week, 3, now.Add(week))}}})
	read(usage)
	select {
	case u := <-sent:
		if u != "a@x" {
			t.Fatalf("sent %s", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not warmed on the read")
	}
	read(usage) // again within warmKickEvery
	select {
	case u := <-sent:
		t.Fatalf("sent %s again", u)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	<-done
	if hooked() {
		t.Fatal("hook left behind")
	}

	// off: a read kicks nothing
	mu.Lock()
	which = ""
	mu.Unlock()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	w.path = filepath.Join(t.TempDir(), "w2.json")
	go keepWarm(ctx, "claude-test", w, prefs)
	read(usage)
	select {
	case u := <-sent:
		t.Fatalf("off, sent %s", u)
	case <-time.After(200 * time.Millisecond):
	}
}
