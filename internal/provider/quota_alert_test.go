package provider

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDueAlertsWindowOncePerRun(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	card := func(used float64, resets time.Time) []SubscriptionQuota {
		return []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "a@b.c", Windows: []QuotaWindow{
			{Name: "5 hours", Used: used, ResetsAt: &resets},
			{Name: "On-demand", Used: 100, Aside: true}, // set aside: never told
		}}}
	}
	marks := map[string]alertMark{}
	if got := dueAlerts(card(79.9, reset), marks, 80, 0, now); len(got) != 0 {
		t.Fatalf("under the share: %+v", got)
	}
	got := dueAlerts(card(80, reset), marks, 80, 0, now)
	if len(got) != 1 || got[0].Window != "5 hours" || got[0].User != "a@b.c" || got[0].Used != 80 || !got[0].ResetsAt.Equal(reset) {
		t.Fatalf("at the share: %+v", got)
	}
	// read again, the reset a few seconds off (Codex's moves): the same run
	if got := dueAlerts(card(95, reset.Add(20*time.Second)), marks, 80, 0, now.Add(5*time.Minute)); len(got) != 0 {
		t.Fatalf("told twice in one run: %+v", got)
	}
	// the window reset and was used up to the share again: told again
	next := reset.Add(5 * time.Hour)
	if got := dueAlerts(card(85, next), marks, 80, 0, reset.Add(time.Hour)); len(got) != 1 {
		t.Fatalf("a new run: %+v", got)
	}
	// falling under the share clears it, in the same run too
	dueAlerts(card(10, next), marks, 80, 0, reset.Add(2*time.Hour))
	if len(marks) != 0 {
		t.Fatalf("marks kept under the share: %v", marks)
	}
	if got := dueAlerts(card(81, next), marks, 80, 0, reset.Add(3*time.Hour)); len(got) != 1 {
		t.Fatalf("back over the share: %+v", got)
	}
	// off: nothing, and nothing kept
	if got := dueAlerts(card(100, next), marks, 0, 0, reset.Add(4*time.Hour)); len(got) != 0 || len(marks) != 0 {
		t.Fatalf("off: %+v %v", got, marks)
	}
}

func TestDueAlertsSecondsLeft(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	q := func(secs int64) []SubscriptionQuota {
		return []SubscriptionQuota{{Provider: "codex", Windows: []QuotaWindow{{Name: "7 days", Used: 90, ResetSecs: secs}}}}
	}
	marks := map[string]alertMark{}
	got := dueAlerts(q(3600), marks, 80, 0, now)
	if len(got) != 1 || got[0].ResetsAt == nil || !got[0].ResetsAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("reset from the seconds left: %+v", got)
	}
	if got := dueAlerts(q(3000), marks, 80, 0, now.Add(10*time.Minute)); len(got) != 0 {
		t.Fatalf("the same run, counted down: %+v", got)
	}
}

func TestDueAlertsStaleReadingKeepsMarks(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour)
	ok := []SubscriptionQuota{{Provider: "claude", User: "x", Windows: []QuotaWindow{{Name: "5 hours", Used: 90, ResetsAt: &reset}}}}
	marks := map[string]alertMark{}
	if len(dueAlerts(ok, marks, 80, 0, now)) != 1 {
		t.Fatal("not told")
	}
	// a failed reading, and one kept from before, say nothing new
	failed := []SubscriptionQuota{{Provider: "claude", User: "x", Error: "429"}}
	if got := dueAlerts(failed, marks, 80, 0, now.Add(alertKeep+time.Hour)); len(got) != 0 || len(marks) != 1 {
		t.Fatalf("failed reading: %+v %v", got, marks)
	}
	stale := []SubscriptionQuota{{Provider: "claude", User: "x", AsOf: &now, Windows: []QuotaWindow{{Name: "5 hours", Used: 10, ResetsAt: &reset}}}}
	if dueAlerts(stale, marks, 80, 0, now); len(marks) != 1 {
		t.Fatalf("a stale reading cleared the mark: %v", marks)
	}
	if len(dueAlerts(ok, marks, 80, 0, now)) != 0 {
		t.Fatal("told again after a failed reading")
	}
	// one no longer read at all is let go after a while
	dueAlerts(nil, marks, 80, 0, now.Add(alertKeep+time.Hour))
	if len(marks) != 0 {
		t.Fatalf("kept forever: %v", marks)
	}
}

func TestDueAlertsBalance(t *testing.T) {
	now := time.Now()
	q := func(b string) []SubscriptionQuota {
		return []SubscriptionQuota{{Provider: "deepseek", Name: "DeepSeek", Balance: b, Windows: []QuotaWindow{}}}
	}
	marks := map[string]alertMark{}
	if got := dueAlerts(q("¥5.01"), marks, 80, 5, now); len(got) != 0 {
		t.Fatalf("over the amount: %+v", got)
	}
	got := dueAlerts(q("¥5.00"), marks, 80, 5, now)
	if len(got) != 1 || got[0].Balance != "¥5.00" || got[0].Window != "" {
		t.Fatalf("at the amount: %+v", got)
	}
	if got := dueAlerts(q("¥1.20"), marks, 80, 5, now); len(got) != 0 {
		t.Fatalf("told twice: %+v", got)
	}
	// topped up, then down again
	dueAlerts(q("¥100.00"), marks, 80, 5, now)
	if got := dueAlerts(q("¥4.00"), marks, 80, 5, now); len(got) != 1 {
		t.Fatalf("down again: %+v", got)
	}
	// a share isn't an amount, and off is off
	if got := dueAlerts(q("3%"), map[string]alertMark{}, 80, 5, now); len(got) != 0 {
		t.Fatalf("a share: %+v", got)
	}
	if got := dueAlerts(q("$0.10"), map[string]alertMark{}, 80, 0, now); len(got) != 0 {
		t.Fatalf("off: %+v", got)
	}
}

func TestBalanceNumber(t *testing.T) {
	for s, want := range map[string]float64{
		"$12.34": 12.34, "¥1,024.50": 1024.5, "CNY 8": 8, "12.3k credits": 12300, "$-3.20": -3.2, "2M tokens": 2e6,
	} {
		if n, ok := BalanceNumber(s); !ok || n != want {
			t.Errorf("BalanceNumber(%q) = %v, %v; want %v", s, n, ok, want)
		}
	}
	for _, s := range []string{"45%", "", "unlimited"} {
		if _, ok := BalanceNumber(s); ok {
			t.Errorf("BalanceNumber(%q) read a number", s)
		}
	}
}

// what was told is kept across a restart: the file, not memory
func TestCheckAlertsKeepsMarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-alerts.json")
	now := time.Now()
	reset := now.Add(time.Hour)
	qs := []SubscriptionQuota{{Provider: "claude", User: "x", Windows: []QuotaWindow{{Name: "5 hours", Used: 90, ResetsAt: &reset}}}}
	if got := checkAlerts(path, qs, 80, 0, now); len(got) != 1 {
		t.Fatalf("first: %+v", got)
	}
	if got := checkAlerts(path, qs, 80, 0, now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("told again after the file was read back: %+v", got)
	}
}

// the windows Antigravity really reports (TestAntigravityQuotaPools): every
// model's own beside the pools' 5-hour and weekly ones. A pool's weekly
// window is what should be told of, once per pool and named with it — not
// once for every model drawing on it, and not the 5-hour asides.
func TestDueAlertsPoolWindows(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	reset := now.Add(48 * time.Hour)
	five, week := 5*time.Hour, 7*24*time.Hour
	qs := []SubscriptionQuota{{
		Provider: "antigravity",
		Name:     "Antigravity",
		User:     "u@x.com",
		Windows: []QuotaWindow{
			// the models' own, as fetchAvailableModels reports them, each
			// drawing on its pool
			{Name: "Gemini 3 Flash", Model: "gemini-3-flash", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
			{Name: "Gemini 3.1 Pro (High)", Model: "gemini-3.1-pro-high", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
			{Name: "Claude Opus 4.6 (Thinking)", Model: "claude-opus-4-6-thinking", Family: "Claude", Pool: "Claude & GPT", Used: 90},
			{Name: "GPT-OSS 120B (Medium)", Model: "gpt-oss-120b-medium", Family: "GPT-OSS", Pool: "Claude & GPT", Used: 90},
			// the pools' own, as retrieveUserQuotaSummary reports them:
			// aside, naming no model
			{Name: "7 days", Pool: "Gemini", Span: week, Aside: true, Used: 85, ResetsAt: &reset},
			{Name: "5 hours", Pool: "Gemini", Span: five, Aside: true, Used: 95, ResetsAt: &reset},
			{Name: "7 days", Pool: "Claude & GPT", Span: week, Aside: true, Used: 90, ResetsAt: &reset},
			{Name: "5 hours", Pool: "Claude & GPT", Span: five, Aside: true, Used: 90, ResetsAt: &reset},
		},
	}}
	marks := map[string]alertMark{}
	got := dueAlerts(qs, marks, 80, 0, now)
	if len(got) != 2 {
		t.Fatalf("got %d alerts, want one per pool: %+v", len(got), got)
	}
	if got[0].Window != "Gemini · 7 days" || got[1].Window != "Claude & GPT · 7 days" {
		t.Errorf("named %q, %q", got[0].Window, got[1].Window)
	}
	if len(marks) != 2 {
		t.Errorf("the pools' marks ran together: %v", marks)
	}
	// told once: the same readings again say nothing
	if again := dueAlerts(qs, marks, 80, 0, now); len(again) != 0 {
		t.Errorf("told again: %+v", again)
	}
	// a mark an earlier build wrote of a window with no pool still holds
	legacy := map[string]alertMark{"codex|c@x.com|w|5 hours|": {At: now, Until: &reset}}
	codex := []SubscriptionQuota{{Provider: "codex", User: "c@x.com",
		Windows: []QuotaWindow{{Name: "5 hours", Used: 90, ResetsAt: &reset}}}}
	if told := dueAlerts(codex, legacy, 80, 0, now); len(told) != 0 {
		t.Errorf("the old mark no longer holds: %+v", told)
	}
}
