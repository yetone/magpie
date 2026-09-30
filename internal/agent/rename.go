package agent

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// RenameProvider gives a provider another id (provider.Rename) and moves
// the agents on one of its models to the same model by the new id, each
// spelled the way the agent spells it, and what else in their configs names
// it (RenameRefs). It answers the agents it moved.
func RenameProvider(from, to string) ([]string, error) {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	type move struct {
		a        *Agent
		key, val string
	}
	// what the agents are on is read before: the picker's options name
	// the provider by the id it has
	var moves []move
	if from != to {
		for _, a := range Detected() {
			vals := a.Values()
			for _, f := range a.Fields {
				v := vals[f.Key]
				if v == "" {
					continue
				}
				nv := ""
				if f.Options != nil {
					for _, o := range f.Options(vals) {
						if o.Value == v && strings.HasPrefix(o.Ref, from+"/") {
							now := to + "/" + strings.TrimPrefix(o.Ref, from+"/")
							nv = strings.Replace(v, o.Ref, now, 1)
							break
						}
					}
				}
				if nv == "" && strings.HasPrefix(v, magpieID+"/"+from+"/") {
					nv = magpieID + "/" + to + "/" + strings.TrimPrefix(v, magpieID+"/"+from+"/")
				}
				if nv != "" && nv != v {
					moves = append(moves, move{a, f.Key, nv})
				}
			}
		}
	}
	if err := provider.Rename(from, to); err != nil {
		return nil, err
	}
	var moved []string
	for _, m := range moves {
		if err := m.a.Apply(m.key, m.val); err != nil {
			return moved, err
		}
		if len(moved) == 0 || moved[len(moved)-1] != m.a.Name {
			moved = append(moved, m.a.Name)
		}
	}
	if from != to {
		for _, a := range Detected() {
			if a.RenameRefs == nil {
				continue
			}
			ok, err := a.RenameRefs(from, to)
			if err != nil {
				return moved, err
			}
			if ok && !slices.Contains(moved, a.Name) {
				moved = append(moved, a.Name)
			}
		}
	}
	SyncCatalog()
	return moved, nil
}

// MoveOffAccountIDs moves each of the user's providers saved on a
// subscription's id (provider.OnAccountIDs) to a free one, the agents on it
// with it, so the subscription is listed once signed in: a "WorkBuddy" key
// saved as workbuddy before WorkBuddy's plan was a subscription hid it, and
// signing in to it said "signed in, but magpie can't list it". Run at
// start-up; errors are swallowed as RenameLegacy's are.
func MoveOffAccountIDs() {
	for _, id := range provider.OnAccountIDs() {
		_, _ = RenameProvider(id, provider.FreeID(id))
	}
}
