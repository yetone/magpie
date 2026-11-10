package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/settings"
)

// A model alias is a name the user counts a model's usage under, so that
// what one model spent under two ids is one row in Usage and Sessions
// (SlouchyQuill507 on Discord: deepseek-v4.1-flash and deepseek-flash).
// It is a way of showing usage and nothing else: a request, the model
// lists written into agents, routing and prices never read it. A call is
// still priced by the id it was made under, and only then counted under
// the alias.

// SetUsageAlias counts usage of the model from under the name to, or, with
// to "", under its own id again. Both are model ids as any vendor spells
// them, matched as the groups magpie finds match them (sameModel: a
// vendor's prefix, case, "_" or a snapshot date make no difference). It
// refuses an alias of a model to itself and one that would loop back to
// from through the others. changed is false when there was nothing to
// change.
func SetUsageAlias(from, to string) (changed bool, err error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if err := checkAliasName(from); err != nil {
		return false, err
	}
	if to != "" {
		if err := checkAliasName(to); err != nil {
			return false, err
		}
		if sameModel(from) == sameModel(to) {
			return false, fmt.Errorf("%s is %s already: an alias names another model id", from, to)
		}
	}
	err = settings.EditUsageAliases(func(m map[string]string) (map[string]string, error) {
		before := maps.Clone(m)
		// one entry per model however it was spelt: the new one takes the
		// place of an older spelling
		for k := range m {
			if sameModel(k) == sameModel(from) {
				delete(m, k)
			}
		}
		if to != "" {
			if m == nil {
				m = map[string]string{}
			}
			m[from] = to
			if loop := aliasLoop(m, from); loop != nil {
				return nil, fmt.Errorf("%s would come back to itself: %s", from, strings.Join(loop, " → "))
			}
		}
		changed = !maps.Equal(before, m)
		return m, nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// checkAliasName is whether s can be a side of an alias: a model's id.
func checkAliasName(s string) error {
	if s == "" {
		return errors.New("name a model id, as deepseek-v4.1-flash")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q is a flag, not a model id", s)
	}
	if len([]rune(s)) > 80 {
		return errors.New("a model id is at most 80 characters")
	}
	if Slug(sameModel(s)) == "" {
		return fmt.Errorf("%q names no model: give a model's id, as deepseek-v4.1-flash", s)
	}
	return nil
}

// aliasLoop is the ids from comes back to itself through when m is
// followed from it, from first and last; nil when it doesn't.
func aliasLoop(m map[string]string, from string) []string {
	by := aliasIndex(m)
	path := []string{from}
	seen := map[string]bool{sameModel(from): true}
	k := sameModel(from)
	for {
		e, ok := by[k]
		if !ok {
			return nil
		}
		path = append(path, e.to)
		k = sameModel(e.to)
		if seen[k] {
			if k == sameModel(from) {
				return path
			}
			return nil // a loop further on, not through from
		}
		seen[k] = true
	}
}

type aliasEntry struct{ from, to string }

// aliasIndex is m by sameModel of its keys; of two keys spelt as one, the
// first in order wins, so a hand-written file reads the same each time.
func aliasIndex(m map[string]string) map[string]aliasEntry {
	by := make(map[string]aliasEntry, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		to := strings.TrimSpace(m[k])
		sk := sameModel(k)
		if to == "" || sk == "" || sameModel(to) == sk {
			continue
		}
		if _, ok := by[sk]; !ok {
			by[sk] = aliasEntry{strings.TrimSpace(k), to}
		}
	}
	return by
}

// UsageAliasFor is the alias the user set for the model id, as
// settings.json has it (from as written there), and where usage of it is
// counted in the end, through any alias of that. ok is false when it has
// none. err is settings.json not read: unknown, not "none".
func UsageAliasFor(id string) (from, to, counted string, ok bool, err error) {
	s, err := settings.LoadChecked()
	if err != nil {
		return "", "", "", false, err
	}
	e, ok := aliasIndex(s.UsageAliases)[sameModel(strings.TrimSpace(id))]
	if !ok {
		return "", "", "", false, nil
	}
	return e.from, e.to, usageAliasOf(s.UsageAliases)(e.from), true, nil
}

// UsageAliases are the aliases the user set, as settings.json has them.
func UsageAliases() map[string]string { return maps.Clone(usageAliasesNow()) }

// lastAliases is the last UsageAliases read, for when settings.json can't
// be read: a failed read is not "no aliases", and usage would otherwise be
// shown split until the file reads again.
var lastAliases atomic.Pointer[map[string]string]

func usageAliasesNow() map[string]string {
	s, err := settings.LoadChecked()
	if err != nil {
		if p := lastAliases.Load(); p != nil {
			return *p
		}
		return nil
	}
	m := s.UsageAliases
	lastAliases.Store(&m)
	return m
}

// UsageAlias names a model's usage after pricing: a found group's public id,
// then the user's alias. Both group spellings count together, including in
// old session files. Read once before any usage lock; the returned function
// reads no settings or providers. It is nil when no names need mapping.
func UsageAlias() func(string) string {
	alias, groups := usageAliasOf(usageAliasesNow()), usageGroupNames()
	if len(groups) == 0 {
		return alias
	}
	var memo sync.Map
	return func(model string) string {
		if v, ok := memo.Load(model); ok {
			return v.(string)
		}
		out := publicRef(model, func(gid string) string { return groups[gid] })
		if alias != nil {
			out = alias(out)
		}
		memo.Store(model, out)
		return out
	}
}

// usageGroupNames is the spelling of every found group that has a public id.
// A user's pre-existing provider named auto keeps ownership of auto/<model>.
func usageGroupNames() map[string]string {
	var names map[string]string
	if autoRefsOn() {
		for _, g := range sharedGroups() {
			if g.Spelled != "" {
				if names == nil {
					names = map[string]string{}
				}
				names[g.ID] = g.Spelled
			}
		}
	}
	return names
}

// UsageNamesKey changes when an alias or a found group's public spelling does,
// so a cached view never holds the names from before a catalog change.
func UsageNamesKey() string {
	b, _ := json.Marshal(struct {
		Aliases, Groups map[string]string
	}{usageAliasesNow(), usageGroupNames()})
	return string(b)
}

// UsageAliasIn maps only the user aliases m, without the current group catalog.
func UsageAliasIn(m map[string]string) func(string) string { return usageAliasOf(m) }

func usageAliasOf(m map[string]string) func(string) string {
	by := aliasIndex(m)
	if len(by) == 0 {
		return nil
	}
	// each alias followed to its end once; a loop written into the file by
	// hand counts under each model's own id ("")
	resolved := make(map[string]string, len(by))
	for k, e := range by {
		seen := map[string]bool{k: true}
		name := e.to
		for {
			nk := sameModel(name)
			if seen[nk] {
				name = ""
				break
			}
			seen[nk] = true
			next, ok := by[nk]
			if !ok {
				break
			}
			name = next.to
		}
		resolved[k] = name
	}
	// a summary asks for the same few ids over every row: sameModel is
	// worked out once for each
	var memo sync.Map
	return func(model string) string {
		if v, ok := memo.Load(model); ok {
			return v.(string)
		}
		out := model
		if name := resolved[sameModel(model)]; name != "" {
			out = name
		}
		memo.Store(model, out)
		return out
	}
}
