package provider

// An account's name of the user's own (#1515, aindijrncom): a team's GLM
// seats are told apart by who holds each, while the vendor names a seat
// by a masked email (31****36@qq.com) or not at all. The name is kept in
// providers.json beside the account's other settings (AccountNames), by
// the account's name in lower case; the vendor's or plugin's sign-in is
// never written to. It is shown wherever the account is: the Usage page,
// the menu bar, magpie accounts, magpie quota and /v1/magpie/quotas
// (SubscriptionQuota.Alias, Quota.Alias).

import (
	"cmp"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxAccountName bounds an account's name, in characters.
const MaxAccountName = 64

// cleanAccountName is a name as it is kept: spaces trimmed and run
// together, no control characters; "" for none.
func cleanAccountName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// normalAccountNames is m as it is kept: keys in lower case, names
// cleaned, accounts with none left out; nil for none.
func normalAccountNames(m map[string]string) map[string]string {
	var out map[string]string
	for u, v := range m {
		u, v = accountKey(u), cleanAccountName(v)
		if u == "" || v == "" || utf8.RuneCountInString(v) > MaxAccountName {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[u] = v
	}
	return out
}

// AccountName is the name the user gave the account user of p, "" when
// none. As with AccountCap, an account made afresh beside the agent's own
// reads it from providers.json.
func (p Provider) AccountName(user string) string {
	names := p.AccountNames
	if names == nil {
		if s, ok := storedPicks(p.ID); ok {
			names = s.AccountNames
		}
	}
	return names[accountKey(user)]
}

// AccountNameOf is the name the user gave the account user of the
// subscription id, "" when none.
func AccountNameOf(id, user string) string {
	if user == "" {
		return ""
	}
	if s, ok := storedPicks(id); ok {
		return s.AccountNames[accountKey(user)]
	}
	return ""
}

// SetAccountName names the account ref of the subscription id; "" takes
// the name off.
func SetAccountName(id, ref, name string) error {
	name = cleanAccountName(name)
	if n := utf8.RuneCountInString(name); n > MaxAccountName {
		return fmt.Errorf("an account's name is at most %d characters, not %d", MaxAccountName, n)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not signed-in accounts; a key is named in its own row", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	k := accountKey(r)
	if p.AccountNames[k] == name {
		return nil
	}
	names := map[string]string{}
	for u, v := range p.AccountNames {
		names[u] = v
	}
	if name == "" {
		delete(names, k)
	} else {
		names[k] = name
	}
	p.AccountNames = names
	return Save(*p)
}

// withAccountNames is qs with each account's name of the user's own on
// its card, else the name of the key plan it stands for (Seat), as
// Alias. qs is not changed: it is a cache's.
func withAccountNames(qs []SubscriptionQuota) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(qs))
	for i, q := range qs {
		if q.From == "" {
			q.Alias = cmp.Or(AccountNameOf(q.Provider, q.User), q.Seat)
		}
		out[i] = q
	}
	return out
}
