package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude account's plain 429 rate_limit_error for one model rests that
// model on the account, not the account (kaitokiku on X: Fable rate limited
// showed "限流" on the account's Opus row too). A refusal of an allowance
// window that counts every model still rests the account.
func TestClaudeModelRateRestsThatModelOnly(t *testing.T) {
	fresh(t)
	acct := func(model string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "rate@example.com"}}, model: model, rest: "claude"}
	}
	s := &Server{}
	body := func(msg string) []byte {
		return []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"` + msg + `"}}`)
	}
	hdr := http.Header{"Retry-After": {"2520"}}
	fable := acct("claude-fable-5-2")
	r := s.restAfter(fable, http.StatusTooManyRequests, hdr, body("This request would exceed your account's rate limit. Please try again later."))
	if r.Why != failRate || r.Model != "claude-fable-5-2" || r.Key != fable.restID() {
		t.Fatalf("rest: %+v", r)
	}
	if _, ok := restingOf(acct("claude-opus-5-5")); ok {
		t.Fatal("Opus rests on an account whose Fable is rate limited")
	}
	// the wrong guess: if Anthropic limits the whole account, Opus gets its
	// own 429 there, rests on that account for Opus, and another account is
	// still ready for it (the request falls over, as any rested candidate)
	opus := acct("claude-opus-5-5")
	if r := s.restAfter(opus, http.StatusTooManyRequests, hdr, body("This request would exceed your account's rate limit.")); r.Model != opus.model || r.Key != opus.restID() {
		t.Fatalf("Opus rest: %+v", r)
	}
	if _, ok := restingOf(opus); !ok {
		t.Fatal("Opus does not rest on the account that refused it")
	}
	if _, ok := restingOf(fable); !ok {
		t.Fatal("Fable's rest was lost")
	}
	other := candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "second@example.com"}}, model: "claude-opus-5-5", rest: "claude"}
	if _, ok := restingOf(other); ok {
		t.Fatal("the second account rests for another account's refusal")
	}
	fresh(t)
	// the account's windows rest the account
	for _, c := range []struct {
		msg string
		h   http.Header
	}{
		{"5-hour limit reached", hdr},
		// as the live ledger's logs hold it (a warm-up refused by the weekly window)
		{"Claude Code: You've hit your weekly limit · resets 2pm (Asia/Shanghai)", http.Header{}},
		{"This request would exceed your account's rate limit.", http.Header{"Retry-After": {"60"}, "Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"}}},
	} {
		o := acct("claude-opus-5-5")
		if r := s.restAfter(o, http.StatusTooManyRequests, c.h, body(c.msg)); r.Key != o.restKey() {
			t.Fatalf("%q rested %+v, want the account", c.msg, r)
		}
		fresh(t)
	}
}
