package provider

// Routing groups: several models, from one provider or many, that an agent
// picks as one. The gateway serves a group's id like any model's and
// routes each request over every member's keys or accounts together — a
// subscription whose allowance renews soonest first across providers, one
// resting after a failure last — rather than over one provider's.
//
// The user makes groups, in the Routing view. magpie also finds some on
// its own: a model more than one provider serves under the same name is a
// group of those, derived each time and never stored until the user
// changes one. Models of different names are only ever grouped by the
// user: nothing here guesses which models are alike.
//
// A group's member may be another group ("group/<id>"): to the group it is
// one member, which a rule can put first like a model; its models are its
// own group's, ordered by its own routing and rules. A group can't be in
// itself, however deep: SaveGroup refuses the loop, and one written into
// providers.json by hand is cut where it closes.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// GroupPrefix starts a group's id in the catalog: "group/<id>".
const GroupPrefix = "group/"

// Affinities are how long a conversation stays with the key or account that
// answered it: "" auto, as long as what the vendor cached of it is worth
// keeping; for the whole session; within a turn only, free to move when
// the user speaks again; or never.
var Affinities = []string{"", AffinitySession, AffinityTurn, AffinityOff}

const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

// EffortAuto is a group whose classifier picks each turn's reasoning
// effort (Group.Effort).
const EffortAuto = "auto"

// Ruled reports whether the group decides anything as a user's turn
// begins: a rule to put a member first, or the turn's effort.
func (g Group) Ruled() bool { return len(g.Rules) > 0 || g.Effort == EffortAuto }

// Group is a routing group.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Members  []string `json:"members"`            // "provider/model" or "group/<id>", in order
	Routing  string   `json:"routing,omitempty"`  // as Provider.Routing, over all the members' keys and accounts
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Rules send the requests they match to one member first, in order:
	// the first that matches decides (see Rule).
	Rules []Rule `json:"rules,omitempty"`
	// Classifier is the model ("provider/model", not a group) asked which
	// of the rules' intents a user's message is. Rules with an intent need
	// one; a small, fast model without reasoning does.
	Classifier string `json:"classifier,omitempty"`
	// Effort "auto" has the classifier — a decision provider's model, as
	// Jev — judge how hard each turn is to think about as it begins, and
	// the turn asks its model for that much reasoning, where the agent
	// asked for some (see EffortAuto).
	Effort string `json:"effort,omitempty"`
	// Context is how long a request the user says the group takes, in
	// tokens: agents are told it rather than its shortest member's.
	Context int `json:"context,omitempty"`
	// Family is a tag the group goes by in which agents are shown it
	// (settings' Visible), with its id.
	Family string `json:"family,omitempty"`
	// Auto is set on a group magpie found: one model served by several
	// providers. It is derived, never stored.
	Auto bool `json:"auto,omitempty"`
	// Hidden is stored for a found group the user removed.
	Hidden bool `json:"hidden,omitempty"`
}

// Member is one of a group's models as it resolves now.
type Member struct {
	ID string // the group's member it is: the model, or the group in the group it is of
	// Path is the members from the group's own down to the model: [ID] for
	// a model the group names itself, ["group/fast", "a/m"] for one of
	// its group fast, and so on down.
	Path []string
	// Via are the groups in the group it is of, the outermost first: the
	// group each of Path's members but the last names.
	Via      []Group
	Provider Provider
	Model    string // what the vendor is asked for
}

// Groups are the ids of the groups in the group the model is of, the
// outermost first.
func (m Member) Groups() []string {
	out := make([]string, len(m.Via))
	for i, g := range m.Via {
		out[i] = g.ID
	}
	return out
}

// Below is the member as the group at depth (0 the group itself, 1 the
// group in it Path[0] names, …) has it.
func (m Member) Below(depth int) Member {
	return Member{ID: m.Path[depth], Path: m.Path[depth:], Via: m.Via[depth:], Provider: m.Provider, Model: m.Model}
}

// maxNest is how deep groups in groups may go.
const maxNest = 8

// Groups lists the user's groups, then those magpie found, hidden ones
// too (marked so).
func Groups() []Group {
	return groupsIn(providerEntries())
}

func groupsIn(entries []Entry) []Group {
	f := load()
	var out []Group
	hidden := map[string]bool{}
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		out = append(out, g)
	}
	for _, g := range autoGroups(entries) {
		if slices.ContainsFunc(out, func(o Group) bool { return o.ID == g.ID }) {
			continue // the user changed it: theirs now
		}
		g.Hidden = hidden[g.ID]
		out = append(out, g)
	}
	return out
}

// autoGroups are the models more than one ready provider serves under the
// same name — however each vendor spells it (see sameModel) — in the order
// the providers were added.
func autoGroups(entries []Entry) []Group {
	var order []string
	by := map[string][]Entry{}
	for _, e := range entries {
		k := sameModel(e.Model)
		if !slices.ContainsFunc(by[k], func(o Entry) bool { return o.Provider.ID == e.Provider.ID }) {
			if by[k] == nil {
				order = append(order, k)
			}
			by[k] = append(by[k], e)
		}
	}
	var out []Group
	for _, k := range order {
		es := by[k]
		if len(es) < 2 {
			continue
		}
		// the model's own name: one a user gave it is that provider's alone
		own := func(e Entry) string {
			if e.Default != "" {
				return e.Default
			}
			return e.Name
		}
		g := Group{ID: "auto-" + Slug(k), Name: own(es[0]), Auto: true}
		for _, e := range es {
			g.Members = append(g.Members, e.ID)
			if g.Name == es[0].Model && own(e) != e.Model {
				g.Name = own(e) // a vendor that names it, over one that only lists its id
			}
		}
		out = append(out, g)
	}
	return out
}

// sameModel is a model's name as vendors agree on it: lowercase, without
// the vendor's own prefix ("anthropic/claude-sonnet-5" is claude-sonnet-5),
// with a version's dot as Anthropic writes it ("claude-opus-5.5" is
// claude-opus-5-5) and without the snapshot date some add
// ("claude-opus-5-5-20260801"). A variant after ":" (":batch") stays apart.
func sameModel(id string) string {
	k := strings.ToLower(id)
	if i := strings.LastIndex(k, "/"); i >= 0 {
		k = k[i+1:]
	}
	b := []byte(k)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && isDigit(b[i-1]) && isDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	k = string(b)
	if i := strings.LastIndex(k, "-"); i > 0 && len(k)-i-1 == 8 && strings.HasPrefix(k[i+1:], "20") && strings.Trim(k[i+1:], "0123456789") == "" {
		k = k[:i]
	}
	return k
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// FindGroup looks a group up by its catalog id ("group/<id>") and resolves
// its members; one not ready now is left out.
func FindGroup(id string) (Group, []Member, bool) {
	return GroupFinder()(id)
}

// GroupFor is the group a model's id without a provider in it names, as
// "group/<id>": the group of that id, else the group of that model however
// a vendor spells it ("grok-4.7" is the group grok-4-7 or auto-grok-4-7).
// A request for the model is the group's then, as it would be for the
// group's own id; ok is false when no group has it. An id with a provider
// in it ("a/m") names that provider's model, never a group.
func GroupFor(id string) (string, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	all := Groups()
	k := Slug(sameModel(id))
	for _, gid := range []string{strings.ToLower(id), k, "auto-" + k} {
		if g, ok := groupOf(all, gid); ok {
			return GroupPrefix + g.ID, true
		}
	}
	return "", false
}

// GroupFinder is FindGroup for looking up many: every provider's models
// are read once, when the first group is looked up, not again for each.
func GroupFinder() func(id string) (Group, []Member, bool) {
	var (
		entries []Entry
		all     []Group
		read    bool
	)
	return func(id string) (Group, []Member, bool) {
		gid, ok := strings.CutPrefix(strings.TrimSpace(id), GroupPrefix)
		if !ok {
			return Group{}, nil, false
		}
		if !read {
			entries, read = providerEntries(), true
			all = groupsIn(entries)
		}
		if g, ok := groupOf(all, gid); ok {
			return g, membersIn(entries, all, g), true
		}
		return Group{}, nil, false
	}
}

// groupOf is the group of an id among all, unless it was removed.
func groupOf(all []Group, id string) (Group, bool) {
	for _, g := range all {
		if g.ID == id && !g.Hidden {
			return g, true
		}
	}
	return Group{}, false
}

// membersIn are a group's models, ready now, in its order: a group in it
// gives its own there, as deep as they go. A model met again is left
// where it was first, and a group that would be in itself is cut there.
func membersIn(entries []Entry, all []Group, g Group) []Member {
	var out []Member
	seen := map[string]bool{}
	var walk func(g Group, path []string, via []Group, in []string)
	walk = func(g Group, path []string, via []Group, in []string) {
		for _, id := range g.Members {
			at := append(slices.Clone(path), id)
			if gid, ok := strings.CutPrefix(id, GroupPrefix); ok {
				sub, ok := groupOf(all, gid)
				if !ok || slices.Contains(in, gid) || len(via) >= maxNest {
					continue // gone, a loop, or deeper than anyone nests
				}
				walk(sub, at, append(slices.Clone(via), sub), append(slices.Clone(in), gid))
				continue
			}
			p, m, ok := resolveIn(entries, id)
			if !ok || seen[p.ID+"/"+m] {
				continue
			}
			seen[p.ID+"/"+m] = true
			out = append(out, Member{ID: at[0], Path: at, Via: via, Provider: p, Model: m})
		}
	}
	walk(g, nil, nil, []string{g.ID})
	return out
}

// groupEntries are the catalog's groups: each with a member ready, named
// as the user named it, answering for its first member when an agent asks
// what the model can do, and offering only the reasoning levels every
// member has.
func groupEntries(entries []Entry) []Entry {
	var out []Entry
	all := groupsIn(entries)
	for _, g := range all {
		if g.Hidden {
			continue
		}
		ms := membersIn(entries, all, g)
		if len(ms) == 0 {
			continue
		}
		e := Entry{ID: GroupPrefix + g.ID, Model: ms[0].Model, Name: g.Name, Provider: ms[0].Provider, Group: g.ID, Images: true}
		for i, m := range ms {
			if !slices.ContainsFunc(ms[:i], func(o Member) bool { return o.Provider.ID == m.Provider.ID }) {
				e.Icons = append(e.Icons, m.Provider.Icon) // each provider once, "" for one without
			}
			var efforts []string
			images, ctx, output := false, 0, 0
			var imageInput *bool
			for _, x := range entries {
				if x.Provider.ID == m.Provider.ID && x.Model == m.Model {
					efforts, images, ctx, output, imageInput = x.Efforts, x.Images, x.Context, x.Output, x.ImageInput
				}
			}
			if output > 0 && (e.Output == 0 || output < e.Output) {
				e.Output = output
			}
			e.Images = e.Images && images
			if ctx > 0 && (e.Context == 0 || ctx < e.Context) {
				e.Context = ctx
			}
			if i == 0 {
				e.Efforts = efforts
				e.ImageInput = imageInput
				continue
			}
			e.Efforts = slices.DeleteFunc(slices.Clone(e.Efforts), func(v string) bool { return !slices.Contains(efforts, v) })
			e.ImageInput = sharedImageInput(e.ImageInput, imageInput)
		}
		if e.ImageInput != nil && !*e.ImageInput {
			e.Images = false
		}
		ruledEntry(&e, g, ms, entries)
		if g.Context > 0 {
			e.Context = g.Context
		}
		e.Family = g.Family
		out = append(out, e)
	}
	return out
}

// SaveGroup adds or replaces a group of the user's. Changing one magpie
// found makes it the user's.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("a group's id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	g.Members = cleanList(g.Members)
	if len(g.Members) == 0 {
		return errors.New("a group needs a model in it")
	}
	if err := groupsInGroup(g, groupsIn(providerEntries())); err != nil {
		return err
	}
	for _, m := range g.Members {
		if IsDecider(m) {
			return fmt.Errorf("%s decides a group's model and effort; it holds no conversation, so it can only be the group's classifier", m)
		}
	}
	if g.Routing != Ordered && g.Routing != Rotate && g.Routing != LeastUsed {
		g.Routing = ""
	}
	if !slices.Contains(Affinities, g.Affinity) {
		g.Affinity = ""
	}
	rules, err := cleanRules(g.Rules, g.Members)
	if err != nil {
		return err
	}
	g.Rules = rules
	g.Classifier = strings.TrimPrefix(strings.TrimSpace(g.Classifier), "magpie/")
	g.Effort = strings.ToLower(strings.TrimSpace(g.Effort))
	intents := slices.ContainsFunc(g.Rules, func(r Rule) bool { return r.Intent != "" })
	switch {
	case g.Effort != "" && g.Effort != EffortAuto:
		return fmt.Errorf("a group's effort is %q or left to the agent, not %q", EffortAuto, g.Effort)
	case strings.HasPrefix(g.Classifier, GroupPrefix):
		return fmt.Errorf("the classifier is a model, not a group (%s)", g.Classifier)
	case intents && g.Classifier == "":
		return errors.New("a rule with an intent needs the group's classifier: the model that tells which intent a message is")
	case g.Effort == EffortAuto && g.Classifier == "":
		return errors.New("effort picked per turn needs the group's classifier to be Jev (a TypeSafe provider's model)")
	case !intents && g.Effort == "":
		g.Classifier = "" // nothing to ask it
	}
	if g.Classifier != "" {
		p, _, ok := Resolve(g.Classifier)
		if !ok {
			return fmt.Errorf("magpie knows no model %q to classify with", g.Classifier)
		}
		if g.Effort == EffortAuto && !p.Decides() {
			return fmt.Errorf("effort picked per turn needs the group's classifier to be Jev (a TypeSafe provider's model), not %s", g.Classifier)
		}
	}
	g.Auto, g.Hidden = false, false
	f := load()
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = g
			return store(f)
		}
	}
	f.Groups = append(f.Groups, g)
	return store(f)
}

// groupsInGroup checks the groups a group has in it: each one magpie has,
// and none that has the group in it, however deep — the group would be
// in itself, and a request to it would go round for ever.
func groupsInGroup(g Group, all []Group) error {
	for _, id := range g.Members {
		gid, ok := strings.CutPrefix(id, GroupPrefix)
		if !ok {
			continue
		}
		if gid == g.ID {
			return fmt.Errorf("%s can't be in itself", g.Name)
		}
		sub, ok := groupOf(all, gid)
		if !ok {
			return fmt.Errorf("magpie has no group %q to put in %s", gid, g.Name)
		}
		if way := wayTo(all, sub, g.ID, nil); way != nil {
			return fmt.Errorf("%s can't be in %s: %s is in it already (%s), so %s would be in itself",
				sub.Name, g.Name, g.Name, strings.Join(append(append([]string{sub.ID}, way...), g.ID), " ⊃ "), g.Name)
		}
		if d := depthOf(all, sub, nil); d+1 > maxNest {
			return fmt.Errorf("%s has groups in it %d deep; a group in a group goes at most %d deep", sub.Name, d, maxNest)
		}
	}
	return nil
}

// wayTo is the groups from g down to the group id, when g has it in it
// however deep (its first step first, the id itself left out); nil when
// it hasn't.
func wayTo(all []Group, g Group, id string, in []string) []string {
	for _, m := range g.Members {
		gid, ok := strings.CutPrefix(m, GroupPrefix)
		if !ok || slices.Contains(in, gid) {
			continue
		}
		if gid == id {
			return []string{}
		}
		if sub, ok := groupOf(all, gid); ok {
			if way := wayTo(all, sub, id, append(slices.Clone(in), g.ID)); way != nil {
				return append([]string{gid}, way...)
			}
		}
	}
	return nil
}

// depthOf is how deep the groups in g go: 0 when it has none.
func depthOf(all []Group, g Group, in []string) int {
	d := 0
	for _, m := range g.Members {
		gid, ok := strings.CutPrefix(m, GroupPrefix)
		if !ok || slices.Contains(in, gid) {
			continue
		}
		if sub, ok := groupOf(all, gid); ok {
			d = max(d, 1+depthOf(all, sub, append(slices.Clone(in), g.ID)))
		}
	}
	return d
}

// GroupsWith are the groups that have the group id in them as a member.
func GroupsWith(id string) []Group {
	var out []Group
	for _, g := range groupsIn(providerEntries()) {
		if !g.Hidden && slices.Contains(g.Members, GroupPrefix+id) {
			out = append(out, g)
		}
	}
	return out
}

// DeleteGroup removes a group of the user's; one magpie found is hidden,
// to come back with ShowGroup. A group another has in it stays until it is
// taken out of that one.
func DeleteGroup(id string) error {
	if in := GroupsWith(id); len(in) > 0 {
		var names []string
		for _, g := range in {
			names = append(names, g.Name)
		}
		return fmt.Errorf("%s is in %s: take it out first", id, strings.Join(names, ", "))
	}
	f := load()
	found := false
	f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool {
		if g.ID == id {
			found = true
			return true
		}
		return false
	})
	if slices.ContainsFunc(autoGroups(providerEntries()), func(g Group) bool { return g.ID == id }) {
		f.Groups = append(f.Groups, Group{ID: id, Hidden: true})
		found = true
	}
	if !found {
		return fmt.Errorf("no group %q", id)
	}
	return store(f)
}

// RemovedGroups are the found groups the user removed, whether or not
// magpie finds them now: each is a record in providers.json ({"id", "hidden":
// true}) that keeps it removed when two providers serve its model again.
func RemovedGroups() []string {
	var out []string
	for _, g := range load().Groups {
		if g.Hidden {
			out = append(out, g.ID)
		}
	}
	return out
}

// ShowGroup brings back a group magpie found that the user had removed.
func ShowGroup(id string) error {
	f := load()
	f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool { return g.ID == id && g.Hidden })
	return store(f)
}

// RenameGroup gives a group another id, the one agents pick it by
// (group/<id>). A group magpie found becomes the user's under the new id,
// the found one kept removed so it doesn't come back beside it. The
// groups that have it in them, and their rules, name it by the new id.
func RenameGroup(from, to string) error {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	if to == "" || to != Slug(to) {
		return fmt.Errorf("a group's id must be lowercase letters, digits and dashes, not %q", to)
	}
	if to == from {
		return nil
	}
	all := groupsIn(providerEntries())
	g, ok := groupOf(all, from)
	if !ok {
		return fmt.Errorf("no group %q", from)
	}
	f := load()
	if slices.ContainsFunc(all, func(o Group) bool { return o.ID == to }) ||
		slices.ContainsFunc(f.Groups, func(o Group) bool { return o.ID == to }) {
		return fmt.Errorf("there is a group %q already", to)
	}
	found := slices.ContainsFunc(autoGroups(providerEntries()), func(o Group) bool { return o.ID == from })
	g.ID, g.Auto, g.Hidden = to, false, false
	f.Groups = slices.DeleteFunc(f.Groups, func(o Group) bool { return o.ID == from })
	if found {
		f.Groups = append(f.Groups, Group{ID: from, Hidden: true})
	}
	f.Groups = append(f.Groups, g)
	old, now := GroupPrefix+from, GroupPrefix+to
	for i := range f.Groups {
		for j, m := range f.Groups[i].Members {
			if m == old {
				f.Groups[i].Members[j] = now
			}
		}
		for j, r := range f.Groups[i].Rules {
			if r.Use == old {
				f.Groups[i].Rules[j].Use = now
			}
		}
	}
	return store(f)
}
