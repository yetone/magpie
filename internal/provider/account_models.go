package provider

// An account's own models (#474): one account of several may be one the
// user keeps for some of the provider's models only — an account with a
// small allowance but a verified standing, kept for the cheap models so
// the big ones don't spend it. Its list is a subset of the provider's;
// an account without one serves every model the provider does, as before.
// The gateway never sends a model to an account whose list leaves it out
// (gateway/fallback.go).

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// AccountServes says whether the account or key ref (a signed-in
// account's name, a key's KeyID) is let serve model: true when it has no
// list of its own, and for a model Codex calls by itself that no list can
// hold (codexOwnHidden).
//
// A model the user lists by an id of theirs (SetModelID) is in the list
// by that id, or by the vendor's own one a list kept from before has.
func (p Provider) AccountServes(ref, model string) bool {
	ms, ok := p.AccountModels[accountKey(ref)]
	if !ok || len(ms) == 0 || slices.Contains(ms, model) || codexOwnHidden(p, model) {
		return true
	}
	own := p.OwnModelID(model)
	return own != model && slices.Contains(ms, own)
}

// codexOwnHidden says model is one a Codex account serves that its list
// of models can't name: codex-auto-review, the model Codex's auto-review
// of an approval (the guardian) asks for. It is hidden from the
// provider's models, so an account's list, picked from those, never had
// it, and a list kept for the big models turned every approval away as
// "every account barred" once Codex's turns went through magpie's
// account failover (#1959, parkavenue9639). ChatGPT serves it to every
// plan, free since 2026-10-06 (#1593).
func codexOwnHidden(p Provider, model string) bool {
	return p.Account != nil && p.Account.Agent == "codex" && strings.EqualFold(strings.TrimSpace(model), "codex-auto-review")
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

// AccountRef is one of a provider's accounts or keys as a gateway key's
// account list knows it (#905): ID what the list keeps — an account's
// stable id, kept through renames, a key's fingerprint — User and Plan
// what it is shown as.
type AccountRef struct {
	ID   string
	User string
	Plan string
	Key  bool // one of the provider's keys, not a signed-in account
}

// AccountIDs are the provider's accounts and keys as a gateway key's
// account list knows them (#905): an account by its stable id, a key by
// its fingerprint, with the name and plan each is shown by.
func (p Provider) AccountIDs() []AccountRef {
	var out []AccountRef
	if p.Account == nil {
		for _, k := range p.KeyList() {
			out = append(out, AccountRef{ID: k.ID, User: cmp.Or(k.Name, k.Masked), Key: true})
		}
		return out
	}
	agent := p.Account.Agent
	if p.IsPlugin() {
		agent = p.ID
	}
	if p.Account.User != "" {
		out = append(out, AccountRef{ID: LoginID(agent, p.Account.User), User: p.Account.User, Plan: p.Account.Plan})
	}
	for _, l := range Logins(agent) {
		if !slices.ContainsFunc(out, func(r AccountRef) bool { return accountKey(r.User) == accountKey(l.User) }) {
			out = append(out, AccountRef{ID: LoginID(agent, l.User), User: l.User, Plan: l.Plan})
		}
	}
	return out
}

// ResolveAccount is the entry "<provider>/<id>" an account list — a
// gateway key's (#905), a routing group's — keeps for provider pid's
// account or key ref: an account named by its stable id or the name it
// is shown by, resolved to that id, a key by its fingerprint. It fails
// for a provider magpie doesn't have, or one with no such account or key.
func ResolveAccount(pid, ref string) (string, error) {
	p, err := Find(pid)
	if err != nil {
		return "", err
	}
	for _, r := range p.AccountIDs() {
		if strings.EqualFold(r.ID, ref) || strings.EqualFold(r.User, ref) {
			return p.ID + "/" + r.ID, nil
		}
	}
	if p.Account == nil {
		return "", fmt.Errorf("%s has no key %q", p.Name, ref)
	}
	return "", fmt.Errorf("%s has no account %q", p.Name, ref)
}

// AccountID is the stable id of p's account or key as a gateway key's
// account list knows it (#905): the account's, kept through renames, or
// a key's fingerprint.
func (p Provider) AccountID() string {
	if a := p.Account; a != nil && a.User != "" {
		agent := a.Agent
		if p.IsPlugin() {
			agent = p.ID
		}
		return LoginID(agent, a.User)
	}
	return KeyID(p.Key)
}

// AccountNames maps every account and key a gateway key may name (#905),
// "<provider>/<id>", to how it is shown, "<provider>/<user>", an account
// by the name the user gave it (AccountShown); what no provider has now
// maps to itself.
func AccountNames() map[string]string {
	out := map[string]string{}
	for _, p := range All() {
		for _, a := range p.AccountIDs() {
			out[p.ID+"/"+a.ID] = p.ID + "/" + a.Shown(p)
		}
	}
	return out
}

// Shown is how the account or key a is shown on p: an account by the
// name the user gave it, a key by its own name.
func (a AccountRef) Shown(p Provider) string {
	if a.Key {
		return a.User
	}
	return p.AccountShown(a.User)
}

// accountRef is the account or key of p that ref names: a signed-in
// account by its name, else by the name the user gave it when that is one
// account's and no other's email (AccountByName; #4532), a key by its
// KeyID or its name, in any case. An account's own name goes first: the
// GUI passes it, and a name given to another account never takes it.
func (p Provider) accountRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	for _, r := range p.AccountRefs() {
		if accountKey(r) == accountKey(ref) {
			return r, true
		}
	}
	if p.Account != nil {
		if u, err := AccountByName(p.ID, p.AccountRefs(), ref); err == nil && u != "" {
			return u, true
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
	// a key set to every model by the user is remembered as theirs, so a
	// Fetch models doesn't tick its own list for it again
	all := len(ms) == 0 && p.Account == nil
	if len(ms) == 0 && p.AccountModels[accountKey(r)] == nil && (!all || p.AccountModelsAll[accountKey(r)]) {
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
	ma := maps.Clone(p.AccountModelsAll)
	if all {
		if ma == nil {
			ma = map[string]bool{}
		}
		ma[accountKey(r)] = true
	} else {
		delete(ma, accountKey(r))
	}
	p.AccountModelsAll = ma
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

// SetModels gives the provider id the models the user picked for its
// agents; none gives them the vendor's list again.
func SetModels(id string, models []string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	ms := normalModels(models)
	if slices.Equal(ms, normalModels(p.Models)) && !p.ServesNone() && !p.BestInFamily {
		return nil
	}
	p.Models, p.NoModels, p.BestInFamily = ms, false, false
	return Save(*p)
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

// normalModelsAll is the keys set back to every model as they are kept:
// in lower case, and only those with no models of their own in am.
func normalModelsAll(m map[string]bool, am map[string][]string) map[string]bool {
	var out map[string]bool
	for u, on := range m {
		u = accountKey(u)
		if !on || u == "" || am[u] != nil {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[u] = true
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
