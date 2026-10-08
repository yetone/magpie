package provider

import (
	"context"
	"testing"
	"time"
)

// An account that spends a Codex reset about to run out by itself has its
// windows started again then, resetExpiryLead before the reset runs out:
// Weekly pace goes by the hours until that, when it is sooner than the week
// renews, and Smart's renewals do too (#717, #718). A reset that runs out
// after the week renews, or has run out, changes nothing.
func TestPaceUntilAutoUsedReset(t *testing.T) {
	now := time.Now()
	near := func(got, want float64) bool { return got > want*0.99 && got < want*1.01 }
	week := func(used float64, renews, runsOut time.Duration) Allowance {
		a := Allowance{
			{Used: 10, Resets: now.Add(2 * time.Hour), Span: 5 * time.Hour},
			{Used: used, Resets: now.Add(renews), Span: 7 * 24 * time.Hour},
		}
		if runsOut != 0 {
			a = a.restartedBy(now.Add(runsOut))
		}
		return a
	}
	// the issue's A: 80% left, the week renewing in five days, a reset
	// running out in an hour and a half — spent in one
	a := week(20, 120*time.Hour, 90*time.Minute)
	if p, due := a.Pace("m", now); !near(p, 80) || !due.Equal(now.Add(time.Hour)) {
		t.Fatalf("by the reset spent: %v %v", p, due)
	}
	if r := a.Renewal("m", now); len(r) != 2 || !r[0].Equal(now.Add(time.Hour)) || !r[1].Equal(now.Add(time.Hour)) {
		t.Fatalf("renewals by the reset spent: %v", r)
	}
	// none to spend: by the week, as before
	if p, due := week(20, 120*time.Hour, 0).Pace("m", now); !near(p, 80.0/120) || !due.Equal(now.Add(120*time.Hour)) {
		t.Fatalf("no reset: %v %v", p, due)
	}
	// the week renews first: by the week
	b := week(50, 48*time.Hour, 20*24*time.Hour)
	if p, due := b.Pace("m", now); !near(p, 50.0/48) || !due.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("week sooner: %v %v", p, due)
	}
	if r := b.Renewal("m", now); !r[0].Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("week sooner, renewals: %v", r)
	}
	// within the lead already: spent on the next look
	if p, due := week(20, 120*time.Hour, 20*time.Minute).Pace("m", now); !near(p, 80) || !due.Equal(now) {
		t.Fatalf("within the lead: %v %v", p, due)
	}
	// run out already: nothing to spend
	if p, _ := week(20, 120*time.Hour, -time.Minute).Pace("m", now); !near(p, 80.0/120) {
		t.Fatalf("ran out: %v", p)
	}
	// five hours alone are cut short too: the reset runs out in two and a
	// half hours, spent in two
	five := Allowance{{Used: 40, Resets: now.Add(4 * time.Hour), Span: 5 * time.Hour}}.restartedBy(now.Add(150 * time.Minute))
	if p, due := five.Pace("m", now); !near(p, 60.0/2) || !due.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("five hours alone: %v %v", p, due)
	}
}

// A reset counts only for an account the user lets spend its resets, that
// holds one that runs out, and whose windows have been used — as the
// spending itself (check). The allowances routing reads carry it.
func TestResetRunsOutForRouting(t *testing.T) {
	signIn(t)
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	until := now.Add(4 * time.Hour)
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 20, ResetsAt: at(120 * time.Hour)}}
	unused := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 0, ResetsAt: at(120 * time.Hour)}}
	aside := []QuotaWindow{{Span: 30 * 24 * time.Hour, Used: 50, Aside: true}}
	held := &ResetCredits{Count: 1, Until: &until}
	if got := resetRunsOut("codex", "me@example.com", used, held); !got.IsZero() {
		t.Fatalf("auto-use off: %v", got)
	}
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		agent   string
		windows []QuotaWindow
		resets  *ResetCredits
		want    time.Time
	}{
		"on, used":            {"codex", used, held, until},
		"none held":           {"codex", used, nil, time.Time{}},
		"count none":          {"codex", used, &ResetCredits{Until: &until}, time.Time{}},
		"never runs out":      {"codex", used, &ResetCredits{Count: 1}, time.Time{}},
		"windows unused":      {"codex", unused, held, time.Time{}},
		"on-demand only used": {"codex", aside, held, time.Time{}},
		"another vendor's":    {"claude", used, held, time.Time{}},
	} {
		if got := resetRunsOut(c.agent, "Me@example.com", c.windows, c.resets); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}

	LoginUsageVia(func(context.Context, string) map[string]SubscriptionQuota {
		return map[string]SubscriptionQuota{
			"me@example.com":    {Windows: used, Resets: held},
			"other@example.com": {Windows: used, Resets: held},
		}
	})
	reset := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale = nil
		usedCache.Unlock()
	}
	reset()
	t.Cleanup(func() { LoginUsageVia(nil); reset() })
	oldWait := firstWait
	firstWait = 5 * time.Second
	t.Cleanup(func() { firstWait = oldWait })
	all := Allowances("codex")
	if got := all["me@example.com"].Restarts(now); !got.Equal(until.Add(-resetExpiryLead)) {
		t.Fatalf("allowance read: restarts %v", got)
	}
	if got := all["other@example.com"].Restarts(now); !got.IsZero() {
		t.Fatalf("an account not let spend its resets: restarts %v", got)
	}
}

// Routing takes the reset as spent when check spends it: half an hour
// before it runs out, or now when the account is held up past then (#718).
func TestRestartsWhenHeldUp(t *testing.T) {
	now := time.Now()
	acct := func(fiveUsed float64, fiveBack time.Duration) Allowance {
		five := Limit{Used: fiveUsed, Span: 5 * time.Hour}
		if fiveBack != 0 {
			five.Resets = now.Add(fiveBack)
		}
		return Allowance{five, {Used: 32, Resets: now.Add(120 * time.Hour), Span: 7 * 24 * time.Hour}}.restartedBy(now.Add(3 * time.Hour))
	}
	for name, c := range map[string]struct {
		a    Allowance
		want time.Time
	}{
		"free again before it is spent":  {acct(100, 110*time.Minute), now.Add(150 * time.Minute)},
		"held up past it":                {acct(100, 160*time.Minute), now},
		"held up, not saying until when": {acct(100, 0), now},
		"not held up":                    {acct(60, 110*time.Minute), now.Add(150 * time.Minute)},
	} {
		if got := c.a.Restarts(now); !got.Equal(c.want) {
			t.Errorf("%s: restarts %v, want %v", name, got, c.want)
		}
	}
}
