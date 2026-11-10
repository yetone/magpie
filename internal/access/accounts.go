package access

import (
	"cmp"
	"errors"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// CleanAccounts is a key's account list as kept (#905): each entry
// "<provider>/<account>", an account named by its stable id — or by the
// name it is shown by, resolved to that id, so the command line reads
// while the list keeps what a rename doesn't move — and a key by its
// fingerprint; trimmed, told apart in lower case as accounts are, each
// once, in the order given. Every account the entry names must be one
// its provider has now, so a typo can't quietly hold a key to nothing;
// one gone later — a key rotated, an account signed out — is kept as it
// is, matching nothing.
//
// An entry "<provider>/!<account>" keeps that account out instead
// (LeiWang_2000 on X): the key may use every other account, those signed
// in later too. An account is never both kept out and named.
//
// An entry "@<group>" uses an account group's accounts, and "!@<group>"
// keeps them out (AccountGroup, LeiWang_2000 on X): the group named by
// its id or name, kept by its id, so a group renamed or changed is the
// key's as it is then.
func CleanAccounts(in []string) ([]string, error) {
	var cleaned []string
	var groups []AccountGroup
	groupsRead := false
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if rest, _ := strings.CutPrefix(a, ExcludeMark); strings.HasPrefix(rest, GroupMark) {
			if !groupsRead {
				var err error
				if groups, err = loadGroups(); err != nil {
					return nil, err
				}
				groupsRead = true
			}
			kept, err := cleanGroupEntry(groups, a)
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(cleaned, func(o string) bool { return strings.EqualFold(o, kept) }) {
				cleaned = append(cleaned, kept)
			}
			continue
		}
		pid, ref, ok := strings.Cut(a, "/")
		if !ok || pid == "" || ref == "" {
			return nil, errors.New("A key's account is <provider>/<account> or <provider>/<key id>, like codex/me@example.com: " + a)
		}
		if len(a) > 300 {
			return nil, errors.New("A key's account is at most 300 characters")
		}
		ref, excl := strings.CutPrefix(ref, ExcludeMark)
		if ref = strings.TrimSpace(ref); ref == "" {
			return nil, errors.New("A key's account is <provider>/<account> or <provider>/<key id>, like codex/me@example.com: " + a)
		}
		kept, err := provider.ResolveAccount(pid, ref)
		if err != nil {
			return nil, err
		}
		if excl {
			p, r, _ := strings.Cut(kept, "/")
			kept = p + "/" + ExcludeMark + r
		}
		if !slices.ContainsFunc(cleaned, func(o string) bool { return strings.EqualFold(o, kept) }) {
			cleaned = append(cleaned, kept)
		}
	}
	out := cleaned
	for _, a := range out {
		if p, r, ok := excludedEntry(a); ok && slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, p+"/"+r) }) {
			return nil, errors.New("A key's account is either used or kept out, not both: " + p + "/" + r)
		}
		if id, kept, ok := groupEntry(a); ok && kept && slices.Contains(out, GroupMark+id) {
			name := id
			if i := findGroup(groups, id); i >= 0 {
				name = groups[i].Name
			}
			return nil, errors.New("An account group is either used or kept out, not both: " + name)
		}
	}
	if len(out) > 500 {
		return nil, errors.New("A key can list at most 500 accounts")
	}
	return out, nil
}

// ExcludeMark starts an entry's account to keep the key off it:
// "<provider>/!<ref>" (LeiWang_2000 on X). A magpie older than the mark
// reads it as an account no provider has, which holds the key to none of
// that provider's: it fails closed.
const ExcludeMark = "!"

// excludedEntry is the provider and account of an entry that keeps an
// account out, "<provider>/!<ref>".
func excludedEntry(a string) (pid, ref string, ok bool) {
	pid, ref, cut := strings.Cut(strings.TrimSpace(a), "/")
	if ref, ok = strings.CutPrefix(ref, ExcludeMark); !cut || !ok || ref == "" {
		return "", "", false
	}
	return pid, ref, true
}

// Excluded says the entry keeps an account out, "<provider>/!<ref>".
func Excluded(a string) bool {
	_, _, ok := excludedEntry(a)
	return ok
}

// picksAccounts says some entry names an account to use, not only ones
// kept out.
func picksAccounts(accounts []string) bool {
	return slices.ContainsFunc(accounts, func(a string) bool { return !Excluded(a) })
}

// AllowsAccount says the provider pid's account or key ref is among the
// key's accounts — "<provider>/<ref>", told apart in lower case, as
// accounts are. The list holds a key to some accounts **of the providers
// it names**: a provider it names no account of, the key uses as it
// always did, its every account and key. Any, for a key that lists none.
// An account the list keeps out ("<provider>/!<ref>") is never one, and
// keeps the key off no other: every account its provider signs in later
// is one the key may use.
func (who Identity) AllowsAccount(pid, ref string) bool {
	if len(who.Accounts) == 0 {
		return true
	}
	if slices.Contains(who.Accounts, heldToNothing) {
		return false // an account group gone, or with no account (expandGroups)
	}
	pid = strings.ToLower(strings.TrimSpace(pid))
	ref = strings.ToLower(strings.TrimSpace(ref))
	key := pid + "/" + ref
	named, used := false, false
	for _, a := range who.Accounts {
		a = strings.ToLower(strings.TrimSpace(a))
		if p, r, ok := excludedEntry(a); ok {
			if p == pid && r == ref && ref != "" {
				return false
			}
			continue
		}
		if a == key {
			used = true
		} else if p, _, ok := strings.Cut(a, "/"); ok && p == pid {
			named = true
		}
	}
	return used || !named
}

// heldToNone is the account an entry "<provider>/<heldToNone>" names: none
// a provider has, so the list names the provider and allows none of it.
const heldToNone = "\x00"

// HeldToGroup is who held to a routing group's accounts too (accounts,
// a Group's Accounts, in the key's form): a provider the group names is
// used only on the accounts the group names that the key may use as well,
// none when the two share none, never one either leaves out; a provider
// only the key names, on the key's; one neither names, on every account
// it has. Group is set to the group's name, for the error to say whose
// accounts held the request. A group naming no account is who as it is.
// Everything that holds a request to its key's accounts
// (AllowsAccount) holds it to the group's then, by the one rule.
func (who Identity) HeldToGroup(group string, accounts []string) Identity {
	named := map[string][]string{} // a provider the group names, in lower case: its entries as written
	var order []string
	for _, a := range accounts {
		a = strings.TrimSpace(a)
		pid, ref, ok := strings.Cut(a, "/")
		if pid = strings.ToLower(pid); !ok || pid == "" || ref == "" {
			continue
		}
		if _, seen := named[pid]; !seen {
			order = append(order, pid)
		}
		named[pid] = append(named[pid], a)
	}
	if len(order) == 0 {
		return who
	}
	var out []string
	for _, a := range who.Accounts {
		if pid, _, _ := strings.Cut(strings.TrimSpace(a), "/"); named[strings.ToLower(pid)] == nil {
			out = append(out, a)
		}
	}
	for _, pid := range order {
		some := false
		for _, a := range named[pid] {
			_, ref, _ := strings.Cut(a, "/")
			if who.AllowsAccount(pid, ref) && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, a) }) {
				out, some = append(out, a), true
			}
		}
		if !some {
			out = append(out, pid+"/"+heldToNone)
		}
	}
	who.Accounts, who.Group = out, group
	return who
}

// AccountsShown is a key's accounts as words say them: those it may use
// by names[entry] (provider.AccountNames), else the entry itself, and
// those it keeps out as "every account but …" (LeiWang_2000 on X).
func AccountsShown(names map[string]string, accounts []string) string {
	var used, out []string
	var groups map[string]string
	for _, a := range accounts {
		if id, kept, ok := groupEntry(a); ok {
			if groups == nil {
				groups = GroupNames()
			}
			g := "group " + cmp.Or(groups[GroupMark+id], GroupMark+id)
			if kept {
				out = append(out, g)
			} else {
				used = append(used, g)
			}
			continue
		}
		if p, r, ok := excludedEntry(a); ok {
			out = append(out, cmp.Or(names[p+"/"+r], p+"/"+r))
		} else {
			used = append(used, cmp.Or(names[a], a))
		}
	}
	var parts []string
	if len(used) > 0 {
		parts = append(parts, strings.Join(used, ", "))
	}
	if len(out) > 0 {
		parts = append(parts, "every account but "+strings.Join(out, ", "))
	}
	return strings.Join(parts, "; ")
}

// ShownAccounts is who's accounts as a message lists them: the entries
// HeldToGroup adds to name a provider with none of its accounts left out.
// An account kept out stays "<provider>/!<ref>".
func (who Identity) ShownAccounts() []string {
	return slices.DeleteFunc(slices.Clone(who.Accounts), func(a string) bool { return strings.HasSuffix(a, "/"+heldToNone) || a == heldToNothing })
}

// ReplaceAccount moves every key's account list from one of provider
// pid's keys to the fingerprint it has now (#1869: the key's value
// replaced in place), so a key held to it is held to it still. It
// returns how many keys it moved.
func ReplaceAccount(pid, was, now string) (int, error) {
	if was == now {
		return 0, nil
	}
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return 0, err
	}
	old := strings.ToLower(pid + "/" + was)
	oldOut := strings.ToLower(pid + "/" + ExcludeMark + was)
	moved := 0
	for i := range keys {
		for j, a := range keys[i].Accounts {
			switch strings.ToLower(strings.TrimSpace(a)) {
			case old:
				keys[i].Accounts[j] = pid + "/" + now
				moved++
			case oldOut:
				keys[i].Accounts[j] = pid + "/" + ExcludeMark + now
				moved++
			}
		}
	}
	// and in the account groups, which keys name (LeiWang_2000 on X)
	gs, err := loadGroups()
	if err != nil {
		return 0, err
	}
	inGroups := 0
	for i := range gs {
		for j, a := range gs[i].Accounts {
			if strings.ToLower(strings.TrimSpace(a)) == old {
				gs[i].Accounts[j] = pid + "/" + now
				inGroups++
			}
		}
	}
	if inGroups > 0 {
		if err := saveGroups(gs); err != nil {
			return 0, err
		}
	}
	if moved == 0 {
		return inGroups, nil
	}
	return moved + inGroups, save(keys)
}
