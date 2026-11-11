package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const grokBalanceOut402 = `{"error":{"message":"Grok: Grok Build usage balance exhausted"}}`

// A Grok account refused with "usage balance exhausted" rests until its
// spent window renews, not 30 minutes "for credit" (@justacati on X: weekly
// 7 days 0% left, 4 days to reset, "402 余额不足 · 19:58 恢复").
func TestGrokBalanceExhaustedRestsToReset(t *testing.T) {
	old, oldAwait := allowances, awaitAllowance
	defer func() { allowances, awaitAllowance = old, oldAwait }()
	fresh(t)
	now := time.Now()
	reset := now.Add(4 * 24 * time.Hour)
	al := map[string]provider.Allowance{"a@x.com": {{Used: 100, Resets: reset, Span: 7 * 24 * time.Hour}}}
	allowances = func(string) map[string]provider.Allowance { return al }
	grok := func(agent string) candidate {
		return candidate{p: provider.Provider{ID: "grok", Account: &provider.Account{Agent: agent, User: "a@x.com"}}, model: "grok-4.7", rest: "grok"}
	}
	r := (&Server{}).restAfter(grok("plugin:grok"), 402, http.Header{}, []byte(grokBalanceOut402))
	if d := time.Until(r.Until); r.Why != failQuota || r.By != "window" || d < 4*24*time.Hour-time.Minute || d > 4*24*time.Hour+time.Minute {
		t.Fatalf("rests %v: %+v, want out of quota to the weekly reset", d, r)
	}

	// no window known: the short rest, not a week
	al = nil
	r = (&Server{}).restAfter(grok("plugin:grok"), 402, http.Header{}, []byte(grokBalanceOut402))
	if d := time.Until(r.Until); r.Why != failQuota || d > time.Hour {
		t.Fatalf("no window known rests %v: %+v", d, r)
	}

	// another vendor's 402 with the same words, and Grok's own payment 402
	// are still out of credit
	al = map[string]provider.Allowance{"a@x.com": {{Used: 100, Resets: reset, Span: 7 * 24 * time.Hour}}}
	r = (&Server{}).restAfter(grok("plugin:other"), 402, http.Header{}, []byte(grokBalanceOut402))
	if r.Why != failCredit {
		t.Fatalf("another vendor's 402 rests %+v, want credit", r)
	}
	r = (&Server{}).restAfter(grok("plugin:grok"), 402, http.Header{}, []byte(`{"error":{"message":"Payment required"}}`))
	if r.Why != failCredit {
		t.Fatalf("Grok's payment 402 rests %+v, want credit", r)
	}
}
