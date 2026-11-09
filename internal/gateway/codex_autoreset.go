package gateway

// A Codex rate-limit reset spent by itself: when everyone a request could
// go to is out of their allowance, a ChatGPT account the user lets spend
// its resets, its weekly window used up, spends one and is asked again
// (provider.AutoUseCodexReset has the rules).

import (
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// autoResetTimeout bounds the look at an account's week and the reset.
var autoResetTimeout = 20 * time.Second

// autoReset spends a reset of one of cands' Codex accounts, c — the last
// one, just out of its allowance — first: only when every other one of
// them sits out too. held are accounts left out as they won't spend their
// credits (planned.held): they sit out, and are out of their allowance as
// c is, so one of them may spend one after cands'. It says which account it
// was and what spending did.
func (s *Server) autoReset(ctx context.Context, cands []candidate, c candidate, held []candidate) (candidate, provider.ResetOutcome, bool) {
	// spent before a route is begun: a wait magpie knows of (unrouted.go)
	defer noteWait(ctx, "Codex reset")()
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
	for _, x := range append(append([]candidate{c}, cands...), held...) {
		a := x.p.Account
		if a == nil || a.User == "" || seen[x.restKey()] || !provider.AutoResetsWeek(a.Agent, a.User) {
			continue
		}
		seen[x.restKey()] = true
		if out, ok := autoResetOf(ctx, a.Agent, a.User); ok {
			s.Unrest(x.restKey())
			x.capped = nil // its windows started again: held no more
			return x, out, true
		}
	}
	return candidate{}, provider.ResetOutcome{}, false
}

// autoResetSignedIn is autoReset for a request relayed with Codex's own
// sign-in, the only account there is.
func (s *Server) autoResetSignedIn(ctx context.Context) (string, provider.ResetOutcome, bool) {
	who, ok := provider.CodexSignedIn()
	if !ok || !provider.AutoResetsWeek("codex", who) {
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

// unhold moves the account c of a plan's left, held as it won't spend its
// credits, to its order: a reset started its windows again, and it is
// tried.
func unhold(order, left []Weighed, c candidate) ([]Weighed, []Weighed) {
	i := slices.IndexFunc(left, func(w Weighed) bool { return w.ID == c.rest && w.NoCredits })
	if i < 0 {
		return order, left
	}
	w := left[i]
	w.Unlisted, w.Capped, w.CapBack, w.NoCredits = false, 0, nil, false
	w.CapAmount, w.CapCount, w.CapUnit = 0, 0, ""
	return append(slices.Clip(order), w), slices.Delete(slices.Clone(left), i, i+1)
}

// resetBeforeCredits spends one of c's resets before a request goes to it
// on its credits (tongzhenbao on X): a Codex account set to spend one by
// itself when its week runs out, its week used up by its last reading,
// and let spend its credits. ChatGPT answers such an account on its
// credits, so it never refuses it as out of its allowance, and autoReset,
// which runs on that refusal, never ran: the week's reset was left
// unspent while the credits were. A reset is free and credits cost money,
// so the reset goes first; once none is held, or the week's one is spent,
// the credits go on as before. AutoUseCodexReset reads the week again
// before spending, one a week at most.
func (s *Server) resetBeforeCredits(ctx context.Context, c candidate) (provider.ResetOutcome, bool) {
	a := c.p.Account
	if a == nil || a.User == "" || !provider.AutoResetsWeek(a.Agent, a.User) {
		return provider.ResetOutcome{}, false
	}
	if al, ok := c.allowance(); !ok || !spendsCredits(c.p, al) || !al.WeekUsedUp(time.Now()) {
		return provider.ResetOutcome{}, false
	}
	out, ok := autoResetOf(ctx, a.Agent, a.User)
	if ok {
		s.Unrest(c.restKey())
	}
	return out, ok
}

// resetBeforeCreditsOn is resetBeforeCredits for a turn relayed on on, or
// on Codex's own sign-in when on is nil; who is the account.
func (s *Server) resetBeforeCreditsOn(ctx context.Context, on *provider.Provider) (who string, out provider.ResetOutcome, ok bool) {
	if on != nil {
		if on.Account == nil {
			return "", provider.ResetOutcome{}, false
		}
		out, ok = s.resetBeforeCredits(ctx, candidate{p: *on, rest: on.ID})
		return on.Account.User, out, ok
	}
	who, signed := provider.CodexSignedIn()
	if !signed || !provider.AutoResetsWeek("codex", who) || !relayOnCredits(nil) || !allowances("codex")[who].WeekUsedUp(time.Now()) {
		return "", provider.ResetOutcome{}, false
	}
	if out, ok = autoResetOf(ctx, "codex", who); ok {
		s.Unrest("codex@" + strings.ToLower(who))
	}
	return who, out, ok
}
