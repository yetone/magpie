package gateway

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude account resting out of its five hours is back as soon as a
// reading of its windows, the one routing goes by, finds them started
// again, rather than when the time Claude Code's refusal named comes.
// Another account still out of its window stays out.
func TestClaudeAccountBackOnceItsWindowRenews(t *testing.T) {
	fresh(t)
	old := allowances
	allowances = provider.Allowances // the accounts' windows as read, here
	t.Cleanup(func() { allowances = old })
	// users of their own: Allowances keeps what it read for the next test
	const a, b = "renewed-a@example.com", "renewed-b@example.com"
	window := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	used := map[string]float64{a: 100, b: 100}
	provider.LoginUsageVia(func(_ context.Context, agent string) map[string]provider.SubscriptionQuota {
		out := map[string]provider.SubscriptionQuota{}
		if agent != "claude" {
			return out
		}
		mu.Lock()
		defer mu.Unlock()
		for u, n := range used {
			out[u] = provider.SubscriptionQuota{Provider: "claude", Windows: []provider.QuotaWindow{
				{Name: "5 hours", Used: n, Span: 5 * time.Hour, ResetsAt: &window},
				{Name: "7 days", Used: 40, Span: 7 * 24 * time.Hour},
			}}
		}
		return out
	})
	t.Cleanup(func() {
		provider.LoginUsageVia(nil)
		provider.StaleAllowance("claude", a)
	})
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: user}}, model: "claude-sonnet-4-6", rest: "claude#" + user}
	}
	ca, cb := acct(a), acct(b)
	forget := func() { clearRest(ca.restKey()); clearRest(cb.restKey()) }
	forget()
	t.Cleanup(forget)
	// read before: each out of its five hours (not what a test before left
	// read of them)
	full := func(al provider.Allowance) bool {
		for _, l := range al {
			if l.Used >= 100 {
				return true
			}
		}
		return false
	}
	provider.StaleAllowance("claude", a)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if al := allowances("claude"); full(al[a]) && full(al[b]) {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("routing reads %v", al)
		}
	}
	// refused, the way the bridge puts Claude Code's limit: resting till
	// the time it named, an hour past the window
	s := &Server{}
	h := http.Header{}
	h.Set(resetsHeader, strconv.FormatInt(window.Add(time.Hour).Unix(), 10))
	for _, c := range []candidate{ca, cb} {
		if r := s.restAfter(c, 429, h, []byte(`{"error":{"type":"rate_limit_error","message":"You've hit your limit"}}`)); r.Why != failQuota || r.By != "resets" {
			t.Fatalf("%s out of its five hours: rests by %s (%s)", c.p.Account.User, r.By, r.Why)
		}
	}

	// a's five hours started again (a new /usage, what Claude Code says
	// as it answers); routing reads it a minute on
	mu.Lock()
	used[a] = 4
	mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := restOf(ca.restKey()); !ok {
			break
		}
		// read again till a reading has it at 4%: one in flight as it
		// changed may have it full still
		if full(allowances("claude")[a]) {
			provider.StaleAllowance("claude", a)
		}
		if time.Now().After(deadline) {
			r, _ := restOf(ca.restKey())
			t.Fatalf("a, its window started again: still rests by %s till %v", r.By, r.Until)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r, ok := restOf(cb.restKey()); !ok || r.By != "resets" {
		t.Fatalf("b, still out of its five hours: %+v, %v", r, ok)
	}
}
