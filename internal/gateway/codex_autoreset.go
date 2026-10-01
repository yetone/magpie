package gateway

// A Codex rate-limit reset spent by itself: when everyone a request could
// go to is out of their allowance, a ChatGPT account the user lets spend
// its resets, its weekly window used up, spends one and is asked again
// (provider.AutoUseCodexReset has the rules).

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// autoResetTimeout bounds the look at an account's week and the reset.
var autoResetTimeout = 20 * time.Second

// autoReset spends a reset of one of cands' Codex accounts, c — the last
// one, just out of its allowance — first: only when every other one of
// them sits out too. It says which account it was and what spending did.
func (s *Server) autoReset(ctx context.Context, cands []candidate, c candidate) (candidate, provider.ResetOutcome, bool) {
	for _, x := range cands {
		if x.restKey() == c.restKey() {
			continue
		}
		_, resting := restOf(x.restKey())
		if !resting && x.restID() != x.restKey() {
			_, resting = restOf(x.restID())
		}
		if !resting {
			return candidate{}, provider.ResetOutcome{}, false
		}
	}
	seen := map[string]bool{}
	for _, x := range append([]candidate{c}, cands...) {
		a := x.p.Account
		if a == nil || a.User == "" || seen[x.restKey()] || !provider.AutoResets(a.Agent, a.User) {
			continue
		}
		seen[x.restKey()] = true
		if out, ok := autoResetOf(ctx, a.Agent, a.User); ok {
			s.Unrest(x.restKey())
			return x, out, true
		}
	}
	return candidate{}, provider.ResetOutcome{}, false
}

// autoResetSignedIn is autoReset for a request relayed with Codex's own
// sign-in, the only account there is.
func (s *Server) autoResetSignedIn(ctx context.Context) (string, provider.ResetOutcome, bool) {
	who, ok := provider.CodexSignedIn()
	if !ok || !provider.CodexAutoReset(who) {
		return "", provider.ResetOutcome{}, false
	}
	out, ok := autoResetOf(ctx, "codex", who)
	if ok {
		s.Unrest("codex@" + strings.ToLower(who))
	}
	return who, out, ok
}

// autoResetOf spends one of agent's account user's resets if its rules
// let it, and says whether the windows started again.
func autoResetOf(ctx context.Context, agent, user string) (provider.ResetOutcome, bool) {
	ctx, cancel := context.WithTimeout(ctx, autoResetTimeout)
	defer cancel()
	out, err := provider.AutoUseCodexReset(ctx, user)
	switch {
	case err != nil:
		log.Printf("%s reset for %s not used: %v", agent, user, err)
	case out.Code == "reset":
		log.Printf("%s reset used for %s, its week used up: %s", agent, user, out.Text())
	case out.Code != "":
		log.Printf("%s reset for %s not used: %s", agent, user, out.Text())
	}
	return out, err == nil && out.Code == "reset"
}
