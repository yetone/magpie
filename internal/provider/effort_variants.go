package provider

// A relay the user added by its address may list one model once for each
// reasoning effort it serves it at, the effort baked into the id
// (gemini-4-argon-low, -medium, -high; elis_pina on X, after #6582), so the
// editor's chips and every agent's picker showed four models where there is
// one, and an agent that also picks an effort had two to pick. magpie offers
// such a family as one model, gemini-4-argon, with the levels the relay
// lists as its effort variants (catalog.Model.Efforts, as Antigravity's
// families are, antigravity_models.go), and the gateway asks the relay for
// the id the request's effort picks (Provider.SentIDAt).
//
// What folds, and what doesn't:
//   - only an id whose last "-" part is exactly an effort word, in any case:
//     minimal, low, medium, high or xhigh (relayEffortWords). -max and -none
//     are not read, as they name models of their own (gpt-5.1-codex-max,
//     qwen3.8-max), and nor is any other word (-highspeed, -lite, -tiered,
//     -preview, -thinking, -fast);
//   - the id without the word must name a version, a digit in its last part,
//     so a size word (mistral-medium, vision-high) is not taken for an
//     effort, as effortBase reads prices;
//   - only a family of two or more such ids: a lone foo-high is listed as it
//     is, whether foo is listed or not. foo itself, when listed, is the
//     family's model asked with no effort;
//   - never a family the user named a member of (SetModelID's ids), nor one
//     whose model id is an id the user gave another model: their own word
//     on the list stands;
//   - only a provider the user added by its address (AddedByAddress), not an
//     Azure, Bedrock or Vertex one, never a preset's, an account's or a
//     plugin's: their catalogs say their own levels (Antigravity's, Devin's,
//     Cursor's families are folded by rules of their own).
//
// An id of a member stays a model of the provider's: a pick of it, an
// agent's config, a routing rule or a key's allow-list naming it reaches the
// relay under that very id, at whatever effort it is asked (SentIDAt), as
// before the family was folded.

import (
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
)

// relayEffortWords are the words after an id's last "-" that name the
// effort a relay serves the model at, weakest first.
var relayEffortWords = []string{"minimal", "low", "medium", "high", "xhigh"}

// splitRelayEffort is the model an id names at an effort, and that
// effort, lowercase: gemini-4-argon-High is gemini-4-argon at high. ok is
// false for an id with no effort word, or whose rest names no version.
func splitRelayEffort(id string) (base, effort string, ok bool) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return "", "", false
	}
	w := strings.ToLower(id[i+1:])
	if !slices.Contains(relayEffortWords, w) {
		return "", "", false
	}
	base = id[:i]
	if !strings.ContainsAny(base[strings.LastIndexByte(base, '/')+1:], "0123456789") {
		return "", "", false
	}
	return base, w, true
}

// relayFamily is one model a relay lists at several efforts.
type relayFamily struct {
	id      string            // the model, the ids without their effort
	bare    bool              // whether the relay lists id itself too
	byLevel map[string]string // each effort's id
	levels  []string          // the efforts listed, weakest first
}

// members are the family's ids: its own, when listed, and each effort's.
func (f *relayFamily) members() []string {
	out := make([]string, 0, len(f.levels)+1)
	if f.bare {
		out = append(out, f.id)
	}
	for _, l := range f.levels {
		out = append(out, f.byLevel[l])
	}
	return out
}

// def is the id asked for when a request names no effort: the model's own
// id when the relay lists it, as the plain model is what such a request
// asks; else its medium, the default effort of most models; else the level
// nearest medium, a tie going up (nearestEffort).
func (f *relayFamily) def() string {
	if f.bare {
		return f.id
	}
	return f.byLevel[nearestEffort("medium", f.levels)]
}

// at is the id a request at effort goes to: the one of that effort, else
// the one of the family's nearest level (nearestEffort, as the gateway fits
// an effort to a model's levels); no effort is the family's default (def).
func (f *relayFamily) at(effort string) string {
	if effort == "" {
		return f.def()
	}
	if id := f.byLevel[nearestEffort(strings.ToLower(effort), f.levels)]; id != "" {
		return id
	}
	return f.def()
}

// model is the family as one model, from the models listed (by id, as
// Decorate left them): what its default id says, under the family's id and
// name, with the levels listed; the least window and output of its members,
// and images only where every member takes them.
func (f *relayFamily) model(byID map[string]catalog.Model) catalog.Model {
	def := f.def()
	m := byID[def]
	name := m.Name
	if !f.bare {
		name = familyName(m.Name, m.ID, f.id, f.byLevel)
	}
	m.ID, m.Name = f.id, name
	m.Efforts, m.EffortsBorrowed, m.Reasoning = slices.Clone(f.levels), false, true
	var keys []string
	every := false
	for _, id := range f.members() {
		v, ok := byID[id]
		if !ok {
			continue
		}
		if v.Context > 0 && (m.Context == 0 || v.Context < m.Context) {
			m.Context = v.Context
		}
		if v.Output > 0 && (m.Output == 0 || v.Output < m.Output) {
			m.Output = v.Output
		}
		m.Images = m.Images && v.Images
		// keys whose list has any member serve the family; one listed to
		// every key (no keys named) serves it on every key
		if len(v.Keys) == 0 {
			every = true
		}
		for _, k := range v.Keys {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	if every {
		keys = nil
	}
	m.Keys = keys
	return m
}

// familyName is the family's name from one member's: the name without the
// effort it says ("Gemini 4 Argon (High)", "Gemini 4 Argon High"), or the
// family's id when the name says nothing more than the member's id, for the
// catalog to name it (catalog.Named).
func familyName(name, id, family string, byLevel map[string]string) string {
	if name == "" || name == id {
		return family
	}
	if n, l := antigravityNameEffort(name); l != "" && byLevel[l] != "" {
		return n
	}
	if i := strings.LastIndexByte(name, ' '); i > 0 {
		if l := strings.ToLower(name[i+1:]); slices.Contains(relayEffortWords, l) {
			return strings.TrimSpace(name[:i])
		}
	}
	return family
}

// relayFamilies are the families of a relay's list: each one's id, and the
// family each member id (its own included) is of.
type relayFamilies struct {
	by map[string]*relayFamily
	of map[string]string
}

// relayFamiliesIn are the families of ms, the relay's chat models: each id
// with an effort word that has a sibling at another effort.
func relayFamiliesIn(ms []catalog.Model) *relayFamilies {
	x := &relayFamilies{}
	listed := make(map[string]bool, len(ms))
	var found map[string]*relayFamily
	var order []string
	twice := map[string]bool{} // a family listing one effort twice (Foo-high, foo-HIGH)
	for _, m := range ms {
		listed[m.ID] = true
		base, l, ok := splitRelayEffort(m.ID)
		if !ok {
			continue
		}
		if found == nil {
			found = map[string]*relayFamily{}
		}
		f := found[base]
		if f == nil {
			f = &relayFamily{id: base, byLevel: map[string]string{}}
			found[base] = f
			order = append(order, base)
		}
		if had, ok := f.byLevel[l]; ok && had != m.ID {
			twice[base] = true
			continue
		}
		f.byLevel[l] = m.ID
	}
	for _, base := range order {
		f := found[base]
		if len(f.byLevel) < 2 || twice[base] {
			continue
		}
		for _, l := range relayEffortWords {
			if f.byLevel[l] != "" {
				f.levels = append(f.levels, l)
			}
		}
		f.bare = listed[base]
		if x.by == nil {
			x.by, x.of = map[string]*relayFamily{}, map[string]string{}
		}
		x.by[base] = f
		for _, id := range f.members() {
			x.of[id] = base
		}
	}
	return x
}

// relayFamiliesMade are each provider's families, made again when its list
// is read anew (as keyListsMade): a list read once is the same list until
// its file changes, so a request's look at one model doesn't walk the list.
var relayFamiliesMade = struct {
	sync.Mutex
	m map[string]familiesFrom
}{m: map[string]familiesFrom{}}

type familiesFrom struct {
	first *catalog.Model
	n     int
	x     *relayFamilies
}

// foldsEfforts is whether p's list is read for effort families: a
// provider the user added by its address, OpenAI-compatible or Anthropic's,
// and not an Azure, Bedrock or Vertex one (whose names are their own).
func (p Provider) foldsEfforts() bool {
	return p.AddedByAddress() && !p.IsAzure() && !p.IsBedrock() && !p.IsVertex() && !p.IsRemoteMagpie()
}

// relayFamilies are the effort families of p's fetched list, nil for a
// provider that folds none.
func (p Provider) relayFamilies() *relayFamilies {
	if !p.foldsEfforts() || !p.lists() {
		return nil
	}
	return familiesOf(p.ID)
}

// familiesOf are the effort families of the list fetched for the provider
// of id pid, whatever the provider: a list read once is read for them once.
func familiesOf(pid string) *relayFamilies {
	all, ok := catalog.LiveAll(pid)
	if !ok || len(all) == 0 {
		return nil
	}
	relayFamiliesMade.Lock()
	defer relayFamiliesMade.Unlock()
	if f, ok := relayFamiliesMade.m[pid]; ok && f.first == &all[0] && f.n == len(all) {
		return f.x
	}
	x := relayFamiliesIn(catalog.Chat(all))
	relayFamiliesMade.m[pid] = familiesFrom{&all[0], len(all), x}
	return x
}

// family is the family model is the id of, folded on p: not one the user
// named a member of or gave model as the id of another (SetModelID).
func (p Provider) family(model string) (*relayFamily, bool) {
	x := p.relayFamilies()
	if x == nil || x.by[model] == nil {
		return nil, false
	}
	f := x.by[model]
	return f, familyFolds(f, modelIDsOf(p.ID))
}

// familyFolds is whether f is offered as one model with the user's ids
// as they are: none of its members listed by an id the user gave it, and
// its id not one the user gave another model.
func familyFolds(f *relayFamily, ids localIDs) bool {
	if ids.none() {
		return true
	}
	if _, ok := ids.own[f.id]; ok {
		return false
	}
	for _, id := range f.members() {
		if _, ok := ids.local[id]; ok {
			return false
		}
	}
	return true
}

// foldEffortVariants is ms, p's listed models as Decorate left them, with
// each effort family one model, in the place its first member had.
func (p Provider) foldEffortVariants(ms []catalog.Model) []catalog.Model {
	x := p.relayFamilies()
	if x == nil || len(x.by) == 0 {
		return ms
	}
	ids := modelIDsOf(p.ID)
	byID := make(map[string]catalog.Model, len(ms))
	for _, m := range ms {
		if _, ok := x.of[m.ID]; ok {
			byID[m.ID] = m
		}
	}
	out := make([]catalog.Model, 0, len(ms))
	done := map[string]bool{}
	for _, m := range ms {
		base, ok := x.of[m.ID]
		if !ok || !familyFolds(x.by[base], ids) {
			out = append(out, m)
			continue
		}
		if done[base] {
			continue
		}
		done[base] = true
		if _, ok := byID[x.by[base].def()]; !ok {
			// its default isn't among the models listed now: as listed
			for _, id := range x.by[base].members() {
				if v, ok := byID[id]; ok {
					out = append(out, v)
				}
			}
			continue
		}
		out = append(out, x.by[base].model(byID))
	}
	return out
}

// withFamilyIDs is set, ids of p's fetched list, with each folded family's
// own id: a pick of the family follows the list as a pick of one listed id
// does (dropGonePicks).
func (p Provider) withFamilyIDs(set map[string]bool) map[string]bool {
	x := p.relayFamilies()
	if x == nil || len(x.by) == 0 || len(set) == 0 {
		return set
	}
	ids := modelIDsOf(p.ID)
	for base, f := range x.by {
		if familyFolds(f, ids) {
			set[base] = true
		}
	}
	return set
}

// SentIDAt is the id a request for model at effort goes to p under: for a
// family of a relay's effort ids offered as one model, the id of the
// effort asked (Provider.relayFamily.at: that level, else the nearest the
// relay lists, else with none asked the model's own id when listed, else
// medium); any other id as it is, an id of one of the family's members too,
// at whatever effort, as it went before the family was one model. The
// user's name for the model upstream (UpstreamNameAs) is put on after.
func (p Provider) SentIDAt(model, effort string) string {
	if f, ok := p.family(model); ok {
		return f.at(effort)
	}
	return model
}

// SentIDOf is SentIDAt for the provider of id pid, for a caller that holds
// only the id (a record's, read for each of a ledger's): an id that names
// no family of the list fetched for pid goes as it is without the
// providers read, and so does one of no provider's.
func SentIDOf(pid, model, effort string) string {
	if x := familiesOf(pid); x == nil || x.by[model] == nil {
		return model
	}
	all := sharedAll()
	if i := slices.IndexFunc(all, func(p Provider) bool { return p.ID == pid }); i >= 0 {
		return all[i].SentIDAt(model, effort)
	}
	return model
}

// liveFamily are the entries of the default id of the family model is, with
// model's id: what a look at one model by id (liveByID) reads for a family,
// whose own id the relay may not list.
func (p Provider) liveFamily(model string) []catalog.Model {
	f, ok := p.family(model)
	if !ok || f.bare {
		return nil
	}
	out := slices.Clone(catalog.LiveByIDShared(p.ID, f.def()))
	for i := range out {
		out[i].ID = model
	}
	return out
}
