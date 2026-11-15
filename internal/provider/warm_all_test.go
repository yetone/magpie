package provider

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// Warm up all (CoderMaurice on X) sends the warm-up to each account in use
// whose windows aren't all running, and leaves alone, saying why, one
// turned off or paused and one already in its windows; one whose sign-in
// lapsed is sent nothing and failed, and one account's failure is its own.
func TestWarmAllWarmsEachIdleAccountInUse(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	five := func(used float64, resets *time.Time) QuotaWindow {
		return QuotaWindow{Name: "5 hours", Span: 5 * time.Hour, Used: used, ResetsAt: resets}
	}
	week := func(used float64, resets *time.Time) QuotaWindow {
		return QuotaWindow{Name: "7 days", Span: 7 * 24 * time.Hour, Used: used, ResetsAt: resets}
	}
	ls := []Login{
		{User: "own@example.com", Active: true},                  // idle: warmed
		{User: "on@example.com", On: true},                       // never read: warmed
		{User: "ON@example.com", On: true},                       // listed twice: once
		{User: "week@example.com", On: true},                     // week running, five hours idle: warmed
		{User: "busy@example.com", On: true},                     // both running: left
		{User: "reset@example.com", On: true},                    // read running, reset since: warmed
		{User: "spent@example.com", On: true},                    // used up, still running: left
		{User: "off@example.com"},                                // turned off: left
		{User: "paused@example.com", Active: true, Paused: true}, // paused: left
		{User: "lapsed@example.com", On: true, Lapsed: "OAuth token revoked"},
		{User: "fails@example.com", On: true},
	}
	usage := map[string]SubscriptionQuota{
		"own@example.com":   {Windows: []QuotaWindow{five(0, nil), week(0, nil)}},
		"week@example.com":  {Windows: []QuotaWindow{five(0, nil), week(30, at(3*24*time.Hour))}},
		"busy@example.com":  {Windows: []QuotaWindow{five(4, at(2*time.Hour)), week(10, at(24*time.Hour))}},
		"reset@example.com": {Windows: []QuotaWindow{five(40, at(-time.Minute)), week(10, at(24*time.Hour))}},
		"spent@example.com": {Windows: []QuotaWindow{five(100, at(time.Hour)), week(100, at(time.Hour)),
			{Name: "Extra usage", Text: true, Used: 100}}},
		"fails@example.com": {Windows: []QuotaWindow{five(0, nil)}},
	}
	var mu sync.Mutex
	var sent []string
	rs := warmAll(context.Background(), ls, usage, now, func(_ context.Context, user string) error {
		mu.Lock()
		sent = append(sent, user)
		mu.Unlock()
		if user == "fails@example.com" {
			return errors.New("Claude Code: You've hit your session limit")
		}
		return nil
	})
	want := []WarmAllResult{
		{User: "own@example.com"},
		{User: "on@example.com"},
		{User: "week@example.com"},
		{User: "busy@example.com", Skip: "running"},
		{User: "reset@example.com"},
		{User: "spent@example.com", Skip: "running"},
		{User: "off@example.com", Skip: "off"},
		{User: "paused@example.com", Skip: "off"},
		{User: "lapsed@example.com", Err: "needs signing in again: OAuth token revoked"},
		{User: "fails@example.com", Err: "Claude Code: You've hit your session limit"},
	}
	if !slices.Equal(rs, want) {
		t.Fatalf("results\n%+v\nwant\n%+v", rs, want)
	}
	slices.Sort(sent)
	if w := []string{"fails@example.com", "on@example.com", "own@example.com", "reset@example.com", "week@example.com"}; !slices.Equal(sent, w) {
		t.Fatalf("sent to %q, want %q", sent, w)
	}
}

// Warm up all is only for the agents with a warm-up.
func TestWarmAllOnlyCodexAndClaude(t *testing.T) {
	if _, err := WarmAll(context.Background(), "copilot"); err == nil {
		t.Fatal("warmed Copilot accounts")
	}
}
