package access

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A provider's side of the keys' models (justforlxz on X: with many keys,
// ticking a provider in each key's models is a chore, so a provider's
// editor ticks the keys that may use it). It reads and writes Key.Models,
// the one list the Gateway page's "Models this key may use" writes too,
// so the two always agree.

// How far a key reaches one provider (ProviderKey.Reach).
const (
	// ReachAll: the key is held to no model, so it may use every
	// provider, those added later too.
	ReachAll = "all"
	// ReachWhole: every model of the provider, "<provider>/*".
	ReachWhole = "whole"
	// ReachSome: some of its models, each named.
	ReachSome = "some"
	// ReachNone: none of them by name; Via says which patterns of the
	// key's may still name some.
	ReachNone = ""
)

// ProviderKey is a gateway key as a provider's editor shows it.
type ProviderKey struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Off   bool   `json:"off,omitempty"`
	Reach string `json:"reach"`
	// Only: the provider is all the key may use by name, so taking it off
	// would leave it no model, which reads as every model (Models empty):
	// such a key is changed in its own models.
	Only bool `json:"only,omitempty"`
	// Via are the key's patterns that may name some of the provider's
	// models whatever it is ticked: "re:…", or a wildcard provider.
	Via []string `json:"via,omitempty"`
}

// modelKeys are the keys a provider's editor lists: those that may use
// a model. A read-only key, the Magpie Browser extension's and a key
// issued under another (bounded by it, short-lived) are left out.
func modelKey(k Key) bool {
	return !k.ReadOnly && !k.Browser && k.Issuer == "" && k.Expires.IsZero()
}

// ownEntry: the model entry names provider pid by its id, "<pid>/…".
func ownEntry(m, pid string) bool {
	if IsPattern(m) {
		return false
	}
	head, _, ok := strings.Cut(m, "/")
	return ok && strings.EqualFold(head, pid)
}

// viaEntry: the entry is no entry of pid's own, and may still name one of
// pid's models: a regular expression, or a wildcard provider matching pid.
func viaEntry(m, pid string) bool {
	if IsPattern(m) {
		return true
	}
	head, _, ok := strings.Cut(m, "/")
	return ok && strings.Contains(head, "*") && globRe(head).MatchString(pid)
}

// heldOf is the models the key is held to with its account groups in
// their place, as a request reads them; an entry that names no provider
// (a key held to nothing) is left out.
func heldOf(k Key, read func() ([]AccountGroup, error)) ([]string, error) {
	accounts, err := expandGroups(k.Accounts, read)
	if err != nil {
		return nil, err
	}
	held := HeldModels(k.Models, accounts)
	if len(k.Models) > 0 {
		return held, nil
	}
	return slices.DeleteFunc(slices.Clone(held), func(m string) bool {
		head, _, _ := strings.Cut(m, "/")
		return head == "" || strings.Contains(head, "\x00")
	}), nil
}

func reachOf(k Key, pid string, read func() ([]AccountGroup, error)) (ProviderKey, []string, error) {
	pk := ProviderKey{ID: k.ID, Name: k.Name, Off: k.Off}
	held, err := heldOf(k, read)
	if err != nil {
		return pk, nil, err
	}
	if len(k.Models) == 0 && !picksAccounts(k.Accounts) {
		pk.Reach = ReachAll
		return pk, held, nil
	}
	rest := 0
	for _, m := range held {
		switch {
		case ownEntry(m, pid):
			if _, model, _ := strings.Cut(m, "/"); model == "*" {
				pk.Reach = ReachWhole
			} else if pk.Reach == ReachNone {
				pk.Reach = ReachSome
			}
		case viaEntry(m, pid):
			pk.Via = append(pk.Via, m)
			rest++
		default:
			rest++
		}
	}
	pk.Only = pk.Reach != ReachNone && rest == 0
	return pk, held, nil
}

// ProviderKeys are the gateway keys that may use a model, each with how
// far it reaches provider pid, in the keys' own order.
func ProviderKeys(pid string) ([]ProviderKey, error) {
	pid = strings.TrimSpace(pid)
	if pid == "" || strings.Contains(pid, "/") {
		return nil, errors.New("No such provider")
	}
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return nil, err
	}
	read := groupsOnce()
	out := []ProviderKey{}
	for _, k := range keys {
		if !modelKey(k) {
			continue
		}
		pk, _, err := reachOf(k, pid, read)
		if err != nil {
			return nil, fmt.Errorf("The gateway key %q names an account group, and magpie can't read its account groups now: %v", k.Name, err)
		}
		out = append(out, pk)
	}
	return out, nil
}

func groupsOnce() func() ([]AccountGroup, error) {
	var gs []AccountGroup
	var err error
	read := false
	return func() ([]AccountGroup, error) {
		if !read {
			gs, err = loadGroups()
			read = true
		}
		return gs, err
	}
}

// SetProviderKeys lets the keys add use provider pid and the keys remove
// use it no longer, writing each key's Models as its own models picker
// would, and nothing else of it:
//   - add, to a key held to some models: "<pid>/*" joins them; to a key
//     held to some accounts' providers alone, those providers' "/*" and
//     "<pid>/*" become its models, so it keeps every one it had; a key
//     that reaches pid already is left as it is (some of its models stay
//     some, never made every one).
//   - remove: the key's entries of pid's own go; one held to accounts
//     alone keeps its other providers. A key that may use every model
//     (ReachAll) is held to every other provider of providers, the ids
//     there are now, "<id>/*" each, and every routing group, "group/*"
//     (justforlxz on X: a key on every model, as most are, couldn't be
//     unticked at all, so the list did nothing); a provider added later
//     is not among them. A key that would be left no model (Only) is
//     refused, never let off to all.
//
// Either every change is made or none is.
func SetProviderKeys(pid string, add, remove, providers []string) error {
	pid = strings.TrimSpace(pid)
	if pid == "" || strings.Contains(pid, "/") || strings.Contains(pid, "*") {
		return errors.New("No such provider")
	}
	for _, id := range add {
		if slices.Contains(remove, id) {
			return errors.New("A key can't be both added and taken off")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return err
	}
	read := groupsOnce()
	changed := false
	find := func(id string) (int, error) {
		i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == id })
		if i < 0 || !modelKey(keys[i]) {
			return -1, errors.New("Key not found")
		}
		return i, nil
	}
	for _, id := range add {
		i, err := find(id)
		if err != nil {
			return err
		}
		pk, held, err := reachOf(keys[i], pid, read)
		if err != nil {
			return err
		}
		if pk.Reach != ReachNone {
			continue
		}
		ms, err := CleanModels(append(slices.Clone(held), pid+"/*"))
		if err != nil {
			return err
		}
		keys[i].Models, changed = ms, true
	}
	for _, id := range remove {
		i, err := find(id)
		if err != nil {
			return err
		}
		pk, held, err := reachOf(keys[i], pid, read)
		if err != nil {
			return err
		}
		switch {
		case pk.Reach == ReachNone:
			continue
		case pk.Reach == ReachAll:
			var ms []string
			for _, id := range providers {
				if id = strings.TrimSpace(id); id != "" && !strings.ContainsAny(id, "/*") && !strings.EqualFold(id, pid) {
					ms = append(ms, id+"/*")
				}
			}
			if len(ms) == 0 {
				// no other provider: an empty list would read as every model
				return fmt.Errorf("%s may use every provider, and there is no other to keep it to: turn the key off, or pick its models on the Gateway page", keys[i].Name)
			}
			if ms, err = CleanModels(append(ms, "group/*")); err != nil {
				return err
			}
			keys[i].Models, changed = ms, true
			continue
		case pk.Only:
			return fmt.Errorf("%s may use only this provider's models, and with none it would use every model: turn the key off, or pick other models for it on the Gateway page", keys[i].Name)
		}
		keys[i].Models, changed = slices.DeleteFunc(slices.Clone(held), func(m string) bool { return ownEntry(m, pid) }), true
	}
	if !changed {
		return nil
	}
	return save(keys)
}
