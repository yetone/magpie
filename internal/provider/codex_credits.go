package provider

// Whether a Codex account spends its credits. A ChatGPT account that holds
// credits (#571) keeps answering once a usage window is used up: the vendor
// spends the credits instead, and a request routed to it goes through. That
// is the default, and it carries a task on when the allowance runs out.
// Turned off for an account, routing holds it as used up at 100% of a
// window, as a usage cap holds it at its share (account_caps.go): never
// tried till the window renews, so the user's other accounts, groups and
// fallbacks take the request, and none spends the credits. An account with
// no credits is refused by the vendor at 100% either way; the switch only
// spares it the request.
//
// It doesn't change which account Codex is signed in to: NextLogin moves
// off an account at its routing's spent share (100% at most) and onto none
// past it, credits or not. Unlike the cap, it holds at the vendor's own
// 100%, so an account that spends its resets by itself spends one there
// (the gateway's autoReset) where a cap never does.

import (
	"slices"

	"github.com/yetone/magpie/internal/settings"
)

// CodexCredits says whether the Codex account user spends its credits
// once a usage window is used up: true unless the user turned it off.
func CodexCredits(user string) bool {
	return !slices.Contains(settings.Load().CodexNoCredits, accountKey(user))
}

// SetCodexCredits turns that on or off for user.
func SetCodexCredits(user string, on bool) error {
	user = accountKey(user)
	s := settings.Load()
	s.CodexNoCredits = slices.DeleteFunc(s.CodexNoCredits, func(u string) bool { return u == user })
	if !on && user != "" {
		s.CodexNoCredits = append(s.CodexNoCredits, user)
	}
	return settings.Save(s)
}

// HoldShare is the share of its windows (in percent) at which routing
// holds the account user of p, of agent, as used up: its usage cap when it
// has one, 100 for a Codex account set not to spend its credits, 0 when
// nothing holds it before the vendor does. noCredits says it is the credits
// that hold it.
func HoldShare(p Provider, agent, user string) (share int, noCredits bool) {
	if c := p.AccountCap(user); c > 0 {
		return c, false
	}
	if agent == "codex" && user != "" && !CodexCredits(user) {
		return 100, true
	}
	return 0, false
}
