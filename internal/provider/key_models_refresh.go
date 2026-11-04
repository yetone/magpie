package provider

// Each key's own models, again on every fetch (young02036 on Discord): a
// provider of several keys, each at a group of a relay's that serves some
// of its models, had each key's list read only when it was added
// (PrefillNewKeys). Fetch models merged every key's list into the
// provider's, marking each model with the keys that list it, but a key's
// own models stayed what its list had when it was added: a model its
// group took on later was never sent to it, one its group gave up still
// was.
//
// A fetch now carries what each key's list gained and lost since the
// fetch before into that key's models: a model its list has now and
// didn't before is ticked for it, one it had and has no more is taken
// out. A model the user un-ticked for a key that its list had before and
// still has stays un-ticked, and one the user ticked that its list never
// had stays ticked: magpie's default never overrides what the user set.
//
// A key with no models of its own yet (All models: one added before its
// own list was asked for, the provider's first key, one whose list wasn't
// read in time when it was added) is given its own list, as a key added
// now is (PrefillNewKeys), when that list leaves out a model the
// provider's other keys list (young02036 on Discord: Fetch models ticks
// the models each key can serve). A key the user set back to every model
// (AccountModelsAll) stays so, and one whose list has every model the
// others list serves every model still, so the vendor's later additions
// reach it. A key with no marks now (one key on, one switched off, a
// list that failed with nothing kept) is unknown, and its models stay as
// they are.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// keyMarks are, by key fingerprint, the provider's own ids of the models
// its fetched list marks each key as seeing (catalog.Model.Keys).
func keyMarks(id string) map[string]map[string]bool {
	ms, _, _ := catalog.Live(id)
	ms = append(ms, catalog.LiveDrawers(id)...)
	ms = append(ms, catalog.LiveVideomakers(id)...)
	out := map[string]map[string]bool{}
	for _, m := range ms {
		for _, k := range m.Keys {
			if out[k] == nil {
				out[k] = map[string]bool{}
			}
			out[k][m.ID] = true
		}
	}
	return out
}

// refreshKeyModels carries what each of the provider id's keys' lists
// gained and lost between before and now into the models it serves
// (AccountModels), gives a key with none of its own its list now when
// that is short of the others', and answers how many keys' models changed.
func refreshKeyModels(id string, before, now map[string]map[string]bool) int {
	if len(now) == 0 {
		return 0
	}
	p, err := Find(id)
	if err != nil {
		return 0
	}
	// every model the keys' lists have between them
	every := map[string]bool{}
	for _, set := range now {
		for m := range set {
			every[m] = true
		}
	}
	var am map[string][]string
	n := 0
	for _, k := range p.KeysOn() {
		kid := keyID(k.Key)
		was := p.AccountModels[accountKey(kid)]
		o, w := before[kid], now[kid]
		if len(w) == 0 {
			continue
		}
		var list []string
		if was == nil {
			// a key on every model, by magpie's default and not the
			// user's, whose list leaves some of the others' out
			if p.AccountModelsAll[accountKey(kid)] || len(w) >= len(every) {
				continue
			}
			for m := range w {
				list = append(list, p.ListedModelID(m))
			}
			slices.Sort(list)
		} else {
			if len(o) == 0 {
				continue
			}
			for _, m := range was {
				own := p.OwnModelID(m)
				if o[own] && !w[own] {
					continue // its list had it and has no more
				}
				list = append(list, m)
			}
			var gained []string
			for m := range w {
				if !o[m] {
					gained = append(gained, m)
				}
			}
			slices.Sort(gained)
			for _, m := range gained {
				if !slices.ContainsFunc(list, func(x string) bool { return x == m || p.OwnModelID(x) == m }) {
					list = append(list, p.ListedModelID(m))
				}
			}
		}
		if len(list) == 0 || slices.Equal(list, was) {
			continue // nothing left would read as every model: keep it
		}
		if am == nil {
			am = map[string][]string{}
		}
		am[accountKey(kid)] = list
		n++
	}
	if n == 0 {
		return 0
	}
	// read again: the user may have set a key's models while the lists
	// were asked for, and what they set stands
	q, err := Find(id)
	if err != nil {
		return 0
	}
	merged := make(map[string][]string, len(q.AccountModels))
	for k, v := range q.AccountModels {
		merged[k] = v
	}
	n = 0
	for k, v := range am {
		if !slices.Equal(q.AccountModels[k], p.AccountModels[k]) || q.AccountModelsAll[k] != p.AccountModelsAll[k] {
			continue
		}
		merged[k] = v
		n++
	}
	if n == 0 {
		return 0
	}
	q.AccountModels = merged
	if Save(*q) != nil {
		return 0
	}
	return n
}

// KeyListed is the models the provider id's last fetched list marks its
// key ref (KeyID) as seeing, by the ids the provider lists them by, for
// the editor's Select all available (young02036 on Discord); nil when no
// list marks that key (one key on, a fetch not asked per key), which is
// unknown, not none.
func KeyListed(id, ref string) []string {
	p, err := Find(id)
	if err != nil {
		return nil
	}
	seen := keyMarks(id)[strings.ToLower(strings.TrimSpace(ref))]
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, p.ListedModelID(m))
	}
	slices.Sort(out)
	return out
}
