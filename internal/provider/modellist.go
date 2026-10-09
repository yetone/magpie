package provider

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// ModelList is everything that decides which of magpie's models an agent's
// lists show, and in what order, as a profile keeps it (#1368): its
// visibility (Visible), the models taken out one by one (HiddenModels) or,
// for an agent shown only its picks, the ones picked (PickedModels), and the
// order it was put in (OrderedModels). The fast picks are not here: they say
// how a model is sent, not whether it is listed.
type ModelList struct {
	// Visible is what its lists are narrowed to; nil when they aren't.
	Visible *[]string `json:"visible,omitempty"`
	Hidden  []string  `json:"hidden,omitempty"`
	// Only: it is shown only Picked, none when Picked is empty.
	Only   bool     `json:"only,omitempty"`
	Picked []string `json:"picked,omitempty"`
	Order  []string `json:"order,omitempty"`
}

// Default reports whether l is what an agent never picked for is shown:
// every model, in magpie's own order.
func (l ModelList) Default() bool {
	return l.Visible == nil && len(l.Hidden) == 0 && !l.Only && len(l.Order) == 0
}

// ModelListOf is agent's model list as it is now.
func ModelListOf(agent string) ModelList {
	agent = strings.ToLower(strings.TrimSpace(agent))
	s := heldSettings()
	var l ModelList
	if v, ok := s.Visible[agent]; ok {
		v = slices.Clone(v)
		l.Visible = &v
	}
	if p, ok := s.PickedModels[agent]; ok {
		l.Only, l.Picked = true, slices.Clone(p)
	} else {
		l.Hidden = slices.Clone(s.HiddenModels[agent])
	}
	l.Order = slices.Clone(s.OrderedModels[agent])
	return l
}

// SetModelLists puts each agent's model list back as it was kept, exactly,
// and tells the agents that keep the models in files of their own once, as
// a pick made by hand does (catalog.Touched). A model of a provider renamed
// since is the same model by its new id. It returns the agents whose list
// changed, sorted.
func SetModelLists(ls map[string]ModelList) ([]string, error) {
	s := settings.Load()
	was := Renamed()
	var changed []string
	put := func(m *map[string][]string, agent string, ids []string, on bool) bool {
		cur, had := (*m)[agent]
		if !on {
			if !had {
				return false
			}
			delete(*m, agent)
			return true
		}
		if had && slices.Equal(cur, ids) {
			return false
		}
		if *m == nil {
			*m = map[string][]string{}
		}
		(*m)[agent] = ids
		return true
	}
	renamed := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			for old, now := range was {
				if r := renamedRef(id, old, now); r != id {
					id = r
					break
				}
			}
			out = append(out, id)
		}
		return out
	}
	for _, agent := range slices.Sorted(maps.Keys(ls)) {
		l := ls[agent]
		agent = strings.ToLower(strings.TrimSpace(agent))
		if agent == "" {
			return nil, errors.New("no agent")
		}
		var visible []string
		if l.Visible != nil {
			for _, n := range *l.Visible {
				for old, now := range was {
					if strings.EqualFold(n, old) {
						n = now
					}
				}
				visible = append(visible, n)
			}
			if visible == nil {
				visible = []string{}
			}
		}
		hidden := sortedIDs(renamed(l.Hidden))
		picked := sortedIDs(renamed(l.Picked))
		order := renamed(l.Order)
		a := put(&s.Visible, agent, visible, l.Visible != nil)
		b := put(&s.PickedModels, agent, picked, l.Only)
		c := put(&s.HiddenModels, agent, hidden, !l.Only && len(hidden) > 0)
		d := put(&s.OrderedModels, agent, order, len(order) > 0)
		if a || b || c || d {
			changed = append(changed, agent)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := settings.Save(s); err != nil {
		return nil, err
	}
	catalog.Touched()
	return changed, nil
}
