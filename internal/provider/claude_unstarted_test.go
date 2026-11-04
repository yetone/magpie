package provider

import (
	"strings"
	"testing"
	"time"
)

// motui9535 (Discord, v0.1.2909, a screenshot): of three Team accounts the
// preferred one showed its week and its Fable week and no 5-hour window,
// the two others all three. Anthropic leaves a window out (null) while it
// hasn't started, so /usage has no "Current session" line for an account
// not used in the last five hours; every Claude account has one all the
// same (claudeWindowsExpected). The card shows it unused, first, as the
// others order it, not missing; and a header heard later still replaces it.
func TestClaudeUnstartedSessionShownUnused(t *testing.T) {
	reset := time.Now().UTC().Add(72*time.Hour).Format("Jan 2 at 3pm") + " (UTC)"
	text := "Current week (all models): 17% used · resets " + reset + "\nCurrent week (Fable): 30% used · resets " + reset + "\n"
	_, card := claudeUsageCards(t, text, nil)
	q := card()
	if q.Error != "" {
		t.Fatalf("reading: %+v", q)
	}
	var names []string
	for _, w := range q.Windows {
		names = append(names, w.Name)
	}
	if strings.Join(names, ", ") != "5 hours, 7 days, 7 days · Fable" {
		t.Fatalf("windows %q, want the 5-hour one first and the two weeks: %+v", names, q.Windows)
	}
	five := q.Windows[0]
	if five.Used != 0 || five.ResetsAt != nil || five.Span != 5*time.Hour || five.Model != "" {
		t.Fatalf("the 5-hour window not started: %+v", five)
	}
	if q.Windows[1].Used != 17 || q.Windows[2].Used != 30 {
		t.Fatalf("the weeks as read: %+v", q.Windows)
	}

	// the account used: Claude Code's header names the 5-hour window, and
	// it stands in place of the unused one
	NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .12, ResetsAt: time.Now().Add(4 * time.Hour).Unix()}})
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	q = card()
	n := 0
	for _, w := range q.Windows {
		if w.Name == "5 hours" {
			n++
			if w.Used != 12 || w.ResetsAt == nil {
				t.Fatalf("the heard 5-hour window: %+v", w)
			}
		}
	}
	if n != 1 || q.Windows[0].Name != "5 hours" {
		t.Fatalf("one 5-hour window, first: %+v", q.Windows)
	}
}
