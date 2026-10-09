package gateway

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// capUsage stands in for the accounts' windows as last read: the five
// hours of each, by user, at the share given.
func capUsage(t *testing.T, used map[string]float64) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	reset := time.Now().Add(3 * time.Hour)
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range used {
			out[u] = provider.Allowance{
				{Used: n, Resets: reset, Span: 5 * time.Hour},
				{Used: 10, Resets: time.Now().Add(4 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
			}
		}
		return out
	}
}

// An account at 75% of its five hours with a 70% usage cap counts as used
// up: the request goes to the next account, though the first is first in
// the order; with the cap lifted, the first takes it again.
func TestAccountCapMovesOn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t) // every account has room as far as ChatGPT goes
	capUsage(t, map[string]float64{"me@example.com": 75, "spare@example.com": 20})
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("capped: %d %s tried %s", code, body, tried)
	}
	// the routing trace says it was left out at its cap, and till when
	var left []string
	for _, r := range srv.Trace(t.Context(), 0, 0).Routes {
		for _, w := range r.Left {
			if w.Capped > 0 && !w.Barred && w.CapBack != nil {
				left = append(left, fmt.Sprintf("%s %g/%d", w.Who, w.Used, w.Capped))
			}
		}
	}
	if len(left) != 1 || left[0] != "me@example.com 75/70" {
		t.Fatalf("traced as capped: %v", left)
	}

	if err := provider.SetAccountCap("codex", "me@example.com", 0); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	code, body = resetPost(t, New())
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" {
		t.Fatalf("cap lifted: %d %s tried %s", code, body, tried)
	}
}

// Every account at its cap: the client is refused as when all are used
// up (429, a rate-limit error), told the cap was reached and what it is,
// and when to try again; nothing is sent to the vendor.
func TestAccountCapEveryAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	capUsage(t, map[string]float64{"me@example.com": 75, "spare@example.com": 92})
	for user, cap := range map[string]int{"me@example.com": 70, "spare@example.com": 90} {
		if err := provider.SetAccountCap("codex", user, cap); err != nil {
			t.Fatal(err)
		}
	}
	srv := New()
	code, body := resetPost(t, srv)
	if tried, _ := b.seen(); code != 429 || tried != "" {
		t.Fatalf("%d tried %q", code, tried)
	}
	for _, want := range []string{"rate_limit_error", "usage cap reached", "me@example.com", "past its 70% cap", "spare@example.com", "past its 90% cap", "account-cap"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
	if calls := srv.Recent(); len(calls) == 0 || calls[0].Status != 429 || calls[0].Error != "every account at its usage cap" {
		t.Fatalf("request log %+v", calls)
	}
}

// One Codex account signed in, at 80% of its week with a 70% cap and the
// user's leave to spend its resets by themselves: the cap holds it, and no
// reset is spent for it — the resets go by ChatGPT's own 100%. Relayed as
// it came, it would have gone through to ChatGPT; with a cap it is routed,
// and held.
func TestAccountCapSpendsNoCodexReset(t *testing.T) {
	codexSignedIn(t)
	b := newResetBackend(t)
	b.week["acct-1"], b.held["acct-1"] = 80, 1
	capUsage(t, map[string]float64{"me@example.com": 80})
	if err := provider.SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	tried, spent := b.seen()
	if code != 429 || !strings.Contains(body, "usage cap reached") || !strings.Contains(body, "70%") || tried != "" || spent != "" {
		t.Fatalf("%d %s tried %q spent %q", code, body, tried, spent)
	}
	if got := resetsTraced(srv); len(got) != 0 {
		t.Fatalf("resets traced %v", got)
	}
	// lifted, it answers, and still spends none: 80% isn't used up
	if err := provider.SetAccountCap("codex", "me@example.com", 0); err != nil {
		t.Fatal(err)
	}
	code, body = resetPost(t, srv)
	if tried, spent := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" || spent != "" {
		t.Fatalf("cap lifted: %d %s tried %q spent %q", code, body, tried, spent)
	}
}

// windowUsage stands in for the accounts' windows as last read, named as
// the vendor's reading names them: each user's five hours and week.
func windowUsage(t *testing.T, used map[string][2]float64) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	five, week := time.Now().Add(3*time.Hour), time.Now().Add(4*24*time.Hour)
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range used {
			out[u] = provider.Allowance{
				{Name: "5 hours", Used: n[0], Resets: five, Span: 5 * time.Hour},
				{Name: "Weekly", Used: n[1], Resets: week, Span: 7 * 24 * time.Hour},
			}
		}
		return out
	}
}

// Each window can have a cap of its own (willz on Discord): a friend's
// account stops when its five hours reach 50%, while its week may go to
// 40%. At 60% of its five hours it is held, though its week is low, and
// the request goes to the next account; at 20% and 30% it is under both
// and takes the request. The other account, with one cap for every window
// as before, is held by that one alone.
func TestWindowCapsHoldTheAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for w, c := range map[string]int{"5 hours": 50, "Weekly": 40} {
		if err := provider.SetWindowCap("codex", "me@example.com", w, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetAccountCap("codex", "spare@example.com", 10); err != nil {
		t.Fatal(err)
	}
	try := func(used map[string][2]float64) (int, string, string, *Server) {
		t.Helper()
		windowUsage(t, used)
		b.mu.Lock()
		b.tried = nil
		b.mu.Unlock()
		srv := New()
		code, body := resetPost(t, srv)
		tried, _ := b.seen()
		return code, body, tried, srv
	}

	// five hours at 60% of its 50%: held, the spare (5% of its 10%) answers
	code, body, tried, srv := try(map[string][2]float64{"me@example.com": {60, 10}, "spare@example.com": {5, 5}})
	if code != 200 || tried != "acct-2" {
		t.Fatalf("5 hours past its own cap: %d %s tried %s", code, body, tried)
	}
	var left []string
	for _, r := range srv.Trace(t.Context(), 0, 0).Routes {
		for _, w := range r.Left {
			if w.Capped > 0 && !w.Barred {
				left = append(left, fmt.Sprintf("%s %g/%d", w.Who, w.Used, w.Capped))
			}
		}
	}
	if len(left) != 1 || left[0] != "me@example.com 60/50" {
		t.Fatalf("traced as capped: %v", left)
	}

	// five hours at 20% of 50%, week at 30% of 40%: used, first in order
	if code, body, tried, _ := try(map[string][2]float64{"me@example.com": {20, 30}, "spare@example.com": {5, 5}}); code != 200 || tried != "acct-1" {
		t.Fatalf("under both its windows' caps: %d %s tried %s", code, body, tried)
	}

	// week at 45%, past its own 40% though under the five hours' 50%; the
	// spare's week at 12%, past its one cap of 10%: both held, refused,
	// each named with the cap of the window that holds it
	code, body, tried, _ = try(map[string][2]float64{"me@example.com": {20, 45}, "spare@example.com": {5, 12}})
	if code != 429 || tried != "" {
		t.Fatalf("both held: %d tried %q", code, tried)
	}
	for _, want := range []string{"usage cap reached", "(me@example.com) is at 45%", "past its 40% cap", "spare@example.com", "past its 10% cap"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
}
