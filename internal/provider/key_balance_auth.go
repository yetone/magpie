package provider

// One relay, keys of two accounts (Kylin_Cloud on X: a New API site's
// keys from two accounts in one provider, both showing one account's
// balance). The provider's access token and user id (BalanceToken,
// New-Api-User) are one account's, so every key's card asked that
// account once. A key can carry its own: the access token (a sub2api
// panel's login token) and user id of the account it belongs to, which
// its card is asked with in place of the provider's. A key without them
// keeps the provider's, or its own key-alone query when the provider has
// no token.

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"strings"
)

// KeyBalanceAuth is what one key's account balance is asked with, where
// the provider's BalanceToken is another account's: the account's access
// token, and for a new-api relay the user's id, sent in New-Api-User.
type KeyBalanceAuth struct {
	Token string `json:"token"`
	User  string `json:"user,omitempty"`
}

// KeyBalanceAuthInfo is a key's own balance query as its row tells it:
// the user id, and whether a token is saved (the token itself never goes
// back to the page).
type KeyBalanceAuthInfo struct {
	User  string `json:"user,omitempty"`
	Token bool   `json:"token"`
}

// keyBalanceAuthOf is the key's own balance query, by its KeyID.
func (p Provider) keyBalanceAuthOf(key string) (KeyBalanceAuth, bool) {
	if key == "" || len(p.KeyBalanceAuth) == 0 {
		return KeyBalanceAuth{}, false
	}
	a, ok := p.KeyBalanceAuth[keyID(key)]
	return a, ok && a.Token != ""
}

// keyBalanceAuthInfo is the key's own balance query for its row; nil with
// none.
func (p Provider) keyBalanceAuthInfo(key string) *KeyBalanceAuthInfo {
	a, ok := p.keyBalanceAuthOf(key)
	if !ok {
		return nil
	}
	return &KeyBalanceAuthInfo{User: a.User, Token: true}
}

// TakesKeyBalanceAuth says a key of p can carry its own balance query: a
// provider whose vendor tells the account's balance to a token
// (TakesBalanceToken), or a relay added by hand whose query magpie looks
// for (balance_probe.go).
func TakesKeyBalanceAuth(p Provider) bool {
	if p.Account != nil {
		return false
	}
	if TakesBalanceToken(p) {
		return true
	}
	q := p
	q.NoBalanceProbe = false
	return probesBalance(q)
}

// SetKeyBalanceAuth gives the key ref (its id or name) of the provider id
// its own account's access token and user id, for its balance. A blank
// token keeps the saved one, so the user id alone can be changed; clear
// drops both, and the key is asked as the provider's other keys are.
func SetKeyBalanceAuth(id, ref, token, user string, clear bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: an access token is for an API key's balance", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no key %q", p.Name, ref)
	}
	k := strings.ToLower(r)
	m := maps.Clone(p.KeyBalanceAuth)
	if m == nil {
		m = map[string]KeyBalanceAuth{}
	}
	if clear {
		if _, had := m[k]; !had {
			return nil
		}
		delete(m, k)
	} else {
		a := m[k]
		if t := strings.TrimSpace(token); t != "" {
			a.Token = t
		}
		a.User = strings.TrimSpace(user)
		if a.User != "" && !allDigits(a.User) {
			return fmt.Errorf("a user ID is the number the site shows in its personal settings, not %q", a.User)
		}
		if a.Token == "" {
			return fmt.Errorf("paste the access token of the account this key belongs to")
		}
		m[k] = a
	}
	p.KeyBalanceAuth = m
	if err := Save(*p); err != nil {
		return err
	}
	ForgetBalances()
	return nil
}

// normalKeyBalanceAuth is m as it is kept: key ids in lower case, only
// the keys p has, each with a token; nil for none.
func normalKeyBalanceAuth(m map[string]KeyBalanceAuth, p Provider) map[string]KeyBalanceAuth {
	if len(m) == 0 {
		return nil
	}
	have := map[string]bool{}
	if p.Key != "" {
		have[keyID(p.Key)] = true
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			have[keyID(k.Key)] = true
		}
	}
	var out map[string]KeyBalanceAuth
	for k, a := range m {
		k = strings.ToLower(strings.TrimSpace(k))
		a.Token, a.User = strings.TrimSpace(a.Token), strings.TrimSpace(a.User)
		if !have[k] || a.Token == "" {
			continue
		}
		if out == nil {
			out = map[string]KeyBalanceAuth{}
		}
		out[k] = a
	}
	return out
}

// withBalanceAuth is q, whose key is one of its keys, asking its balance
// with a: the token in place of the provider's, and the user id in
// New-Api-User in place of the provider's (none when a names none: the
// provider's is another account's, which the relay refuses beside this
// token).
func withBalanceAuth(q Provider, a KeyBalanceAuth) Provider {
	q.BalanceToken = a.Token
	h := maps.Clone(userIDHeader(q.Headers))
	for n := range h {
		if strings.EqualFold(n, NewAPIUserHeader) {
			delete(h, n)
		}
	}
	if a.User != "" {
		if h == nil {
			h = map[string]string{}
		}
		h[NewAPIUserHeader] = a.User
	}
	q.Headers = h
	return q
}

// keyAccountQuery is q, asked with a key's own token (withBalanceAuth),
// at its panel's account query where the provider asks a key's: the
// Balance URL the user named, or the one found at a relay added by hand
// (balance_probe.go, looked for with the key alone first). A key query
// never takes a token (errKeyUsageWithToken), and the token was given
// for the account's balance. The account query's own field is read,
// unless the Balance URL already named an account query.
func keyAccountQuery(ctx context.Context, q Provider) Provider {
	raw := strings.TrimSpace(q.BalanceURL)
	if raw == "" {
		plain := q
		plain.BalanceToken = ""
		if !probesBalance(plain) {
			return q
		}
		u, found := probeBalance(ctx, plain)
		if !found {
			return q
		}
		raw = u
		q.BalancePath = ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return q
	}
	path := strings.TrimRight(u.Path, "/")
	for _, t := range tokenQueries {
		base, ok := strings.CutSuffix(path, t.key)
		if !ok {
			continue
		}
		u.Path, u.RawPath, u.RawQuery, u.Fragment = base+t.account, "", "", ""
		q.BalanceURL, q.BalancePath = u.String(), ""
		return q
	}
	q.BalanceURL = raw
	return q
}

// hasKeyBalanceAuth says a key p has in use carries its own balance
// query.
func hasKeyBalanceAuth(p Provider) bool {
	if len(p.KeyBalanceAuth) == 0 {
		return false
	}
	if _, ok := p.keyBalanceAuthOf(p.Key); ok {
		return true
	}
	for _, k := range p.Keys {
		if _, ok := p.keyBalanceAuthOf(k.Key); ok && !k.Off {
			return true
		}
	}
	return false
}

type keyBalanceJob struct {
	p       Provider
	user    string
	account bool
}

// keyBalanceAuthJobs are the balance cards of p, some of whose keys carry
// their own account's token: one per account asked with a token, the
// keys of one account sharing its card as the provider's keys share the
// provider's token, and one per key asked with the key alone. A card is
// named by its first key when there is more than one, as each key's is.
// known: p's balance query is one magpie knows (not one a relay added by
// hand is looked for at), whose keys on a plan without it have no card.
func keyBalanceAuthJobs(p Provider, known bool) []keyBalanceJob {
	type card struct {
		keyBalanceJob
		group string
	}
	var cards []card
	add := func(key, name, plan string) {
		q := p.atKeyPlan(plan)
		q.Key = key
		group, account := "key\x00"+keyID(key), false
		if a, ok := p.keyBalanceAuthOf(key); ok {
			q = withBalanceAuth(q, a)
			group, account = "own\x00"+a.Token+"\x00"+a.User, true
		} else if src, ok := balanceSourceOf(q); ok && src.token != "" {
			group = "provider"
		}
		if known && !hasBalanceSource(q) {
			return
		}
		for _, c := range cards {
			if c.group == group {
				return
			}
		}
		if name == "" {
			name = Mask(key)
		}
		cards = append(cards, card{keyBalanceJob{q, name, account}, group})
	}
	add(p.Key, p.KeyName, p.KeyPlan)
	for _, k := range p.Keys {
		if k.Off || k.Key == "" || k.Key == p.Key {
			continue
		}
		add(k.Key, k.Name, k.Plan)
	}
	out := make([]keyBalanceJob, len(cards))
	for i, c := range cards {
		out[i] = c.keyBalanceJob
		if len(cards) == 1 {
			out[i].user = ""
		}
	}
	return out
}
