package gateway

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// 9won_25273 on Discord: an account capped at 300 of a day's 1000 credits
// is held once the vendor's count of the day reaches 300, and the request
// goes to the next account; under it, it is used; every account at its
// cap is refused, named with the count and the amount.
func TestWindowAmountHoldsTheAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		if err := provider.SetWindowAmount("codex", u, []string{"Daily credits"}, 300); err != nil {
			t.Fatal(err)
		}
	}
	try := func(count map[string]float64) (int, string, string, *Server) {
		t.Helper()
		old := allowances
		t.Cleanup(func() { allowances = old })
		day := time.Now().Add(5 * time.Hour)
		allowances = func(string) map[string]provider.Allowance {
			out := map[string]provider.Allowance{}
			for u, n := range count {
				out[u] = provider.Allowance{{Name: "Daily credits", Used: n / 10, Amount: n, Of: 1000, Unit: "credits", Resets: day, Span: 24 * time.Hour}}
			}
			return out
		}
		b.mu.Lock()
		b.tried = nil
		b.mu.Unlock()
		srv := New()
		code, body := resetPost(t, srv)
		tried, _ := b.seen()
		return code, body, tried, srv
	}

	code, body, tried, srv := try(map[string]float64{"me@example.com": 320, "spare@example.com": 40})
	if code != 200 || tried != "acct-2" {
		t.Fatalf("320 of 300 credits: %d %s tried %s", code, body, tried)
	}
	var left []string
	for _, r := range srv.Trace(t.Context(), 0, 0).Routes {
		for _, w := range r.Left {
			if w.Capped > 0 && !w.Barred {
				left = append(left, fmt.Sprintf("%s %g/%g %s", w.Who, w.CapCount, w.CapAmount, w.CapUnit))
			}
		}
	}
	if len(left) != 1 || left[0] != "me@example.com 320/300 credits" {
		t.Fatalf("traced as capped: %v", left)
	}

	if code, body, tried, _ := try(map[string]float64{"me@example.com": 299, "spare@example.com": 40}); code != 200 || tried != "acct-1" {
		t.Fatalf("299 of 300 credits: %d %s tried %s", code, body, tried)
	}

	code, body, tried, _ = try(map[string]float64{"me@example.com": 300, "spare@example.com": 412.5})
	if code != 429 || tried != "" {
		t.Fatalf("both at their amount: %d tried %q", code, tried)
	}
	for _, want := range []string{"usage cap reached", "(me@example.com) has used 300 credits", "(spare@example.com) has used 412.5 credits", "past its cap of 300"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
}
