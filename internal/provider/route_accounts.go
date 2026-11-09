package provider

import (
	"slices"
	"strings"
)

// Which of a provider's accounts or keys a model goes to. The gateway's
// routing (gateway/fallback.go, perKeyEvery) and magpie's own test of a
// model (test.go) both take them from here, so a test asks the account
// or key a real request would: a pool whose accounts list different
// models tested every one of them with its first account, and a model
// only another account serves failed a test the gateway answered (#4992,
// scutken).

// AccountOn is one of a signed-in provider's accounts a request may take:
// Own is the one the agent is signed in to, Rank its place in the user's
// order.
type AccountOn struct {
	Provider
	Own  bool
	Rank int
}

// AccountsOn are the accounts of p (signed in: p.Account) a request may
// take, in the user's order: the one signed in to, unless the user paused
// it for the others on (#263), then the others on; kept signed in to an
// account of the user's choosing, the one signed in to stands at its own
// place in the order, not first (#524).
func (p Provider) AccountsOn() []AccountOn {
	also := p.AlsoOn()
	var all []AccountOn
	if len(also) == 0 || !p.OwnPaused() {
		all = append(all, AccountOn{Provider: p, Own: true})
	}
	for i, q := range also {
		all = append(all, AccountOn{Provider: q, Rank: i + 1})
	}
	if ranks := p.LoginRanks(); ranks != nil {
		at := func(a AccountOn) int {
			if r, ok := ranks[strings.ToLower(a.Account.User)]; ok {
				return r
			}
			return len(ranks)
		}
		slices.SortStableFunc(all, func(a, b AccountOn) int { return at(a) - at(b) })
		for i := range all {
			all[i].Rank = i
		}
	}
	return all
}

// AccountsFor are p's accounts on (AccountsOn) as routing takes them for
// model: out, those it goes over; left, those whose own list (their plan)
// leaves the model out, tried only after them; barred, those the user set
// not to serve it (AccountModels, #474), then those held reports held
// back (the gateway's usage caps; nil holds none). An account whose list
// lacks the model would only answer 400, so it is in out only when no
// account lists it (by the vendor's own id: an account's list names its
// models by theirs, not by one the user lists one by). A list older than
// the model, as magpie knows it (Account.listLags), is unknown, not a
// plan without it: that account is in out.
func (p Provider) AccountsFor(model string, held func(AccountOn) bool) (out, left, barred []AccountOn) {
	all := slices.DeleteFunc(p.AccountsOn(), func(a AccountOn) bool {
		if p.AccountServes(a.Account.User, model) {
			return false
		}
		barred = append(barred, a)
		return true
	})
	if held != nil {
		all = slices.DeleteFunc(all, func(a AccountOn) bool {
			if !held(a) {
				return false
			}
			barred = append(barred, a)
			return true
		})
	}
	own := p.OwnModelID(model)
	var lacking []AccountOn
	for _, a := range all {
		if a.Account.Lists(own) {
			out = append(out, a)
		} else {
			lacking = append(lacking, a)
		}
	}
	if len(out) == 0 {
		return all, nil, barred
	}
	// a list older than the model says nothing of it (listLags): such an
	// account is asked as one that lists it, at its own place in the order
	listing := slices.Clone(out)
	lags := map[int]bool{}
	for _, a := range lacking {
		if a.Account.listLags(own, listing) {
			lags[a.Rank] = true
		} else {
			left = append(left, a)
		}
	}
	if len(lags) > 0 {
		out = slices.DeleteFunc(slices.Clone(all), func(a AccountOn) bool {
			return !lags[a.Rank] && !slices.ContainsFunc(listing, func(o AccountOn) bool { return o.Rank == a.Rank })
		})
	}
	return out, left, barred
}

// KeyOn is one of a provider's keys on, at its place among them (Rank),
// of how many it has on (Of).
type KeyOn struct {
	KeyAccount
	Rank, Of int
}

// KeysFor are p's keys on as routing takes them for model: out, those it
// goes over; unlisted, those the vendor lists the model to no key of
// (tried after out, or as out when no key lists it); barred, those the
// user set not to serve it (AccountModels, #474) and those held reports
// held back (the gateway's spend caps; nil holds none). A key on a plan
// of its own whose models leave this one out (KeyAccount.Plan) is never
// asked while another can be: only when no key's plan has it, a model
// the vendor added since, are they out. A key made for a protocol p has
// no endpoint for is in none of them; noURL is the first such key's.
func (p Provider) KeysFor(model string, held func(KeyOn) bool) (out, unlisted, barred []KeyOn, noURL Protocol) {
	keys := p.KeysOn()
	serves := p.Serving(model) // the lists read once, not once a key (@NetRozee on X)
	// the Agent Plan's key is never sent the pay as you go's
	// deepseek-v4-1-flash-261017, while a key whose plan has it can take
	// it (yckonnnn on X)
	planHas := p.PlanServing(model)
	var planless []KeyOn
	for i, k := range keys {
		q := p.WithKey(k)
		if len(q.Speaks()) == 0 && !q.DecidesModel(model) {
			if noURL == "" {
				noURL = k.Protocol
			}
			continue // made for a protocol this provider has no endpoint for
		}
		ko := KeyOn{KeyAccount: k, Rank: i, Of: len(keys)}
		switch {
		case !p.AccountServes(keyID(k.Key), model):
			barred = append(barred, ko)
		case held != nil && held(ko):
			barred = append(barred, ko)
		case !planHas(k):
			planless = append(planless, ko)
		case !serves(k):
			// the vendor lists the model to another key only
			unlisted = append(unlisted, ko)
		default:
			out = append(out, ko)
		}
	}
	if len(out) == 0 {
		out, unlisted = unlisted, nil // no key lists it: try them all the same
	}
	if len(out) == 0 {
		// no key's plan has it as magpie knows the plans: a model the
		// vendor added since, tried on them all the same. Else a key
		// whose plan lacks it is never asked, not even after the others
		// refused it: its endpoint has no such model either
		out = planless
	}
	return out, unlisted, barred, noURL
}
