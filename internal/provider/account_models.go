package provider

// An account's own models (#474): one account of several may be one the
// user keeps for some of the provider's models only — an account with a
// small allowance but a verified standing, kept for the cheap models so
// the big ones don't spend it. Its list is a subset of the provider's;
// an account without one serves every model the provider does, as before.
// The gateway never sends a model to an account whose list leaves it out
// (gateway/fallback.go).

import (
	"fmt"
	"slices"
	"strings"
)

// AccountServes says whether the account or key ref (a signed-in
// account's name, a key's KeyID) is let serve model: true when it has no
// list of its own.
func (p Provider) AccountServes(ref, model string) bool {
	ms, ok := p.AccountModels[accountKey(ref)]
	return !ok || len(ms) == 0 || slices.Contains(ms, model)
}

// AccountRefs are the provider's accounts, or keys, as AccountModels
// knows them: a signed-in account's name, a key's KeyID.
func (p Provider) AccountRefs() []string {
	var out []string
	if p.Account == nil {
		for _, k := range p.KeyList() {
			out = append(out, k.ID)
		}
		return out
	}
	agent := p.Account.Agent
	if p.IsPlugin() {
		agent = p.ID
	}
	if p.Account.User != "" {
		out = append(out, p.Account.User)
	}
	for _, l := range Logins(agent) {
		if !slices.ContainsFunc(out, func(u string) bool { return accountKey(u) == accountKey(l.User) }) {
			out = append(out, l.User)
		}
	}
	return out
}

// accountRef is the account or key of p that ref names: a signed-in
// account by its name, a key by its KeyID or its name, in any case.
func (p Provider) accountRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	for _, r := range p.AccountRefs() {
		if accountKey(r) == accountKey(ref) {
			return r, true
		}
	}
	if p.Account == nil {
		for _, k := range p.KeyList() {
			if k.Name != "" && strings.EqualFold(k.Name, ref) {
				return k.ID, true
			}
		}
	}
	return "", false
}

// SetAccountModels gives the account or key ref of the provider id the
// models it alone serves; none gives it every model the provider has again.
func SetAccountModels(id, ref string, models []string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	r, ok := p.accountRef(ref)
	if !ok {
		if p.Account == nil {
			return fmt.Errorf("%s has no key %q", p.Name, ref)
		}
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	ms := normalModels(models)
	if len(ms) == 0 && p.AccountModels[accountKey(r)] == nil {
		return nil
	}
	am := map[string][]string{}
	for k, v := range p.AccountModels {
		am[k] = v
	}
	if len(ms) == 0 {
		delete(am, accountKey(r))
	} else {
		am[accountKey(r)] = ms
	}
	p.AccountModels = am
	return Save(*p)
}

// AccountModelsOf is the models the account or key ref of the provider id
// alone serves, and the account or key as AccountModels knows it; no
// models when it serves all the provider's.
func AccountModelsOf(id, ref string) (string, []string, error) {
	p, err := Find(id)
	if err != nil {
		return "", nil, err
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return "", nil, fmt.Errorf("%s has no account or key %q", p.Name, ref)
	}
	return r, p.AccountModels[accountKey(r)], nil
}

// normalModels is a list of model ids as it is kept: trimmed, each once,
// in the order given.
func normalModels(ms []string) []string {
	var out []string
	for _, m := range ms {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// normalAccountModels is m as it is kept: keys in lower case, lists
// normalized, accounts serving all the provider's models left out; nil
// for none.
func normalAccountModels(m map[string][]string) map[string][]string {
	var out map[string][]string
	for u, ms := range m {
		u, ms = accountKey(u), normalModels(ms)
		if u == "" || len(ms) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[u] = ms
	}
	return out
}
