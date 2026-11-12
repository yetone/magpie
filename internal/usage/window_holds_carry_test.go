package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Musikrv77852 on X: after the 7-day window resets and nothing is used
// yet, the Kimi Code card lost its estimate. It keeps the last window's,
// marked as that window's, until the new one tells its own, and across a
// restart.
func TestWholeKeptAcrossReset(t *testing.T) {
	holdsHome(t)
	now := holdClock(t, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	kimi := Record{Agent: "claude-code", Provider: "kimi-code", ProviderAccount: "mu@example.com", Status: 200}
	call := func(ago time.Duration, in, out int) {
		r := kimi
		r.Time, r.Model, r.Input, r.Output = now.Add(-ago), "claude-sonnet-4-6", in, out
		Append(r)
	}
	call(9*24*time.Hour, 1, 1) // the log's first call
	call(2*24*time.Hour, 30000, 10000)
	week := 7 * 24 * time.Hour
	card := func(at time.Time, used float64, resets time.Duration) []provider.SubscriptionQuota {
		end := at.Add(resets)
		return []provider.SubscriptionQuota{{Provider: "kimi-code", User: "mu@example.com", Windows: []provider.QuotaWindow{{Name: "7 days", Used: used, ResetsAt: &end, Span: week}}}}
	}
	got := WithWindowHolds(card(now, 40, 5*24*time.Hour), now)[0].Windows[0]
	if got.Holds == nil || got.Holds.PrevAt != nil || got.Holds.Tokens != 100000 {
		t.Fatalf("this window: %+v, want 100000 tokens, not carried", got.Holds)
	}

	later := now.Add(5*24*time.Hour + time.Hour) // reset passed, nothing used
	for _, used := range []float64{0, 2} {
		w := WithWindowHolds(card(later, used, week-time.Hour), later)[0].Windows[0]
		if w.Holds == nil || w.Holds.PrevAt == nil || w.Holds.Tokens != 100000 || w.HoldsWhy != "" {
			t.Fatalf("used %v after the reset: holds %+v why %q, want the last window's 100000 tokens, marked", used, w.Holds, w.HoldsWhy)
		}
	}

	// a restart: what is kept is on disk
	carry.Lock()
	carry.m = nil
	carry.Unlock()
	w := WithWindowHolds(card(later, 0, week-time.Hour), later)[0].Windows[0]
	if w.Holds == nil || w.Holds.PrevAt == nil || w.Holds.Tokens != 100000 {
		t.Fatalf("after a restart: %+v", w.Holds)
	}

	// the same window, nothing yet told: not another window's
	other := WithWindowHolds(card(now, 1, 5*24*time.Hour), now)[0].Windows[0]
	if other.Holds != nil || other.HoldsWhy != provider.HoldsLow {
		t.Fatalf("same window under the floor: %+v %q, want none and low", other.Holds, other.HoldsWhy)
	}

	// another account has none to carry
	q := card(later, 0, week-time.Hour)
	q[0].User = "other@example.com"
	if o := WithWindowHolds(q, later)[0].Windows[0]; o.Holds != nil {
		t.Fatalf("another account got %+v", o.Holds)
	}

	// the new window tells its own once it can
	r := kimi
	r.Time, r.Model, r.Input, r.Output = later.Add(-30*time.Minute), "claude-sonnet-4-6", 5000, 5000
	Append(r)
	own := WithWindowHolds(card(later, 10, week-time.Hour), later)[0].Windows[0]
	if own.Holds == nil || own.Holds.PrevAt != nil || own.Holds.Tokens != 100000 {
		t.Fatalf("new window with data: %+v", own.Holds)
	}
}
