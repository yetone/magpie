package provider

// Whether a Codex account spends its credits. A ChatGPT account that holds
// credits (#571) keeps answering once a usage window is used up: the vendor
// spends the credits instead, and a request routed to it goes through. That
// is the default, and it carries a task on when the allowance runs out.
// Turned off for an account, routing holds it as used up at 100% of a
// window, as a usage cap holds it at its share (account_caps.go): never
// tried till the window renews, so the user's other accounts, groups and
// fallbacks take the request rather than it spending the credits. The hold
// goes by magpie's latest reading (Allowances, read again about every
// minute), so a request in the minute after a window fills may still reach
// the account. An account with
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

// HoldCaps is the shares of its windows at which routing holds the
// account user of p, of agent, as used up: its usage cap and its windows'
// own (account_caps.go), and for a Codex account set not to spend its
// credits 100% of any window with no cap. Holds() is false when nothing
// holds it before the vendor does.
func HoldCaps(p Provider, agent, user string) WindowCaps {
	c := p.CapsOf(user)
	c.Credits = agent == "codex" && user != "" && !CodexCredits(user)
	return c
}
