package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fakeWait runs quotaWait on a clock that moves only when it sleeps, and
// reads what at says for the time it is.
type fakeWait struct {
	now      time.Time
	slept    []time.Duration
	reads    int
	said     []string
	at       func(now time.Time) []provider.SubscriptionQuota
	cancel   func() // called on the nth sleep, when set
	cancelAt int
}

func (f *fakeWait) wait() quotaWait {
	return quotaWait{
		read: func(context.Context) []provider.SubscriptionQuota { f.reads++; return f.at(f.now) },
		now:  func() time.Time { return f.now },
		sleep: func(ctx context.Context, d time.Duration) error {
			f.slept = append(f.slept, d)
			if f.cancel != nil && len(f.slept) == f.cancelAt {
				f.cancel()
				return ctx.Err()
			}
			f.now = f.now.Add(d)
			return nil
		},
		say: func(s string) { f.said = append(f.said, s) },
	}
}

func codexAt(user string, used float64, reset time.Time) provider.SubscriptionQuota {
	r := reset
	return provider.SubscriptionQuota{Provider: "codex", Name: "Codex", User: user, Windows: []provider.QuotaWindow{
		{Name: "5 hours", Used: 20, ResetsAt: &r},
		{Name: "weekly", Used: used, ResetsAt: &r},
		// on-demand spending used up stops nothing
		{Name: "credits", Used: 100, Aside: true},
	}}
}

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// An account with room: back at once, nothing slept.
func TestQuotaWaitAlreadyAvailable(t *testing.T) {
	f := &fakeWait{now: t0, at: func(time.Time) []provider.SubscriptionQuota {
		return []provider.SubscriptionQuota{codexAt("a@x", 100, t0.Add(time.Hour)), codexAt("b@x", 97, t0.Add(time.Hour))}
	}}
	q, err := f.wait().run(context.Background(), quotaTarget{provider: "codex", label: "codex"}, 0)
	if err != nil || q.User != "b@x" || len(f.slept) != 0 {
		t.Fatalf("got %q %v, slept %v", q.User, err, f.slept)
	}
}

// Every account used up: it sleeps to just past the soonest reset, in
// steps of at most ten minutes, and returns the account that came back.
func TestQuotaWaitUntilReset(t *testing.T) {
	resetA, resetB := t0.Add(25*time.Minute), t0.Add(3*time.Hour)
	f := &fakeWait{now: t0}
	f.at = func(now time.Time) []provider.SubscriptionQuota {
		a := codexAt("a@x", 100, resetA)
		if !now.Before(resetA) {
			a = codexAt("a@x", 0, resetA.Add(7*24*time.Hour))
		}
		return []provider.SubscriptionQuota{codexAt("b@x", 100, resetB), a}
	}
	q, err := f.wait().run(context.Background(), quotaTarget{provider: "codex", label: "codex"}, 0)
	if err != nil || q.User != "a@x" {
		t.Fatalf("got %q %v", q.User, err)
	}
	want := []time.Duration{10 * time.Minute, 10 * time.Minute, 5*time.Minute + quotaWaitMargin}
	if len(f.slept) != len(want) {
		t.Fatalf("slept %v, want %v", f.slept, want)
	}
	for i := range want {
		if f.slept[i] != want[i] {
			t.Fatalf("slept %v, want %v", f.slept, want)
		}
	}
	if len(f.said) != 3 || !strings.Contains(f.said[0], "codex (a@x first) is used up until") {
		t.Errorf("said %q", f.said)
	}
}

// One account named: another's room doesn't end the wait, and a reset
// under a minute away is still a minute's sleep.
func TestQuotaWaitOneAccount(t *testing.T) {
	reset := t0.Add(10 * time.Second)
	f := &fakeWait{now: t0}
	f.at = func(now time.Time) []provider.SubscriptionQuota {
		a := codexAt("a@x", 100, reset)
		if now.After(reset) {
			a = codexAt("a@x", 5, reset.Add(time.Hour))
		}
		return []provider.SubscriptionQuota{codexAt("b@x", 0, reset), a}
	}
	q, err := f.wait().run(context.Background(), quotaTarget{provider: "codex", user: "A@x", label: "codex · a@x"}, 0)
	if err != nil || q.User != "a@x" || len(f.slept) != 1 || f.slept[0] != quotaWaitMin {
		t.Fatalf("got %q %v, slept %v", q.User, err, f.slept)
	}
}

// --timeout: the last look is at the deadline, then errWaitTimeout.
func TestQuotaWaitTimeout(t *testing.T) {
	f := &fakeWait{now: t0, at: func(time.Time) []provider.SubscriptionQuota {
		return []provider.SubscriptionQuota{codexAt("a@x", 100, t0.Add(5*time.Hour))}
	}}
	_, err := f.wait().run(context.Background(), quotaTarget{provider: "codex", label: "codex"}, 25*time.Minute)
	if !errors.Is(err, errWaitTimeout) {
		t.Fatalf("err %v", err)
	}
	if f.now != t0.Add(25*time.Minute) || f.reads != 4 {
		t.Fatalf("ended at %v after %d reads, slept %v", f.now.Sub(t0), f.reads, f.slept)
	}
}

// Usage that can't be read is asked again, less often each time up to
// ten minutes, and the wait goes on once it reads.
func TestQuotaWaitUnknownRetries(t *testing.T) {
	f := &fakeWait{now: t0}
	f.at = func(now time.Time) []provider.SubscriptionQuota {
		switch {
		case now.Sub(t0) < 20*time.Minute:
			return []provider.SubscriptionQuota{{Provider: "codex", User: "a@x", Error: "HTTP 503"}}
		case now.Sub(t0) < 40*time.Minute:
			// the last reading kept in its place says nothing of now
			at := t0
			q := codexAt("a@x", 0, t0)
			q.AsOf = &at
			return []provider.SubscriptionQuota{q}
		}
		return []provider.SubscriptionQuota{codexAt("a@x", 50, t0.Add(time.Hour))}
	}
	q, err := f.wait().run(context.Background(), quotaTarget{provider: "codex", label: "codex"}, 0)
	if err != nil || q.User != "a@x" {
		t.Fatalf("got %q %v", q.User, err)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	if len(f.slept) != len(want) {
		t.Fatalf("slept %v, want %v", f.slept, want)
	}
	for i := range want {
		if f.slept[i] != want[i] {
			t.Fatalf("slept %v, want %v", f.slept, want)
		}
	}
	if !strings.Contains(f.said[0], "couldn't be read (HTTP 503)") {
		t.Errorf("said %q", f.said[0])
	}
}

// A reset that isn't told: looked at again every ten minutes.
func TestQuotaWaitResetUntold(t *testing.T) {
	f := &fakeWait{now: t0}
	f.at = func(now time.Time) []provider.SubscriptionQuota {
		q := provider.SubscriptionQuota{Provider: "zcode", User: "u", Windows: []provider.QuotaWindow{{Name: "day", Used: 100}}}
		if now.Sub(t0) >= 20*time.Minute {
			q.Windows[0].Used = 0
		}
		return []provider.SubscriptionQuota{q}
	}
	_, err := f.wait().run(context.Background(), quotaTarget{provider: "zcode", label: "zcode"}, 0)
	if err != nil || len(f.slept) != 2 || f.slept[0] != quotaWaitMax {
		t.Fatalf("%v, slept %v", err, f.slept)
	}
}

// An account switched off in magpie is passed over when waiting on its
// provider: its room would never be used.
func TestQuotaWaitSkipsOff(t *testing.T) {
	f := &fakeWait{now: t0, at: func(now time.Time) []provider.SubscriptionQuota {
		a := codexAt("a@x", 100, t0.Add(time.Minute))
		if now.After(t0) {
			a = codexAt("a@x", 0, t0.Add(time.Hour))
		}
		return []provider.SubscriptionQuota{codexAt("off@x", 0, t0), a}
	}}
	w := f.wait()
	w.skip = func(q provider.SubscriptionQuota) bool { return q.User == "off@x" }
	q, err := w.run(context.Background(), quotaTarget{provider: "codex", label: "codex"}, 0)
	if err != nil || q.User != "a@x" || len(f.slept) != 1 {
		t.Fatalf("got %q %v, slept %v", q.User, err, f.slept)
	}
}

// Ctrl+C mid-sleep ends the wait with the context's error.
func TestQuotaWaitCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeWait{now: t0, cancel: cancel, cancelAt: 2, at: func(time.Time) []provider.SubscriptionQuota {
		return []provider.SubscriptionQuota{codexAt("a@x", 100, t0.Add(time.Hour))}
	}}
	_, err := f.wait().run(ctx, quotaTarget{provider: "codex", label: "codex"}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestResolveQuotaTarget(t *testing.T) {
	qs := []provider.SubscriptionQuota{
		codexAt("a@x", 0, t0), codexAt("b@x", 0, t0),
		{Provider: "claude", Name: "Claude Code", User: "a@x"},
		{Provider: "zcode", Name: "ZCode", User: "solo@y"},
	}
	known := func(s string) bool { return s == "kimi" }
	for _, c := range []struct{ arg, provider, user, err string }{
		{arg: "codex", provider: "codex"},
		{arg: "Claude Code", provider: "claude"},
		{arg: "solo@y", provider: "zcode", user: "solo@y"},
		{arg: "codex/B@x", provider: "codex", user: "B@x"},
		{arg: "a@x", err: "codex/a@x, claude/a@x"},
		{arg: "codex/z@x", err: "codex has no account z@x"},
		{arg: "kimi", err: "no subscription or plan whose allowance"},
		{arg: "nope", err: "no subscription, plan or account nope"},
	} {
		got, err := resolveQuotaTarget(c.arg, qs, known)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v, want %q", c.arg, err, c.err)
			}
			continue
		}
		if err != nil || got.provider != c.provider || got.user != c.user {
			t.Errorf("%s: %+v %v", c.arg, got, err)
		}
	}
}

// Unknown names and flags are exit code 2, before anything is read.
func TestQuotaWaitCmdUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"--timeout", "soon", "codex"}, {"--frob", "codex"}, {"codex", "claude"}} {
		var e exitError
		if err := quotaWaitCmd(args); !errors.As(err, &e) || e.code != 2 {
			t.Errorf("%q: %v", args, err)
		}
	}
}
