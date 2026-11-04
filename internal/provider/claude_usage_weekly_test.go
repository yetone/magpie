package provider

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// claudeWeeklyOnly is `claude -p /usage` of a Max 20x account, which has no
// 5-hour window, as #4532's reporter printed it: no "Current session" line.
const claudeWeeklyOnly = `You are currently using your subscription to power your Claude Code usage

Current week (all models): 1% used · resets Nov 6 at 11am (Asia/Dubai)
Current week (Fable): 0% used · resets Nov 6 at 11am (Asia/Dubai)
`

// #4532: every /usage the tests had carried a session line. With weekly
// windows only, the week across all models is what stops the account, the
// Fable week is the model's own, and no 5-hour window is made up.
func TestClaudeUsageWeeklyOnly(t *testing.T) {
	dubai, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	// the day the reporter read it, and in a zone far from Dubai's
	for _, now := range []time.Time{
		time.Date(2026, 10, 31, 9, 0, 0, 0, dubai),
		time.Date(2026, 11, 5, 18, 0, 0, 0, time.FixedZone("UTC-10", -10*3600)),
	} {
		ws, err := parseClaudeUsage(claudeWeeklyOnly, now)
		if err != nil {
			t.Fatalf("at %v: %v", now, err)
		}
		if len(ws) != 2 {
			t.Fatalf("at %v: %d windows: %+v", now, len(ws), ws)
		}
		reset := time.Date(2026, 11, 6, 11, 0, 0, 0, dubai)
		all, fable := ws[0], ws[1]
		if all.Name != "7 days" || all.Model != "" || all.Used != 1 || all.Span != 7*24*time.Hour || all.ResetsAt == nil || !all.ResetsAt.Equal(reset) {
			t.Errorf("at %v: the week: %+v", now, all)
		}
		if fable.Name != "7 days · Fable" || fable.Model != "fable" || fable.Used != 0 || fable.ResetsAt == nil || !fable.ResetsAt.Equal(reset) {
			t.Errorf("at %v: Fable's week: %+v", now, fable)
		}
		for _, w := range ws {
			if w.Span == 5*time.Hour {
				t.Errorf("at %v: a 5-hour window made up: %+v", now, w)
			}
		}
	}

	// near out goes by the week across all models alone; Fable's own
	// week is the model's, which the switch doesn't count yet (#4532 asks
	// that it may)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, dubai)
	at := func(all, fable string) []QuotaWindow {
		t.Helper()
		text := strings.Replace(claudeWeeklyOnly, "(all models): 1%", "(all models): "+all+"%", 1)
		text = strings.Replace(text, "(Fable): 0%", "(Fable): "+fable+"%", 1)
		ws, err := parseClaudeUsage(text, now)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	for _, c := range []struct {
		all, fable string
		near, out  bool
	}{
		{"1", "0", false, false},
		{"89", "0", false, false},
		{"90", "0", true, false},
		{"100", "0", true, true},
		{"1", "100", false, false},
	} {
		ws := at(c.all, c.fable)
		if got := claudeNearOut(ws, now); got != c.near {
			t.Errorf("week %s%%, Fable %s%%: near out %v", c.all, c.fable, got)
		}
		if got := usedUp(SubscriptionQuota{Windows: ws}, now); got != c.out {
			t.Errorf("week %s%%, Fable %s%%: used up %v", c.all, c.fable, got)
		}
	}
	// the week read used up is back when it resets
	if back := BackAt(SubscriptionQuota{Windows: at("100", "0")}, now); !back.Equal(time.Date(2026, 11, 6, 11, 0, 0, 0, dubai)) {
		t.Errorf("back at %v", back)
	}
}

// The switch off an account whose /usage has weekly windows only: at 1%
// Claude Code stays where it is; with the week used up it moves to the
// spare, read the same way.
func TestClaudeSwitchWeeklyOnly(t *testing.T) {
	claudeThree(t) // pro@x.com signed in; team and max saved
	var text atomic.Value
	text.Store("")
	fakeClaudeUsage(t, &text, nil)
	forget := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}
	forget()
	t.Cleanup(forget)
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	spare := ""
	for _, l := range Logins("claude") {
		if strings.HasPrefix(l.User, "max@x.com") {
			spare = l.User
			if err := SetLoginOn("claude", l.User, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	// the reporter's lines, their reset three days on (the week's still
	// running whenever the test runs)
	dubai, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().In(dubai).Add(72*time.Hour).Format("Jan 2 at 3pm") + " (Asia/Dubai)"
	usage := func(all string) string {
		s := strings.ReplaceAll(claudeWeeklyOnly, "Nov 6 at 11am (Asia/Dubai)", reset)
		return strings.Replace(s, "(all models): 1%", "(all models): "+all+"%", 1)
	}
	var mu sync.Mutex
	active := "1"
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if dir == "" {
			return usage(active), nil
		}
		return usage("1"), nil // the spare
	})
	ctx := context.Background()
	if to, err := SwitchWhenSpent(ctx, "claude"); err != nil || to != "" {
		t.Fatalf("at 1%% of the week: switched to %q: %v", to, err)
	}
	q := LoginUsage(ctx, "claude")
	for user, u := range q {
		// the card has the 5-hour window /usage left out, not started
		// (motui9535): Anthropic leaves one out while it hasn't started
		if strings.HasPrefix(user, "pro@x.com") && (len(u.Windows) != 3 || u.Windows[0].Name != "5 hours" || u.Windows[0].Used != 0 || u.Windows[0].ResetsAt != nil ||
			u.Windows[1].Name != "7 days" || u.Windows[2].Model != "fable") {
			t.Fatalf("Claude Code's own account read: %+v", u)
		}
	}
	mu.Lock()
	active = "100"
	mu.Unlock()
	// the next look, past the reading's wait and the 30s floor
	forget()
	claudeUsage.Lock()
	for k, e := range claudeUsage.m {
		e.tried, e.at = e.tried.Add(-time.Hour), e.at.Add(-time.Hour)
		claudeUsage.m[k] = e
	}
	claudeUsage.Unlock()
	if to, err := SwitchWhenSpent(ctx, "claude"); err != nil || to != spare {
		t.Fatalf("the week used up: switched to %q: %v, want %q", to, err, spare)
	}
}
