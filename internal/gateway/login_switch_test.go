package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code signed in to a, with b on beside it; then signed in to b
// (magpie switching it, or the user). The account the agent is on is
// named by the provider's id alone, so the switch hands "claude" from a to
// b — but a conversation a answered stays with a, and what a served and
// how often it failed are a's, not b's (#209).
func TestLoginSwitchKeepsEachAccountItself(t *testing.T) {
	acct := func(user, rest string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Routing: provider.LeastUsed,
			Account: &provider.Account{Agent: "claude", User: user}}, model: "claude-opus-4-8", rest: rest}
	}
	t.Cleanup(func() {
		routed.Lock()
		for _, k := range []string{"claude", "claude@a@example.com", "claude@b@example.com"} {
			delete(routed.used, k)
			delete(routed.failures, k)
		}
		routed.Unlock()
	})
	a, b := acct("a@example.com", "claude"), acct("b@example.com", "claude@b@example.com")
	h := http.Header{}
	h.Set("x-claude-code-session-id", "login-switch-209")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	pl := planned{order: make([]Weighed, 2)}
	_, _, _, key := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Anthropic, body, body, []candidate{a, b}, pl)
	answered(key, a, 1, Usage{})
	servedCandidate(a, 5000)

	// signed in to b now
	a2, b2 := acct("a@example.com", "claude@a@example.com"), acct("b@example.com", "claude")
	cs, _, aff, _ := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Anthropic, body, body, []candidate{b2, a2}, planned{order: make([]Weighed, 2)})
	if cs[0].p.Account.User != "a@example.com" || !aff.Kept {
		t.Fatalf("the conversation went to %s (kept %v, %s)", cs[0].p.Account.User, aff.Kept, aff.Why)
	}
	// least used: b, which served nothing, before a
	cs, _ = weigh(b2.p, []candidate{a2, b2}, "claude-opus-4-8", provider.Anthropic)
	if cs[0].p.Account.User != "b@example.com" {
		t.Fatalf("least used put %s first", cs[0].p.Account.User)
	}
	routed.Lock()
	_, onB := routed.used["claude"]
	routed.Unlock()
	if onB {
		t.Fatal("a's tokens counted by the id the signed-in account has")
	}
}
