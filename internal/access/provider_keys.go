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

// GroupRef is a routing group as a provider's side of the keys reads it.
type GroupRef struct {
	// Refs are the ids a key's models may name it by: "group/<id>", and
	// a found group's "auto/<model>".
	Refs []string
	// Found: a group magpie found (one model several providers serve). A
	// key held to some models may use one whose every member it may use
	// without naming it, so an unticked key needs no entry for it.
	Found bool
	// Reaches: a member of it may be the provider's, now or as its
	// patterns match later. A key that names the group is sent to every
	// member, whatever models it lists besides.
	Reaches bool
}

// namesReaching says model entry m names one of gs's groups that reach the
// provider.
func namesReaching(m string, gs []GroupRef) bool {
	return slices.ContainsFunc(gs, func(g GroupRef) bool { return g.Reaches && namesGroup(m, g) })
}

func namesGroup(m string, g GroupRef) bool {
	return slices.ContainsFunc(g.Refs, func(ref string) bool { return ModelAllowed([]string{m}, ref) })
}

// routingGroupEntry: the entry names routing groups alone, "group/…" or
// "auto/…".
func routingGroupEntry(m string) bool {
	head, _, _ := strings.Cut(m, "/")
	return strings.EqualFold(head, "group") || strings.EqualFold(head, "auto")
}

// keptGroups are the user's groups, "group/<id>" each, none of whose
// members may be the provider's: what a key unticked from it keeps of
// groups (justforlxz's untick wrote "group/*", and a key that names a
// group goes to every member of it, so the key reached the provider
// through any group that has one of its models, a group magpie found for
// a model the provider serves with another too).
func keptGroups(gs []GroupRef, among func(GroupRef) bool) []string {
	var out []string
	for _, g := range gs {
		if !g.Reaches && !g.Found && len(g.Refs) > 0 && among(g) {
			out = append(out, g.Refs[0])
		}
	}
	return out
}

// modelKeys are the keys a provider's editor lists: those that may use
// a model. A read-only key, the Magpie Browser extension's, a key for
// MCP servers only (NoModels) and a key issued under another (bounded by
// it, short-lived) are left out.
func modelKey(k Key) bool {
	return !k.ReadOnly && !k.Browser && k.Issuer == "" && k.Expires.IsZero() && !slices.Equal(k.Models, []string{NoModels})
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

func reachOf(k Key, pid string, groups []GroupRef, read func() ([]AccountGroup, error)) (ProviderKey, []string, error) {
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
		case viaEntry(m, pid) || namesReaching(m, groups):
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
// far it reaches provider pid, in the keys' own order; groups are the
// routing groups, as they reach pid now.
func ProviderKeys(pid string, groups []GroupRef) ([]ProviderKey, error) {
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
		pk, _, err := reachOf(k, pid, groups, read)
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
//     there are now, "<id>/*" each, and the user's routing groups none
//     of whose members may be pid's, "group/<id>" each (justforlxz on
//     X: a key on every model, as most are, couldn't be unticked at
//     all, so the list did nothing); a provider or group added later is
//     not among them. A group magpie found is the key's when it may use
//     every member. An entry of the key's that names groups, one that
//     reaches pid among them, keeps those that don't: a key that names a
//     group goes to every member of it. A key that would be left no
//     model (Only, or groups alone, none kept) is refused, never let off
//     to all.
//
// Either every change is made or none is.
func SetProviderKeys(pid string, add, remove, providers []string, groups []GroupRef) error {
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
		pk, held, err := reachOf(keys[i], pid, groups, read)
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
		pk, held, err := reachOf(keys[i], pid, groups, read)
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
			kept := keptGroups(groups, func(GroupRef) bool { return true })
			if ms, err = CleanModels(append(ms, kept...)); err != nil {
				return err
			}
			keys[i].Models, changed = ms, true
			continue
		case pk.Only:
			return fmt.Errorf("%s may use only this provider's models, and with none it would use every model: turn the key off, or pick other models for it on the Gateway page", keys[i].Name)
		}
		var ms []string
		for _, m := range held {
			switch {
			case ownEntry(m, pid):
			case routingGroupEntry(m) && namesReaching(m, groups):
				ms = append(ms, keptGroups(groups, func(g GroupRef) bool { return namesGroup(m, g) })...)
			default:
				ms = append(ms, m)
			}
		}
		if len(ms) == 0 {
			return fmt.Errorf("%s may use only this provider's models, through its routing groups, and with none it would use every model: turn the key off, or pick other models for it on the Gateway page", keys[i].Name)
		}
		if ms, err = CleanModels(ms); err != nil {
			return err
		}
		keys[i].Models, changed = ms, true
	}
	if !changed {
		return nil
	}
	return save(keys)
}
