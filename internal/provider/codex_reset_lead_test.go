package provider

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// setResetLead saves the user's lead, in minutes, in a sandbox HOME.
func setResetLead(t *testing.T, minutes int) {
	t.Helper()
	s := settings.Load()
	s.CodexResetLead = minutes
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// The spend and routing go by the lead the user set (the owner, 10-30:
// 把提前用卡的时间做成可配置的), the same one, so they can't drift apart
// (#717, #718): three hours set, a reset running out in four is spent in
// one, and routing takes the windows to start again then.
func TestResetLeadFollowsSetting(t *testing.T) {
	signIn(t)
	now := time.Now().Round(0)
	until := now.Add(4 * time.Hour)
	if got := expiringResetSpent(until, now, false, time.Time{}); !got.Equal(until.Add(-30 * time.Minute)) {
		t.Fatalf("unset: spent at %v, want half an hour before", until.Sub(got))
	}
	setResetLead(t, 180)
	want := until.Add(-3 * time.Hour)
	if got := expiringResetSpent(until, now, false, time.Time{}); !got.Equal(want) {
		t.Fatalf("spent %v before it runs out, want 3h", until.Sub(got))
	}
	a := Allowance{{Used: 20, Resets: now.Add(120 * time.Hour), Span: 7 * 24 * time.Hour}}.restartedBy(until)
	if got := a.Restarts(now); !got.Equal(want) {
		t.Fatalf("routing: restarts %v before it runs out, want 3h", until.Sub(got))
	}
	// within the lead already: at once, for both
	now2 := until.Add(-2 * time.Hour)
	if got := expiringResetSpent(until, now2, false, time.Time{}); !got.Equal(now2) {
		t.Fatalf("within the lead: %v", got)
	}
	if got := a.Restarts(now2); !got.Equal(now2) {
		t.Fatalf("routing within the lead: %v", got)
	}
	// and the check spends it then, not before
	e := expiringResets{next: map[string]time.Time{}}
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 40}}
	if out, _ := e.check("me@example.com", want.Add(-time.Minute), func() ([]QuotaWindow, *ResetCredits, error) {
		return used, &ResetCredits{Count: 1, Until: &until}, nil
	}, func() (ResetOutcome, error) { return ResetOutcome{Code: "reset"}, nil }); out.Code != "" {
		t.Fatalf("spent a minute before the lead: %+v", out)
	}
	e.next = map[string]time.Time{}
	if out, _ := e.check("me@example.com", want, func() ([]QuotaWindow, *ResetCredits, error) {
		return used, &ResetCredits{Count: 1, Until: &until}, nil
	}, func() (ResetOutcome, error) { return ResetOutcome{Code: "reset"}, nil }); out.Code != "reset" {
		t.Fatalf("not spent at the lead: %+v", out)
	}
}

// A long lead still has several looks inside it: looks tighten to every
// resetExpiryClose half an hour before the lead starts, so a spend that
// fails when it is due is tried again five minutes later, not half an
// hour, and one missed look doesn't push the spend toward the end.
func TestResetLeadLooksInsideLongLead(t *testing.T) {
	signIn(t)
	setResetLead(t, 6*60)
	start := time.Date(2026, 10, 30, 0, 0, 0, 0, time.Local)
	until := start.Add(12 * time.Hour)
	due := until.Add(-6 * time.Hour)
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 40}}
	e := expiringResets{next: map[string]time.Time{}}
	var spent time.Time
	var looks []time.Time
	tries := 0
	for now := start; now.Before(until); now = now.Add(resetExpiryClose) {
		e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
			looks = append(looks, now)
			if !spent.IsZero() {
				return used, nil, nil
			}
			return used, &ResetCredits{Count: 1, Until: &until}, nil
		}, func() (ResetOutcome, error) {
			// the first try fails (ChatGPT not answering)
			if tries++; tries == 1 {
				return ResetOutcome{}, http.ErrHandlerTimeout
			}
			spent = now
			return ResetOutcome{Code: "reset"}, nil
		})
	}
	if want := due.Add(resetExpiryClose); !spent.Equal(want) {
		t.Fatalf("spent %v before it runs out, want %v (looked at %v)", until.Sub(spent), until.Sub(want), looks)
	}
	// looked at every five minutes from half an hour before the lead
	n := 0
	for _, l := range looks {
		if !l.Before(due.Add(-30*time.Minute)) && !l.After(due) {
			n++
		}
	}
	if n != 7 {
		t.Fatalf("%d looks in the half hour before the lead, want 7: %v", n, looks)
	}
	for _, c := range []struct{ lead, near time.Duration }{
		{15 * time.Minute, time.Hour},
		{30 * time.Minute, time.Hour},
		{2 * time.Hour, 150 * time.Minute},
		{24 * time.Hour, 24*time.Hour + 30*time.Minute},
	} {
		if got := resetExpiryNear(c.lead); got != c.near {
			t.Errorf("lead %v: near %v, want %v", c.lead, got, c.near)
		}
	}
}

// A lead out of range, written by hand, or a settings file that can't be
// read, is the default half hour: never none, which would spend a reset
// only as it runs out.
func TestResetLeadUnreadIsDefault(t *testing.T) {
	signIn(t)
	for _, m := range []int{-5, 1, 2, 24*60 + 1, 1 << 30} {
		setResetLead(t, m)
		if got := resetLead(); got != 30*time.Minute {
			t.Errorf("%d minutes: lead %v", m, got)
		}
	}
	setResetLead(t, 15)
	if got := resetLead(); got != 15*time.Minute {
		t.Fatalf("15 minutes: lead %v", got)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"codexResetLead": 15,`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resetLead(); got != 30*time.Minute && got != 15*time.Minute {
		// a half-written file is read as the last good one or the default
		t.Fatalf("unreadable: lead %v", got)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"codexResetLead": "an hour"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resetLead(); got != 30*time.Minute && got != 15*time.Minute {
		t.Fatalf("not a number: lead %v", got)
	}
}

// runLoop drives check as KeepResetsFromRunningOut does, from start until
// the reset runs out: a pass at now, which takes the time spend says (a
// pass hung on ChatGPT ends at its two-minute bound), then wake's sleep.
// It returns when the reset was spent (zero: never) and the tries.
func runLoop(t *testing.T, start, until time.Time, spend func(try int, now time.Time) (ResetOutcome, error, time.Duration)) (spent time.Time, tries []time.Time) {
	t.Helper()
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 40}}
	e := expiringResets{next: map[string]time.Time{}}
	for now := start; now.Before(until); {
		took := 3 * time.Second
		e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
			if !spent.IsZero() {
				return used, nil, nil
			}
			return used, &ResetCredits{Count: 1, Until: &until}, nil
		}, func() (ResetOutcome, error) {
			tries = append(tries, now)
			out, err, d := spend(len(tries), now)
			took += d
			if err == nil && out.Code == "reset" {
				spent = now.Add(d)
			}
			return out, err
		})
		now = now.Add(took)
		now = now.Add(e.wake(now))
	}
	return spent, tries
}

// The shortest lead, five minutes (shangyuan0818 on Discord: less than 15,
// to use more of the windows first), is spent at its start, not at the
// next five-minute pass, whatever the loop's phase; a failed try is made
// again a minute on, so a try hung to the pass's two-minute bound still
// leaves another before the reset runs out.
func TestShortLeadSpentInTime(t *testing.T) {
	signIn(t)
	setResetLead(t, 5)
	until := time.Date(2026, 11, 7, 18, 0, 0, 0, time.Local)
	due := until.Add(-5 * time.Minute)
	for _, phase := range []time.Duration{0, 17 * time.Second, 2*time.Minute + 41*time.Second, 4*time.Minute + 59*time.Second} {
		start := until.Add(-3*time.Hour + phase)
		// answers at once: spent at the lead's start
		spent, tries := runLoop(t, start, until, func(int, time.Time) (ResetOutcome, error, time.Duration) {
			return ResetOutcome{Code: "reset"}, nil, time.Second
		})
		if spent.Before(due) || spent.After(due.Add(5*time.Second)) || len(tries) != 1 {
			t.Errorf("phase %v: spent %v before it runs out, want 5m; tries at %v", phase, until.Sub(spent), tries)
		}
		// the first try hangs two minutes and fails: tried again, in time
		spent, tries = runLoop(t, start, until, func(n int, _ time.Time) (ResetOutcome, error, time.Duration) {
			if n == 1 {
				return ResetOutcome{}, http.ErrHandlerTimeout, 2 * time.Minute
			}
			return ResetOutcome{Code: "reset"}, nil, time.Second
		})
		if spent.IsZero() || !spent.Before(until.Add(-time.Minute)) || len(tries) != 2 {
			t.Errorf("phase %v, a hung try: spent %v before it runs out; tries at %v", phase, until.Sub(spent), tries)
		}
		// ChatGPT refusing at once: a try every minute, four before it runs out
		spent, tries = runLoop(t, start, until, func(n int, _ time.Time) (ResetOutcome, error, time.Duration) {
			if n < 4 {
				return ResetOutcome{}, http.ErrHandlerTimeout, time.Second
			}
			return ResetOutcome{Code: "reset"}, nil, time.Second
		})
		if spent.IsZero() || !spent.Before(until) || len(tries) != 4 {
			t.Errorf("phase %v, failing tries: spent %v before it runs out; tries at %v", phase, until.Sub(spent), tries)
		}
	}
	// a lead the CLI set off the five-minute marks (7m): the look lands on
	// its start too, not at the pass after
	setResetLead(t, 7)
	for _, phase := range []time.Duration{0, 17 * time.Second, 4*time.Minute + 59*time.Second} {
		spent, _ := runLoop(t, until.Add(-3*time.Hour+phase), until, func(int, time.Time) (ResetOutcome, error, time.Duration) {
			return ResetOutcome{Code: "reset"}, nil, time.Second
		})
		if want := until.Add(-7 * time.Minute); spent.Before(want) || spent.After(want.Add(5*time.Second)) {
			t.Errorf("7m lead, phase %v: spent %v before it runs out", phase, until.Sub(spent))
		}
	}
	// the least lead (3m): spent at its start, and a try hung to the pass's
	// two-minute bound still leaves a retry that is spent before it runs out
	setResetLead(t, settings.CodexResetLeadLeast)
	least := time.Duration(settings.CodexResetLeadLeast) * time.Minute
	for _, phase := range []time.Duration{0, 17 * time.Second, 2*time.Minute + 41*time.Second, 4*time.Minute + 59*time.Second} {
		start := until.Add(-3*time.Hour + phase)
		spent, tries := runLoop(t, start, until, func(int, time.Time) (ResetOutcome, error, time.Duration) {
			return ResetOutcome{Code: "reset"}, nil, time.Second
		})
		if want := until.Add(-least); spent.Before(want) || spent.After(want.Add(5*time.Second)) || len(tries) != 1 {
			t.Errorf("least lead, phase %v: spent %v before it runs out; tries at %v", phase, until.Sub(spent), tries)
		}
		spent, tries = runLoop(t, start, until, func(n int, _ time.Time) (ResetOutcome, error, time.Duration) {
			if n == 1 {
				return ResetOutcome{}, http.ErrHandlerTimeout, 2 * time.Minute
			}
			return ResetOutcome{Code: "reset"}, nil, 30 * time.Second
		})
		if spent.IsZero() || !spent.Before(until) || len(tries) != 2 {
			t.Errorf("least lead, phase %v, a hung try: spent %v before it runs out; tries at %v", phase, until.Sub(spent), tries)
		}
	}
	// a long lead failing all along is tried every five minutes until the
	// last quarter hour, not every minute for hours
	setResetLead(t, 6*60)
	_, tries := runLoop(t, until.Add(-7*time.Hour), until, func(int, time.Time) (ResetOutcome, error, time.Duration) {
		return ResetOutcome{}, http.ErrHandlerTimeout, time.Second
	})
	if n := len(tries); n < 70 || n > 90 {
		t.Errorf("a six-hour lead failing: %d tries, want about 6h/5m + 15", n)
	}
}

// The loop sleeps until the soonest look due of the accounts its pass went
// over, five minutes at most, a second at least: one due while the pass
// still ran is looked at once; an account the pass skipped (auto-use
// turned off) doesn't wake it.
func TestResetLoopWakesForTheSoonestLook(t *testing.T) {
	now := time.Now().Round(0)
	e := expiringResets{next: map[string]time.Time{}}
	if got := e.wake(now); got != resetExpiryClose {
		t.Fatalf("none due: %v", got)
	}
	passed := func(keys ...string) {
		for _, k := range keys {
			e.check(k, now.Add(-time.Hour), func() ([]QuotaWindow, *ResetCredits, error) { t.Fatal("looked"); return nil, nil, nil },
				func() (ResetOutcome, error) { t.Fatal("spent"); return ResetOutcome{}, nil })
		}
	}
	e.next["skipped"] = now.Add(-time.Hour)
	e.next["far"] = now.Add(time.Hour)
	passed("far")
	if got := e.wake(now); got != resetExpiryClose {
		t.Fatalf("skipped and far: %v", got)
	}
	e.next["soon"] = now.Add(97 * time.Second)
	passed("far", "soon")
	if got := e.wake(now); got != 97*time.Second {
		t.Fatalf("one in 97s: %v", got)
	}
	// each pass names its own accounts: soon isn't waited for unless asked about again
	passed("far")
	if got := e.wake(now); got != resetExpiryClose {
		t.Fatalf("soon not in the pass: %v", got)
	}
	e.next["overdue"] = now.Add(-30 * time.Second)
	passed("far", "overdue")
	if got := e.wake(now); got != time.Second {
		t.Fatalf("one due while the pass ran: %v", got)
	}
}
