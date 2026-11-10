package provider

// Keeping Codex or Claude Code signed in to an account of the user's
// choosing (#524). Making an account first did two things at once: the
// gateway tries it first, and the agent is signed in to it. Kept on the
// first (KeepLogin), magpie only stopped moving the sign-in on. But the
// first is often just the account whose allowance is to be spent first,
// not the one the user uses the agent as: KeepLoginAs keeps the agent
// signed in to that one, wherever it stands in the order, and the order —
// which a drag or Make first sets — is the gateway's alone.

import (
	"fmt"
	"slices"
	"strings"
)

// keptAs is the account agent is kept signed in to in place of the first
// (Provider.KeepLoginAs), "" when there is none or it is no longer saved.
func keptAs(agent string) string {
	if !slices.Contains(switchedAgents, agent) {
		return ""
	}
	for _, p := range storedFile().Providers {
		if p.ID != agent {
			continue
		}
		if !p.keepsLogin() || p.KeepLoginAs == "" {
			return ""
		}
		for _, l := range Logins(agent) {
			if strings.EqualFold(l.User, p.KeepLoginAs) {
				return l.User
			}
		}
	}
	return ""
}

// LoginRanks is, for the account Codex or Claude Code is signed in to while
// it is kept signed in to one of the user's choosing (KeepLoginAs), each of
// the agent's accounts' place in the order the gateway tries them, the one
// signed in to at its own place among them; nil otherwise, when the one
// signed in to is the first.
func (p Provider) LoginRanks() map[string]int {
	if p.Account == nil || p.Account.token != nil && !p.Account.standIn || !slices.Contains(switchedAgents, p.Account.Agent) ||
		!p.keepsLogin() || p.KeepLoginAs == "" {
		return nil
	}
	ranks := map[string]int{}
	kept := false
	for i, l := range Logins(p.Account.Agent) {
		ranks[strings.ToLower(l.User)] = i
		kept = kept || strings.EqualFold(l.User, p.KeepLoginAs)
	}
	if !kept {
		return nil
	}
	return ranks
}

// SetKeepLoginAs keeps Codex or Claude Code signed in to user, one of its
// saved accounts, and signs it in to it now; the order the gateway tries
// the accounts in stays as it was, the first still first.
func SetKeepLoginAs(id, user string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil || p.Account.Agent != p.ID || !slices.Contains(switchedAgents, p.ID) {
		return fmt.Errorf("magpie doesn't sign %s in to another of its accounts", p.Name)
	}
	if p.SwitchOnly {
		return errSwitchOnly(*p)
	}
	ls := Logins(p.ID)
	i := slices.IndexFunc(ls, func(l Login) bool { return strings.EqualFold(l.User, user) })
	if i < 0 {
		return fmt.Errorf("no saved %s account %q", p.ID, user)
	}
	to := ls[i]
	if to.Lapsed != "" {
		return fmt.Errorf("%s has to be signed in to again first", to.User)
	}
	if to.Token {
		return fmt.Errorf(errClaudeTokenSwitch, to.User)
	}
	if to.AccessOnly {
		return fmt.Errorf(errCodexAccessOnlySwitch, to.User)
	}
	if !p.KeepLogin || p.KeepLoginAs == "" {
		// the order the gateway goes by now — the one signed in to
		// first — is kept as the accounts' own, before the sign-in moves
		order := make([]string, 0, len(ls))
		for _, l := range ls {
			if l.Active {
				order = append(order, l.User)
			}
		}
		for _, l := range ls {
			if !l.Active {
				order = append(order, l.User)
			}
		}
		if err := rankLogins(p.ID, order); err != nil {
			return err
		}
	}
	p.KeepLogin, p.KeepLoginAs = true, to.User
	if err := Save(*p); err != nil {
		return err
	}
	if to.Active {
		return nil
	}
	return SwitchLogin(p.ID, to.User)
}

// keepsLogin reports whether magpie keeps the agent signed in to one
// account (KeepLogin, KeepLoginAs). Not while it only switches the agent's
// sign-in (SwitchOnly): that asks for the sign-in to move to the next
// account with room, and kept on one, nothing would ever move it, nor any
// request fail over through the gateway (#1907: both ticked, Codex stayed
// on an account at 0% left). The choice is kept, and holds again once
// switch-only is unticked.
func (p Provider) keepsLogin() bool { return p.KeepLogin && !p.SwitchOnly }

// errSwitchOnly refuses to keep p's agent on one account while magpie only
// switches its sign-in (keepsLogin).
func errSwitchOnly(p Provider) error {
	return fmt.Errorf("magpie only switches %s's sign-in to the next account with room; untick that to keep it signed in to one account", p.Name)
}

// rankLogins writes order as the order of agent's saved accounts.
func rankLogins(agent string, order []string) error {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent != agent {
			continue
		}
		if r := slices.IndexFunc(order, func(u string) bool { return strings.EqualFold(u, ls[i].User) }); r >= 0 {
			ls[i].Order = r + 1
		}
	}
	return writeLogins(ls)
}

// signInToFirst signs agent in to the first of its accounts in use, as the
// gateway orders them, when it is on another: let go of one of the user's
// choosing, the one signed in to is the first again.
func signInToFirst(agent string) error {
	for _, l := range Logins(agent) {
		if (l.Active || l.On) && !l.Paused && l.Lapsed == "" && !l.Token && !l.AccessOnly {
			if l.Active {
				return nil
			}
			return SwitchLogin(agent, l.User)
		}
	}
	return nil
}
