package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A ChatGPT account at 100% of its five hours that holds credits answers
// on them (the backend answers it): by default it is asked first as the
// order says and spends them; set not to spend its credits, it is held as
// used up and the next account takes the request, the trace saying why.
func TestCodexNoCreditsMovesOn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t) // every account answers: the first on its credits
	capUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20})
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	code, body := resetPost(t, New())
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" {
		t.Fatalf("credits spent: %d %s tried %s", code, body, tried)
	}

	if err := provider.SetCodexCredits("Me@Example.com", false); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	srv := New()
	code, body = resetPost(t, srv)
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("credits not spent: %d %s tried %s", code, body, tried)
	}
	var left []string
	for _, r := range srv.Trace(t.Context(), 0, 0).Routes {
		for _, w := range r.Left {
			if w.NoCredits && w.Capped == 100 && w.CapBack != nil {
				left = append(left, w.Who)
			}
		}
	}
	if len(left) != 1 || left[0] != "me@example.com" {
		t.Fatalf("traced as held for its credits: %v", left)
	}
}

// Every account held, as none may spend its credits or one is at its cap:
// the client is refused as when all are used up (429), told why in the
// words of what holds each, and how to go on; nothing reaches the vendor.
func TestCodexNoCreditsEveryAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	capUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 100})
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		if err := provider.SetCodexCredits(u, false); err != nil {
			t.Fatal(err)
		}
	}
	srv := New()
	code, body := resetPost(t, srv)
	if tried, _ := b.seen(); code != 429 || tried != "" {
		t.Fatalf("%d tried %q: %s", code, tried, body)
	}
	for _, want := range []string{"rate_limit_error", "set in magpie not to spend its credits", "me@example.com", "spare@example.com", "quota credits"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
	if strings.Contains(body, "usage cap reached") {
		t.Errorf("told as a cap: %s", body)
	}

	// one held at its cap, the other for its credits: both told
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	code, body = resetPost(t, srv)
	if tried, _ := b.seen(); code != 429 || tried != "" {
		t.Fatalf("%d tried %q: %s", code, tried, body)
	}
	for _, want := range []string{"held by magpie", "past its 70% cap", "(spare@example.com) has used up a usage window and is set not to spend its credits", "account-cap", "quota credits"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
}

// Codex's one account, its week used up and credits left, set not to
// spend them and to spend its resets by itself: relayed as it came it
// would answer on its credits; routed, it is held, and with nobody else
// left a reset is spent first and the turn goes through on the windows
// started again — the trace says so. Without leave to spend resets, it is
// refused, and nothing is spent.
func TestCodexNoCreditsSpendsAReset(t *testing.T) {
	codexSignedIn(t)
	b := newResetBackend(t)
	b.week["acct-1"], b.held["acct-1"] = 100, 1 // answers all the same: credits
	capUsage(t, map[string]float64{"me@example.com": 100})
	if err := provider.SetCodexCredits("me@example.com", false); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	if tried, spent := b.seen(); code != 429 || !strings.Contains(body, "not to spend its credits") || tried != "" || spent != "" {
		t.Fatalf("no auto-reset: %d %s tried %q spent %q", code, body, tried, spent)
	}

	if err := provider.SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	code, body = resetPost(t, srv)
	if tried, spent := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" || spent != "acct-1" {
		t.Fatalf("auto-reset: %d %s tried %q spent %q", code, body, tried, spent)
	}
	if got := resetsTraced(srv); len(got) != 1 || got[0] != "me@example.com: 2 windows started again" {
		t.Fatalf("traced %v", got)
	}
	// the account it was spent on is in the trace's order, tried, not
	// left out
	r := srv.Trace(t.Context(), 0, 0).Routes[0]
	if len(r.Order) != 1 || r.Order[0].Who != "me@example.com" || r.Order[0].Capped != 0 || len(r.Left) != 0 {
		t.Fatalf("order %+v left %+v", r.Order, r.Left)
	}
}

// The account Codex is signed in to is out of its allowance, its credits
// spent or none, and the only other is held as it won't spend its credits,
// its week used up, with leave to spend its resets: once the first is
// refused, the held one spends a reset and answers.
func TestCodexNoCreditsResetAfterTheLastRefusal(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t, "acct-1")
	b.held["acct-1"] = 0
	b.week["acct-2"], b.held["acct-2"] = 100, 1
	capUsage(t, map[string]float64{"me@example.com": 60, "spare@example.com": 100})
	if err := provider.SetCodexCredits("spare@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetCodexAutoReset("spare@example.com", true); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	if tried, spent := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1,acct-2" || spent != "acct-2" {
		t.Fatalf("%d %s tried %q spent %q", code, body, tried, spent)
	}
	if got := resetsTraced(srv); len(got) != 1 || got[0] != "spare@example.com: 2 windows started again" {
		t.Fatalf("traced %v", got)
	}
}
