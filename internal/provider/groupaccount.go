package provider

// A group's member may name one account or key of its provider
// (EastonXvii on X): "codex/gpt-6-astra#<account>". A request routed to
// that member is sent on that account alone, never spilling over to the
// provider's others, and the next member takes over when it can't
// answer. Group.Accounts holds the whole group to a set of accounts, with
// no order between them; a member's own account puts each account where
// the user wants it in the group's order:
//
//	codex/gpt-6-astra#A → codex/gpt-6-astra#B → codex/gpt-6.1-sol#C
//
// The account is part of the member's id, last, after the effort, as the
// effort is (groupeffort.go): the same model on two accounts is two
// members, which rules, weights, fast, off and the trace tell apart, and
// the vendor is asked for the model's own id. A member without one goes
// over every account of its provider the group's Accounts allow, as
// before, so a group saved before reads the same.
//
// Kept, the account is its stable id (ResolveAccount: a signed-in
// account's, kept through renames, a key's fingerprint), so "#" and the
// id are what a member's id ends with; typed, it may be the name the
// account is shown by. No model id magpie has met has a "#" in it.

import (
	"fmt"
	"strings"
)

// AccountMark puts a member's account after its model: "a/m:high#<id>".
const AccountMark = "#"

// MemberAccount splits a group's member into the member without its
// account and the account it names ("" for none): "a/m:high#x" is a/m:high
// on x. A group in the group ("group/<id>") is split too, for SaveGroup to
// refuse: its members name their own.
func MemberAccount(id string) (member, account string) {
	i := strings.LastIndex(id, AccountMark)
	if i <= 0 {
		return id, ""
	}
	acct := strings.TrimSpace(id[i+1:])
	if acct == "" || strings.Contains(acct, "/") {
		return id, ""
	}
	return id[:i], acct
}

// WithMemberAccount is member on account: the member itself for "".
func WithMemberAccount(member, account string) string {
	if account == "" {
		return member
	}
	return member + AccountMark + account
}

// memberAccountKept is the account a member typed names, as it is kept:
// the stable id of the account or key of the member's provider it names
// (ResolveAccount), or ref as typed when the provider has none such — a
// member saved before whose account was signed out since stays on it,
// matching nothing, rather than let loose on every account. ok is false
// for one that names no account, for SaveGroup to refuse when new.
func memberAccountKept(model, ref string) (kept string, ok bool) {
	pid, _, found := strings.Cut(model, "/")
	if !found {
		return ref, false
	}
	r, err := ResolveAccount(pid, ref)
	if err != nil {
		return ref, false
	}
	_, id, _ := strings.Cut(r, "/")
	return id, true
}

// checkMemberAccounts refuses a member's account that names a group, and
// one magpie has no such account or key for, unless the group had that
// member already (was): an account signed out since keeps its members, as
// Group.Accounts keeps its entries.
func checkMemberAccounts(members, was []string) error {
	for _, m := range members {
		base, acct := MemberAccount(m)
		if acct == "" {
			continue
		}
		model, _ := MemberEffort(base)
		if IsGroupRef(model) || strings.HasPrefix(model, AutoPrefix) {
			return fmt.Errorf("%s is a group: its models are sent on the accounts it says, so it takes no account of its own (%s%s)", model, AccountMark, acct)
		}
		if _, ok := memberAccountKept(model, acct); ok {
			continue
		}
		known := false
		for _, w := range was {
			if strings.EqualFold(w, m) {
				known = true
				break
			}
		}
		if !known {
			pid, _, _ := strings.Cut(model, "/")
			if p, err := Find(pid); err == nil {
				return fmt.Errorf("%s has no account or key %q to send %s on", p.Name, acct, model)
			}
			return fmt.Errorf("magpie has no provider %q to send %s on account %q", pid, model, acct)
		}
	}
	return nil
}

// withoutMemberAccounts is g with no member naming an account of its own,
// each on every account again: for a group shared with another magpie,
// which has accounts of its own (ShareGroup, as Group.Accounts is
// dropped). Two members left the same are one.
func withoutMemberAccounts(g *Group) {
	for _, m := range g.Members {
		if base, acct := MemberAccount(m); acct != "" && !IsGroupRef(base) {
			g.RenameMember(m, base)
		}
	}
	g.Members, g.Fast, g.Ultra, g.Off, g.Placed = cleanList(g.Members), cleanList(g.Fast), cleanList(g.Ultra), cleanList(g.Off), cleanList(g.Placed)
}

// wasMembers are the members the stored group id has, those its patterns
// matched among them: nil for a new group.
func wasMembers(gs []Group, id string) []string {
	for _, g := range gs {
		if g.ID == id {
			return g.Members
		}
	}
	return nil
}
