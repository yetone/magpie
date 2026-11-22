package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

// claudeUsageWaiting has /usage answer after d, or end with its ctx.
func claudeUsageWaiting(t *testing.T, d time.Duration) {
	t.Helper()
	claudeCLIUsage.RLock()
	old := claudeCLIUsage.f
	claudeCLIUsage.RUnlock()
	t.Cleanup(func() { UsageClaudeVia(old) })
	UsageClaudeVia(func(ctx context.Context, _ string) (string, error) {
		select {
		case <-time.After(d):
			return "Current session: 5% used", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
}

// A /usage run outlasts the refresh it is part of: every read on the
// owner's Mac was killed at the refresh's 20s, where Claude Code's two
// rounds of requests take longer.
func TestClaudeUsageOutlastsRefresh(t *testing.T) {
	claudeUsageWaiting(t, 200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := runClaudeUsage(ctx, "")
	if err != nil || got == "" {
		t.Fatalf("run cut at its refresh's deadline: %q, %v", got, err)
	}
}

// It is still bounded, by claudeUsageTimeout.
func TestClaudeUsageBounded(t *testing.T) {
	old := claudeUsageTimeout
	claudeUsageTimeout = 30 * time.Millisecond
	t.Cleanup(func() { claudeUsageTimeout = old })
	claudeUsageWaiting(t, time.Minute)
	start := time.Now()
	_, err := runClaudeUsage(context.Background(), "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the run's own deadline", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

// And a caller that goes away ends it.
func TestClaudeUsageEndsWithCaller(t *testing.T) {
	claudeUsageWaiting(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := runClaudeUsage(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}
