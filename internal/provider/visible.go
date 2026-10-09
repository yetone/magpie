package provider

import (
	"errors"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Which of the catalog an agent is shown. Settings' Visible names, for an
// agent, the families its lists hold: a family is the tag providers and
// groups are given (magpie provider set <id> family=relay), and a
// provider's or group's id names it alone. The gateway's model list and
// what magpie writes into the agents' files both come from CatalogFor, so
// the two never differ.

// Names are what an entry answers to in a visibility: its family, and its
// provider's id or its group's id (as group/<id> too).
func (e Entry) Names() []string {
	var out []string
	if e.Family != "" {
		out = append(out, e.Family)
	}
	if e.Group != "" {
		return append(out, e.Group, GroupPrefix+e.Group)
	}
	return append(out, e.Provider.ID)
}

// VisibleTo is what agent's lists are narrowed to, and whether they are.
func VisibleTo(agent string) ([]string, bool) {
	names, ok := heldSettings().Visible[strings.ToLower(agent)]
	return slices.Clone(names), ok
}

// Shows reports whether a visibility shows e.
func Shows(names []string, e Entry) bool {
	for _, n := range e.Names() {
		if slices.ContainsFunc(names, func(v string) bool { return strings.EqualFold(v, n) }) {
			return true
		}
	}
	return false
}

// Described is set by the gateway: whether an image sent to a model that
// can't see is described to it by one that can (Settings › Vision). Agents
// are then told every model takes images; told a model is text-only, they
// turn the user's image away before magpie is asked (Codex: "does not
// support image input").
var Described func() bool

// described is Described, false before the gateway sets it.
func described() bool { return Described != nil && Described() }

// CatalogFor is the catalog as agent is shown it, and what is kept from it
// (none when its lists aren't narrowed): its visibility's, less the models
// taken out of its lists one by one (HiddenModels), or, when it is shown
// only the models picked for it, those alone (PickedModels).
func CatalogFor(agent string) (shown, hidden []Entry) {
	listed, hidden := ListedFor(agent)
	off := ModelOff(agent)
	for _, e := range listed {
		if off(e.ID) {
			hidden = append(hidden, e)
		} else {
			shown = append(shown, e)
		}
	}
	return shown, hidden
}

// ListedFor is the catalog agent's visibility gives it, the models it may
// pick to show or not among, and what the visibility keeps from it.
func ListedFor(agent string) (listed, kept []Entry) {
	all := Catalog()
	names, ok := VisibleTo(agent)
	if !ok {
		return InOrder(agent, all), nil
	}
	for _, e := range all {
		if Shows(names, e) {
			listed = append(listed, e)
		} else {
			kept = append(kept, e)
		}
	}
	return InOrder(agent, listed), kept
}

// HiddenModels are the ids of the entries taken out of agent's lists.
func HiddenModels(agent string) map[string]bool {
	ids := heldSettings().HiddenModels[strings.ToLower(agent)]
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// PickedModels are the only entries agent's lists show, by id, and whether
// it is shown only those (#1337): a model not among them, one that came
// after the user switched to it among them, is not shown.
func PickedModels(agent string) (map[string]bool, bool) {
	ids, only := heldSettings().PickedModels[strings.ToLower(agent)]
	if !only {
		return nil, false
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, true
}

// ModelOff reports whether agent's lists leave an entry out by the picks
// made one model at a time: every one not picked, when it is shown only
// the models picked for it, else the ones taken out (HiddenModels).
func ModelOff(agent string) func(id string) bool {
	if on, only := PickedModels(agent); only {
		return func(id string) bool { return !on[id] }
	}
	off := HiddenModels(agent)
	return func(id string) bool { return off[id] }
}

// SetOnlyPicked switches agent between being shown every model not taken
// out of its lists, a new one among them, and being shown only the ones
// picked for it (#1337). Either way the models it is shown now stay as
// they are: switched on, they are its picks; switched off, the ones listed
// for it and not picked are taken out, and a model that comes later is
// shown again.
func SetOnlyPicked(agent string, on bool) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	if _, only := PickedModels(agent); only == on {
		return nil
	}
	listed, _ := ListedFor(agent)
	off := ModelOff(agent)
	var shown, hidden []string
	for _, e := range listed {
		if off(e.ID) {
			hidden = append(hidden, e.ID)
		} else {
			shown = append(shown, e.ID)
		}
	}
	s := settings.Load()
	if on {
		if s.PickedModels == nil {
			s.PickedModels = map[string][]string{}
		}
		s.PickedModels[agent] = sortedIDs(shown)
		delete(s.HiddenModels, agent)
	} else {
		delete(s.PickedModels, agent)
		if hidden = sortedIDs(hidden); len(hidden) > 0 {
			if s.HiddenModels == nil {
				s.HiddenModels = map[string][]string{}
			}
			s.HiddenModels[agent] = hidden
		} else {
			delete(s.HiddenModels, agent)
		}
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetPickedModels makes these entries the only ones agent's lists show, for
// an agent shown only the models picked for it. A pick of an entry not
// listed for it now (its provider switched off for a while, a model its
// vendor dropped from the list) is kept, so it is shown again when it is
// back, as it was picked.
func SetPickedModels(agent string, ids []string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	was, only := PickedModels(agent)
	if !only {
		return errors.New(agent + " is shown every model not taken out of its lists, not only the ones picked")
	}
	listed, _ := ListedFor(agent)
	now := map[string]bool{}
	for _, e := range listed {
		now[e.ID] = true
	}
	keep := slices.Clone(ids)
	for id := range was {
		if !now[id] {
			keep = append(keep, id)
		}
	}
	keep = sortedIDs(keep)
	s := settings.Load()
	if slices.Equal(s.PickedModels[agent], keep) {
		return nil
	}
	if s.PickedModels == nil {
		s.PickedModels = map[string][]string{}
	}
	s.PickedModels[agent] = keep
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// sortedIDs is ids trimmed, without blanks or twins, sorted; never nil, so
// an agent shown only its picks with none picked is still saved as one.
func sortedIDs(ids []string) []string {
	keep := []string{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(keep, id) {
			keep = append(keep, id)
		}
	}
	slices.Sort(keep)
	return keep
}

// SetHiddenModels takes these entries out of agent's lists, and puts back
// every other; none shows it every model its visibility gives it. The
// agents that keep the models in files of their own are told.
func SetHiddenModels(agent string, ids []string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	var keep []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(keep, id) {
			keep = append(keep, id)
		}
	}
	slices.Sort(keep)
	s := settings.Load()
	if slices.Equal(s.HiddenModels[agent], keep) {
		return nil
	}
	if len(keep) == 0 {
		delete(s.HiddenModels, agent)
	} else {
		if s.HiddenModels == nil {
			s.HiddenModels = map[string][]string{}
		}
		s.HiddenModels[agent] = keep
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// Families are the families providers and groups are tagged with, sorted.
func Families() []string {
	var out []string
	add := func(f string) {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	for _, p := range All() {
		add(p.Family)
	}
	for _, g := range Groups() {
		add(g.Family)
	}
	slices.Sort(out)
	return out
}

// ModelOrder is the order the user put agent's models in (OrderedModels),
// by entry id; none when they never did.
func ModelOrder(agent string) []string {
	return slices.Clone(heldSettings().OrderedModels[strings.ToLower(agent)])
}

// SetModelOrder lists agent's models in this order, by entry id: the ones
// it names first, as it names them, then every other as before; none puts
// them back in magpie's own order. Only agent's lists follow it.
func SetModelOrder(agent string, ids []string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	var keep []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(keep, id) {
			keep = append(keep, id)
		}
	}
	s := settings.Load()
	if slices.Equal(s.OrderedModels[agent], keep) {
		return nil
	}
	if len(keep) == 0 {
		delete(s.OrderedModels, agent)
	} else {
		if s.OrderedModels == nil {
			s.OrderedModels = map[string][]string{}
		}
		s.OrderedModels[agent] = keep
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// InOrder is es in agent's order: the entries ModelOrder names first, as it
// names them, then the rest as they come — a model added since goes after.
// Codex's own, a ChatGPT account's models, lead the rest, as Codex's list
// has them before magpie's.
func InOrder(agent string, es []Entry) []Entry {
	order := ModelOrder(agent)
	if len(order) == 0 {
		return es
	}
	at := make(map[string]int, len(order))
	for i, id := range order {
		at[id] = i
	}
	out := slices.Clone(es)
	rank := func(e Entry) int {
		if i, ok := at[e.ID]; ok {
			return i
		}
		if strings.EqualFold(agent, "codex") && !CodexOwn(e) {
			return len(order) + 1
		}
		return len(order)
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return rank(a) - rank(b) })
	return out
}

// CodexOwn reports whether e is one of a ChatGPT account's own models,
// which the ChatGPT backend lists to Codex by its bare slug (e.Model).
func CodexOwn(e Entry) bool {
	return e.Group == "" && e.Provider.Account != nil && e.Provider.Account.Agent == "codex"
}
