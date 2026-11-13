package provider

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// noExtraUsage turns Claude's Extra usage off (ClaudeNoExtraUsage) for
// the test.
func noExtraUsage(t *testing.T, off bool) {
	t.Helper()
	if err := settings.Save(settings.Settings{ClaudeNoExtraUsage: off}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
}

// claudeRead is a Claude account's windows as /usage tells them: its five
// hours and week at the shares given, on overages once one is used up,
// with spent of its 50 USD of extra usage.
func claudeRead(five, week, spent float64, now time.Time) []QuotaWindow {
	r5, r7 := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	ws := []QuotaWindow{
		{Name: "5 hours", Used: five, Span: 5 * time.Hour, ResetsAt: &r5},
		{Name: "7 days", Used: week, Span: 7 * 24 * time.Hour, ResetsAt: &r7},
		{Name: "Usage credits", Used: spent / 50 * 100, Aside: true, Extra: true, Amount: spent, Limit: 50, Unit: "USD"},
	}
	if five >= 100 || week >= 100 {
		ws = append(ws, claudeOveragesWindow(ws))
	}
	return ws
}

// baksili_ on X: a Claude account past its plan goes on at Anthropic's
// extra usage, billed. With Extra usage off in magpie (off by default:
// nothing changes for anyone who didn't turn it off) every Claude account
// is held at 100% of a plan window, as a Codex account set not to spend
// its credits is, while what its plan has left is still used; a Codex
// account's credits are not touched by it.
func TestClaudeNoExtraUsageHoldsAtThePlan(t *testing.T) {
	signIn(t)
	now := time.Now()
	p := Provider{ID: "claude"}
	if !ClaudeExtraUsage() || HoldCaps(p, "claude", "c@example.com").Holds() {
		t.Fatal("held by default")
	}
	full := allowanceOf(claudeRead(100, 40, 12, now), now)
	if h := full.CapHeld("claude-sonnet-5", HoldCaps(p, "claude", "c@example.com"), now); h != nil {
		t.Fatalf("held by default at 100%%: %+v", h)
	}

	noExtraUsage(t, true)
	if ClaudeExtraUsage() {
		t.Fatal("on, though turned off")
	}
	caps := HoldCaps(p, "claude", "c@example.com")
	if share, credits := caps.Of("5 hours"); share != 100 || !credits {
		t.Fatalf("held at %d (credits %v)", share, credits)
	}
	h := full.CapHeld("claude-sonnet-5", caps, now)
	if h == nil || h.Cap != 100 || !h.Credits || h.Back.Before(now.Add(time.Hour)) {
		t.Fatalf("used up, not held at 100%%: %+v", h)
	}
	if h := allowanceOf(claudeRead(60, 99, 0, now), now).CapHeld("claude-sonnet-5", caps, now); h != nil {
		t.Fatalf("held with its plan not used up: %+v", h)
	}
	// a cap of the user's own still holds at it
	capped := Provider{ID: "claude", AccountCaps: map[string]int{"c@example.com": 70}}
	if h := allowanceOf(claudeRead(75, 20, 0, now), now).CapHeld("claude-sonnet-5", HoldCaps(capped, "claude", "c@example.com"), now); h == nil || h.Cap != 70 || h.Credits {
		t.Fatalf("its own cap: %+v", h)
	}
	// Codex's credits are their own switch
	if share, credits := HoldCaps(Provider{ID: "codex"}, "codex", "x@example.com").Of("5 hours"); share != 0 || credits {
		t.Fatalf("a Codex account held at %d (credits %v)", share, credits)
	}
}

// A cap on a Claude account's Usage credits (its extra usage) holds the
// account at 100% of its plan once that much extra usage is spent: never
// before its plan is used up, as the plan costs nothing more. Below the
// cap it goes on at its extra usage, as the vendor lets it.
func TestClaudeExtraUsageCapHoldsAtThePlan(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name               string
		caps               WindowCaps
		five, spent        float64
		held, heldAtPlanOK bool
	}{
		{"amount reached, plan used up", WindowCaps{Amounts: map[string]float64{"usage credits": 20}}, 100, 20, true, false},
		{"amount reached, plan has room", WindowCaps{Amounts: map[string]float64{"usage credits": 20}}, 60, 25, false, false},
		{"amount not reached", WindowCaps{Amounts: map[string]float64{"usage credits": 20}}, 100, 19.5, false, false},
		{"share reached, plan used up", WindowCaps{Windows: map[string]int{"usage credits": 40}}, 100, 20, true, false},
		{"share not reached", WindowCaps{Windows: map[string]int{"usage credits": 40}}, 100, 15, false, false},
	} {
		al := allowanceOf(claudeRead(c.five, 30, c.spent, now), now)
		h := al.CapHeld("claude-opus-5-5", c.caps, now)
		if (h != nil) != c.held {
			t.Errorf("%s: held %+v", c.name, h)
			continue
		}
		if h != nil && (h.Cap != 100 || !h.Credits) {
			t.Errorf("%s: held at %d (credits %v), want 100%% of the plan", c.name, h.Cap, h.Credits)
		}
	}
}

// An amount cap on a window set aside (WorkBuddy's credits beside its
// plan) holds the account once that much of it is used: the aside
// window's count reaches its cap, as a plan window's does.
func TestAsideWindowAmountCapHolds(t *testing.T) {
	now := time.Now()
	day := now.Add(5 * time.Hour)
	al := allowanceOf([]QuotaWindow{
		{Name: "Plan", Used: 20, Span: 24 * time.Hour, ResetsAt: &day},
		{Name: "Credits", Used: 25, Aside: true, Amount: 250, Limit: 1000, Unit: "credits", ResetsAt: &day},
	}, now)
	caps := WindowCaps{Amounts: map[string]float64{"credits": 200}}
	h := al.CapHeld("m", caps, now)
	if h == nil || h.Amount != 200 || h.Count != 250 || h.Unit != "credits" || h.Credits {
		t.Fatalf("250 of a 200 cap: %+v", h)
	}
	if h := al.CapHeld("m", WindowCaps{Amounts: map[string]float64{"credits": 300}}, now); h != nil {
		t.Fatalf("250 of a 300 cap: %+v", h)
	}
}

// The warm-up sends a held account nothing while it is used up: a
// request then would be billed as extra usage (Claude) or spend credits
// (Codex) the user set it not to spend. Its windows stay due, and go out
// once it has room again.
func TestWarmUpSendsAHeldAccountNothing(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	weekEnd := now.Add(2 * 24 * time.Hour)
	q := SubscriptionQuota{Windows: []QuotaWindow{
		{Name: "5 hours", Span: fiveHours}, // not started
		win("7 days", week, 100, weekEnd),
	}}
	q.Windows = append(q.Windows, claudeOveragesWindow(q.Windows))
	held, sent := true, 0
	w := codexWarmer{path: filepath.Join(t.TempDir(), "w.json"), now: func() time.Time { return now },
		usage:  func(context.Context) map[string]SubscriptionQuota { return map[string]SubscriptionQuota{"a@x": q} },
		send:   func(context.Context, string) error { sent++; return nil },
		held:   func(string, SubscriptionQuota, time.Time) bool { return held },
		expect: claudeWindowsExpected}
	if rs := w.warmNow(context.Background(), "all", nil); sent != 0 || len(rs) != 0 {
		t.Fatalf("held and used up: sent %d %+v", sent, rs)
	}
	held = false
	if w.warmNow(context.Background(), "all", nil); sent != 1 {
		t.Fatalf("not held: sent %d", sent)
	}
}

// claudeHeldFull, the Claude warm-up's hold: Extra usage off, or the
// account's extra usage at the cap set on it.
func TestClaudeHeldFull(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	now := time.Now()
	q := SubscriptionQuota{Windows: claudeRead(100, 30, 30, now)}
	if claudeHeldFull("c@example.com", q, now) {
		t.Fatal("held with Extra usage on and no cap")
	}
	// saved as SetWindowAmount saves it, which wants the account's email
	// from a /usage read this test has none of
	if err := Save(Provider{ID: "claude", AccountWindowAmounts: map[string]map[string]float64{"c@example.com": {WindowCapID("Usage credits"): 25}}}); err != nil {
		t.Fatal(err)
	}
	if !claudeHeldFull("c@example.com", q, now) {
		t.Fatalf("30 USD past a 25 USD cap, not held: %+v", AccountCapsOf("claude", "c@example.com"))
	}
	if claudeHeldFull("d@example.com", q, now) {
		t.Fatal("another account held by its cap")
	}
	noExtraUsage(t, true)
	if !claudeHeldFull("d@example.com", q, now) {
		t.Fatal("Extra usage off, not held")
	}
}
