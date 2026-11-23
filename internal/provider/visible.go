package provider

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"

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
// provider's id or its group's id (as group/<id> too, and a found group's
// auto/<model>).
func (e Entry) Names() []string {
	var out []string
	if e.Family != "" {
		out = append(out, e.Family)
	}
	if e.Group != "" {
		out = append(out, e.Group, GroupPrefix+e.Group)
		if e.ID != GroupPrefix+e.Group { // a found group's auto/<model>
			out = append(out, e.ID)
		}
		return out
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
//
// It is kept across requests with the catalog (keptOf): a window poll
// asks it for every agent, and building each again from the whole
// catalog was ~140 MB of what twenty polls allocated. Each caller gets
// its own copy of the lists.
func CatalogFor(agent string) (shown, hidden []Entry) {
	c := catalogKept(agent)
	return slices.Clone(c.shown), slices.Clone(c.hidden)
}

// catalogKept is CatalogFor's lists as kept, only for reading.
func catalogKept(agent string) catalogLists {
	return keptOf("catalog-for\x00"+agent, func() catalogLists {
		catalogsBuilt.Add(1)
		s, h := catalogFor(agent)
		return catalogLists{slices.Clip(s), slices.Clip(h)}
	})
}

type catalogLists struct{ shown, hidden []Entry }

// catalogsBuilt counts the lists CatalogFor built, for a test.
var catalogsBuilt atomic.Int64

func catalogFor(agent string) (shown, hidden []Entry) {
	listed, hidden := ListedFor(agent)
	off := ModelOff(agent)
	for _, e := range listed {
		if off(e.ID) {
			hidden = append(hidden, e)
		} else {
			shown = append(shown, e)
		}
	}
	// the windows the user set for this agent alone (settings.AgentContexts)
	shown = withAgentWindows(heldSettings(), agent, shown)
	return shown, hidden
}

// ListedFor is the catalog agent's visibility gives it, the models it may
// pick to show or not among, and what the visibility keeps from it.
func ListedFor(agent string) (listed, kept []Entry) {
	all := catalogOf(agent)
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

// HiddenModels are the ids of the entries taken out of agent's lists, a
// found group saved by its old id, group/auto-<slug>, by the one it is
// listed by now (PublicRef).
func HiddenModels(agent string) map[string]bool {
	ids := heldSettings().HiddenModels[strings.ToLower(agent)]
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[PublicRef(id)] = true
	}
	return out
}

// PickedModels are the only entries agent's lists show, by id (a found
// group by the id it is listed by now, PublicRef), and whether
// it is shown only those (#1337): a model not among them, one that came
// after the user switched to it among them, is not shown.
func PickedModels(agent string) (map[string]bool, bool) {
	ids, only := heldSettings().PickedModels[strings.ToLower(agent)]
	if !only {
		return nil, false
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[PublicRef(id)] = true
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

// Widen lets agent's visibility give it these entries, of those it keeps
// now (whysugarsweet on X: Claude Desktop narrowed to five providers, set
// from the terminal, never listed OpenCode Go added later, and the Agents
// page's list had no way to it): each one's provider, or its group, is
// added to the names the agent is narrowed to, so the provider's other
// models are listed too, for its list to show or not. When nothing is
// kept after, the agent is narrowed no more. An id not kept is passed by.
func Widen(agent string, ids []string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	names, narrowed := VisibleTo(agent)
	if !narrowed || len(ids) == 0 {
		return nil
	}
	_, kept := ListedFor(agent)
	added := false
	for _, e := range kept {
		if !slices.Contains(ids, e.ID) {
			continue
		}
		n := e.Provider.ID
		if e.Group != "" {
			n = e.Group
		}
		if !slices.ContainsFunc(names, func(v string) bool { return strings.EqualFold(v, n) }) {
			names, added = append(names, n), true
		}
	}
	if !added {
		return nil
	}
	s := settings.Load()
	if slices.ContainsFunc(kept, func(e Entry) bool { return !Shows(names, e) }) {
		if s.Visible == nil {
			s.Visible = map[string][]string{}
		}
		s.Visible[agent] = names
	} else {
		delete(s.Visible, agent)
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
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
	// a model taken out that its provider's list lacks now (ZCode's
	// -Trial entries are listed from 23:00 only) isn't in the list the
	// editor sent, so it stays taken out for when it is listed again, as
	// SetPickedModels keeps a pick of it
	if was := HiddenModels(agent); len(was) > 0 {
		listed, _ := ListedFor(agent)
		now := map[string]bool{}
		for _, e := range listed {
			now[e.ID] = true
		}
		for id := range was {
			if !now[id] && !slices.Contains(keep, id) {
				keep = append(keep, id)
			}
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

// OwnModelsHidden reports whether agent's pickers leave out the models it
// reaches on its own, offering magpie's alone (#1642).
func OwnModelsHidden(agent string) bool {
	return slices.Contains(heldSettings().OwnModelsHidden, strings.ToLower(strings.TrimSpace(agent)))
}

// SetOwnModelsHidden switches agent's pickers between offering its own
// models beside magpie's and offering magpie's alone.
func SetOwnModelsHidden(agent string, on bool) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	s := settings.Load()
	if slices.Contains(s.OwnModelsHidden, agent) == on {
		return nil
	}
	if on {
		s.OwnModelsHidden = append(s.OwnModelsHidden, agent)
		slices.Sort(s.OwnModelsHidden)
	} else {
		s.OwnModelsHidden = slices.DeleteFunc(s.OwnModelsHidden, func(a string) bool { return a == agent })
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	// an agent's own config says the switch too (Claude Code's modelPicker
	// replaceBuiltInOptions, #4548), so it is written again now, as a
	// hidden model's change is
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
	for _, g := range sharedGroups() {
		add(g.Family)
	}
	slices.Sort(out)
	return out
}

// ModelOrder is the order the user put agent's models in (OrderedModels),
// by entry id, a found group by the id it is listed by now (PublicRef);
// none when they never did.
func ModelOrder(agent string) []string {
	ids := slices.Clone(heldSettings().OrderedModels[strings.ToLower(agent)])
	for i, id := range ids {
		ids[i] = PublicRef(id)
	}
	return ids
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

// ShowFor puts entry id back in agent's lists, as the user picks it in
// one of the agent's pickers where its lists leave it out (a routing
// group made after the agent was narrowed, or shown only its picks): its
// provider or group is added to the names the agent is narrowed to, and
// it is picked for an agent shown only its picks, else no longer taken
// out. Nothing else in the lists changes.
func ShowFor(agent, id string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if id = strings.TrimSpace(id); agent == "" || id == "" {
		return errors.New("no agent or model")
	}
	if err := Widen(agent, []string{id}); err != nil {
		return err
	}
	s := settings.Load()
	changed := false
	if picks, only := s.PickedModels[agent]; only {
		if !slices.ContainsFunc(picks, func(p string) bool { return PublicRef(p) == id }) {
			s.PickedModels[agent], changed = sortedIDs(append(slices.Clone(picks), id)), true
		}
	} else if was := s.HiddenModels[agent]; slices.ContainsFunc(was, func(h string) bool { return PublicRef(h) == id }) {
		keep := slices.DeleteFunc(slices.Clone(was), func(h string) bool { return PublicRef(h) == id })
		if len(keep) == 0 {
			delete(s.HiddenModels, agent)
		} else {
			s.HiddenModels[agent] = keep
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// PicksJoin reports whether agent, shown only the models picked for it,
// takes in the models picked for a provider as well (eiyuuzaki2333 on X):
// a model ticked in a provider's editor is ticked for it too.
func PicksJoin(agent string) bool {
	return slices.Contains(heldSettings().PicksJoin, strings.ToLower(strings.TrimSpace(agent)))
}

// SetPicksJoin switches whether agent takes in the models picked for a
// provider. It changes no pick: only the models picked from now on join.
func SetPicksJoin(agent string, on bool) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return errors.New("no agent")
	}
	s := settings.Load()
	if slices.Contains(s.PicksJoin, agent) == on {
		return nil
	}
	if on {
		s.PicksJoin = append(s.PicksJoin, agent)
		slices.Sort(s.PicksJoin)
	} else {
		s.PicksJoin = slices.DeleteFunc(s.PicksJoin, func(a string) bool { return a == agent })
	}
	return settings.Save(s)
}

// JoinPicks ticks the models picked for p now (its Models) that before,
// p as it was saved (nil: just added), didn't serve, for every agent shown
// only its picks that takes in a provider's (PicksJoin), and returns those
// agents, sorted. Only a pick the user made joins: a provider serving its
// vendor's list (none picked) gives none, nor does a model its vendor
// lists later, nor one it served before it was picked (a first untick in
// a provider serving its vendor's list makes the rest picks). An agent
// shown every model not taken out is shown a new one already, and one that
// isn't shown only its picks keeps its list as the user left it.
func JoinPicks(p Provider, before *Provider) ([]string, error) {
	if p.ID == "" || p.NoModels {
		return nil, nil
	}
	had := map[string]bool{}
	if before != nil && !before.NoModels {
		for _, m := range before.Models {
			had[before.ListedModelID(strings.TrimSpace(m))] = true
		}
		if len(before.Models) == 0 {
			for _, m := range before.Exposed() {
				had[m.ID] = true
			}
		}
	}
	var add []string
	for _, m := range normalModels(p.Models) {
		if id := p.ListedModelID(m); !had[id] {
			add = append(add, p.ID+"/"+id)
		}
	}
	if len(add) == 0 {
		return nil, nil
	}
	s := settings.Load()
	var changed []string
	for _, a := range s.PicksJoin {
		picks, only := s.PickedModels[a]
		if !only {
			continue
		}
		next := sortedIDs(append(slices.Clone(picks), add...))
		if slices.Equal(next, picks) {
			continue
		}
		s.PickedModels[a] = next
		changed = append(changed, a)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := settings.Save(s); err != nil {
		return nil, err
	}
	catalog.Touched()
	slices.Sort(changed)
	return changed, nil
}
