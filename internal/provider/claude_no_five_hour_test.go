package provider

import (
	"testing"
	"time"
)

// shangyuan0818 on X: a verified Claude Ultra account has no 5-hour limit,
// and the Usage overview still showed one. Anthropic leaves a window out
// until a request starts it, so a reading of an idle account and one of an
// account with no such window look alike; the one made up for an idle
// account (claudeWithUnstarted) stays up only till the account is seen used
// (its week rises) with none, and then stays down, across a restart too,
// until a reading or header tells one.
func TestClaudeNoFiveHourWindowNotMadeUp(t *testing.T) {
	reset := time.Now().UTC().Add(72*time.Hour).Format("Jan 2 at 3pm") + " (UTC)"
	week := func(used string) string {
		return "Current week (all models): " + used + "% used · resets " + reset + "\n"
	}
	out, card := claudeUsageCards(t, week("10"), nil)
	has5 := func(q SubscriptionQuota) bool {
		for _, w := range q.Windows {
			if w.Name == "5 hours" {
				return true
			}
		}
		return false
	}
	// idle: no way to tell, the account is taken to have one, not started
	if q := card(); !has5(q) {
		t.Fatalf("an account not seen used shows its 5-hour window unstarted: %+v", q.Windows)
	}
	// used, and still none: the vendor sends none
	out.Store(week("12"))
	retryClaudeUsage()
	if q := card(); has5(q) || len(q.Windows) != 1 {
		t.Fatalf("an account used with no 5-hour window shows one: %+v", q.Windows)
	}
	// idle again: still none
	retryClaudeUsage()
	if q := card(); has5(q) {
		t.Fatalf("a window the account hasn't came back while idle: %+v", q.Windows)
	}
	// a reading that tells one shows it
	out.Store("Current session: 3% used · resets " + time.Now().UTC().Add(4*time.Hour).Format("Jan 2 at 3:04pm") + " (UTC)\n" + week("13"))
	retryClaudeUsage()
	if q := card(); !has5(q) {
		t.Fatalf("a 5-hour window the vendor sent: %+v", q.Windows)
	}
}
