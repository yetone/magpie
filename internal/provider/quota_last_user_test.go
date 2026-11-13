package provider

import (
	"context"
	"testing"
	"time"
)

// An account's reading is named for the account it was asked for,
// whichever way it comes back (#5310: a read given up on at its deadline
// came back with the kept windows and no User, and 768 of 1,440
// Antigravity accounts each drew a card of its own on the Usage page).
func TestAccountReadingNamed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	restart()
	defer restart()
	later := time.Now().Add(time.Hour)
	for _, agent := range []string{"antigravity", "claude"} {
		l := Login{Agent: agent, User: "a@x.com"}
		keepLast(SubscriptionQuota{Provider: loginProvider(l), User: "a@x.com", Windows: []QuotaWindow{
			{Name: "5 hours", Used: 40, ResetsAt: &later, Span: 5 * time.Hour},
		}}, "")
		// given up on at its deadline, as loginReading does: the kept
		// reading, under the account's name
		if q := keepReading(context.Background(), cutShort(l, context.DeadlineExceeded), l.User); q.User != "a@x.com" || len(q.Windows) != 1 || q.Windows[0].Used != 40 {
			t.Fatalf("%s deadline, kept: %+v", agent, q)
		}
		if q := keepLast(cutShort(l, errNotAsked), l.User); q.User != "a@x.com" || len(q.Windows) != 1 {
			t.Fatalf("%s not asked: %+v", agent, q)
		}
		// an account with nothing kept says its error under its own name
		b := Login{Agent: agent, User: "b@x.com"}
		if q := keepReading(context.Background(), cutShort(b, context.DeadlineExceeded), b.User); q.User != "b@x.com" || q.Error == "" {
			t.Fatalf("%s nothing kept: %+v", agent, q)
		}
		// a reading made with no name is named; one with a name keeps it
		if q := namedFor(b, SubscriptionQuota{Provider: agent}); q.User != "b@x.com" {
			t.Fatalf("%s unnamed: %+v", agent, q)
		}
		if q := namedFor(b, SubscriptionQuota{Provider: agent, User: "B@X.com"}); q.User != "B@X.com" {
			t.Fatalf("%s named: %+v", agent, q)
		}
	}
}
