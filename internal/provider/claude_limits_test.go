package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// What Claude Code says as it answers is the account's allowance, without
// its /usage, which keeps the windows it alone tells.
func TestNoteClaudeLimits(t *testing.T) {
	var out atomic.Value
	out.Store("")
	var fail atomic.Bool
	fail.Store(true) // /usage fails
	fakeClaudeUsage(t, &out, &fail)
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"kev@example.com": {at: time.Now().Add(-time.Hour), ws: []QuotaWindow{
		{Name: "7 days · Opus", Used: 30, Model: "opus"}, {Name: "5 hours", Used: 5},
	}}}
	claudeUsage.Unlock()
	defer func() { claudeUsage.Lock(); claudeUsage.m = nil; claudeUsage.Unlock() }()

	NoteClaudeLimits("Kev@example.com", []ClaudeLimit{{Kind: "five_hour", Used: 0.42, ResetsAt: reset.Unix()}, {Kind: "seven_day", Used: 0.1}, {Kind: "overage", Used: 1}})
	ws, err := claudeWindows(context.Background(), "kev@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range ws {
		names = append(names, w.Name)
	}
	if len(ws) != 3 || ws[0].Name != "5 hours" || ws[0].Used != 42 || !ws[0].ResetsAt.Equal(reset) || ws[0].Span != 5*time.Hour || ws[1].Name != "7 days" || ws[1].Used != 10 || ws[2].Name != "7 days · Opus" {
		t.Fatalf("windows: %v %+v", names, ws)
	}

	// asked, /usage failing leaves what was heard
	AskClaudeUsage()
	claudeUsage.Lock()
	e := claudeUsage.m["kev@example.com"]
	e.at = time.Now().Add(-10 * time.Minute)
	e.tried = time.Now().Add(-time.Hour) // past the floor
	claudeUsage.m["kev@example.com"] = e
	claudeUsage.Unlock()
	if ws, err := claudeWindows(context.Background(), "kev@example.com", true); err != nil || len(ws) != 3 {
		t.Fatalf("after the endpoint failed: %v %v", ws, err)
	}
	// heard long ago, the failure is told
	AskClaudeUsage()
	claudeUsage.Lock()
	e.heard = time.Now().Add(-2 * time.Hour)
	e.tried = time.Now().Add(-time.Hour)
	claudeUsage.m["kev@example.com"] = e
	claudeUsage.Unlock()
	if _, err := claudeWindows(context.Background(), "kev@example.com", true); err == nil {
		t.Fatal("an old word hid the endpoint's failure")
	}
}

// ifccod7391564 on X: on a cold start, an answer whose rate_limit_event
// told only the 5-hour window took the place of the reading kept on disk,
// and the week, not renewed yet, was gone from the card. What an answer
// doesn't name stands as kept.
func TestNoteClaudeLimitsKeepsTheWeekOnDisk(t *testing.T) {
	week := time.Now().Add(3 * 24 * time.Hour).Truncate(time.Second)
	gone := time.Now().Add(-time.Hour).Truncate(time.Second)
	five := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	claudeUsageCards(t, "", &lastQuota{At: time.Now().Add(-time.Hour), Q: SubscriptionQuota{
		Provider: "claude", User: "a@example.com", Windows: []QuotaWindow{
			{Name: "5 hours", Used: 10, ResetsAt: &gone},
			{Name: "7 days", Used: 37, ResetsAt: &week},
		},
	}, Spans: []lastWindow{{Span: 5 * time.Hour}, {Span: 7 * 24 * time.Hour}}})
	reset := func() {
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.data = nil
		subscriptionUsageCache.Unlock()
	}
	reset() // a cold start: nothing heard or read in this run
	t.Cleanup(reset)

	NoteClaudeLimits("A@example.com", []ClaudeLimit{{Kind: "five_hour", Used: 0.2, ResetsAt: five.Unix()}})
	q := cardOf(CachedCards(time.Now()), "claude", "a@example.com")
	if q == nil {
		t.Fatal("no Claude card")
	}
	if len(q.Windows) != 2 || q.Windows[0].Name != "5 hours" || q.Windows[0].Used != 20 ||
		q.Windows[1].Name != "7 days" || q.Windows[1].Used != 37 || q.Windows[1].ResetsAt == nil || !q.Windows[1].ResetsAt.Equal(week) || q.Windows[1].Span != 7*24*time.Hour {
		t.Fatalf("windows after a 5-hour-only answer: %+v", q.Windows)
	}
	if ls := KeptClaudeLimits("a@example.com"); len(ls) != 2 || ls[1].Kind != "seven_day" || ls[1].ResetsAt != week.Unix() {
		t.Fatalf("kept limits: %+v", ls)
	}

	// a week that has renewed since it was kept isn't brought back
	reset()
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	writeFile(t, lastQuotasPath(), map[string]lastQuota{"claude/a@example.com": {At: time.Now().Add(-8 * 24 * time.Hour), Q: SubscriptionQuota{
		Provider: "claude", User: "a@example.com", Windows: []QuotaWindow{{Name: "7 days", Used: 90, ResetsAt: &gone}},
	}}})
	NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: 0.2, ResetsAt: five.Unix()}})
	if ls := KeptClaudeLimits("a@example.com"); len(ls) != 1 || ls[0].Kind != "five_hour" {
		t.Fatalf("a renewed week came back: %+v", ls)
	}
}
