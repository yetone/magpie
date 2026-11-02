package provider

// A key's own models when it is added (Young on Discord): a provider of
// several keys, each of a group of the relay's that serves some of its
// models, had every new key take every model (AccountModels' "all"), and
// the user narrowed each by hand. A key added now is asked for its own
// list, with that key alone, and is given what it answers as its models,
// when that leaves out a model the provider's other keys serve.
//
// Only a key that is new and has no list of its own is given one: an
// existing key's list, or one the user set, is never changed. A key whose
// list can't be read now (an error, a timeout, a base that didn't answer,
// an empty list) is unknown and keeps every model, as before; so does a
// key of a vendor with no list to ask (Preset.NoList, a plan's preset
// models), and a key whose list has every model the others have, which a
// list would only hold back from what the vendor adds later.

import (
	"context"
	"slices"
	"sync"
	"time"
)

// prefillWait is the longest an add waits for the keys' lists.
const prefillWait = 8 * time.Second

// KeysHeld is the keys, on or off, each provider of ids (every provider
// when none is named) has now, by provider id and KeyID: what
// PrefillNewKeys tells the keys added since by.
func KeysHeld(ids ...string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range All() {
		if len(ids) > 0 && !slices.Contains(ids, p.ID) {
			continue
		}
		set := map[string]bool{}
		for _, k := range p.KeyList() {
			set[k.ID] = true
		}
		out[p.ID] = set
	}
	return out
}

// PrefillNewKeys gives each key on that a provider of ids (every provider
// when none is named) has and before didn't — a provider before has no
// entry for is new, all its keys with it — the models its own list
// answers, as AccountModels: only a key with no models of its own yet, of
// a provider with more than one key on, whose list leaves out a model the
// provider's others list. It answers how many keys were given a list.
func PrefillNewKeys(ctx context.Context, before map[string]map[string]bool, ids ...string) int {
	ctx, cancel := context.WithTimeout(ctx, prefillWait)
	defer cancel()
	type job struct {
		p Provider
		k KeyAccount
	}
	var jobs []job
	for _, p := range All() {
		if len(ids) > 0 && !slices.Contains(ids, p.ID) {
			continue
		}
		// one switched off is asked nothing
		if p.Account != nil || p.IsPlugin() || !p.Ready() || p.Off {
			continue
		}
		keys := p.KeysOn()
		if len(keys) < 2 {
			continue // one key's list is the provider's
		}
		for _, k := range keys {
			id := keyID(k.Key)
			if before[p.ID][id] || p.AccountModels[accountKey(id)] != nil {
				continue
			}
			jobs = append(jobs, job{p, k})
		}
	}
	if len(jobs) == 0 {
		return 0
	}
	// each new key's own list, a few at a time: an import of a thousand
	// keys is not a thousand requests to one relay at once
	lists := make([][]string, len(jobs))
	slots := make(chan struct{}, keyReadsAtOnce)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			lists[i] = ownList(ctx, j.p.WithKey(j.k))
		})
	}
	wg.Wait()
	// what the provider's keys list between them: its saved list, and the
	// new keys' own
	all := map[string]map[string]bool{}
	for i, j := range jobs {
		set := all[j.p.ID]
		if set == nil {
			set = map[string]bool{}
			if live, ok := j.p.liveAll(); ok {
				for _, m := range live {
					set[m.ID] = true
				}
			}
			all[j.p.ID] = set
		}
		for _, m := range lists[i] {
			set[m] = true
		}
	}
	give := map[string]map[string][]string{} // provider id → key id → models
	for i, j := range jobs {
		ms := lists[i]
		if len(ms) == 0 {
			continue // unknown: every model, as before
		}
		has := make(map[string]bool, len(ms))
		for _, m := range ms {
			has[m] = true
		}
		short := false
		for m := range all[j.p.ID] {
			if !has[m] {
				short = true
				break
			}
		}
		if !short {
			continue // it serves all the provider has; a list adds nothing
		}
		if give[j.p.ID] == nil {
			give[j.p.ID] = map[string][]string{}
		}
		give[j.p.ID][keyID(j.k.Key)] = ms
	}
	n := 0
	for pid, keys := range give {
		// read again: the user may have set a key's models, or removed
		// it, while its list was asked for
		p, err := Find(pid)
		if err != nil {
			continue
		}
		am := map[string][]string{}
		for k, v := range p.AccountModels {
			am[k] = v
		}
		changed := 0
		for kid, ms := range keys {
			if am[accountKey(kid)] != nil || !p.HasKeyID(kid) {
				continue
			}
			am[accountKey(kid)] = ms
			changed++
		}
		if changed == 0 {
			continue
		}
		p.AccountModels = am
		if Save(*p) == nil {
			n += changed
		}
	}
	return n
}

// HasKeyID reports whether the key of fingerprint id is one of the
// provider's, on or off.
func (p Provider) HasKeyID(id string) bool {
	return slices.ContainsFunc(p.KeyList(), func(k KeyInfo) bool { return k.ID == id })
}

// ownList is the ids of the models q, a provider seen with one of its
// keys, is listed by the vendor now, with that key alone; nil when it
// can't be told: a vendor with no list to ask, a list that failed or
// answered nothing, or one of its bases that didn't answer, whose part
// would be filled in from the provider's list saved before.
func ownList(ctx context.Context, q Provider) []string {
	if !q.ListAsks() {
		return nil
	}
	if _, ok := q.planOnly(); ok {
		return nil
	}
	if q.IsCline() || q.IsKilo() {
		return nil // lists of their own feeds, not a key's /models
	}
	l, err := q.fetchOne(ctx)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	for b := range l.sides {
		if !l.answered[b] {
			return nil // a base kept from before, not this key's answer
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range l.models {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m.ID)
		}
	}
	return out
}
