package provider

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A model can be listed at its provider by an id the user gives it
// (settings' ModelIDs), for a provider that serves one model under ids of
// its own (zeewell on X: deepseek-v4.1-flash-1017 at one relay,
// grok-4.6@500k at another). Everything magpie lists, picks, groups, finds
// routing groups by, limits a gateway key to and records usage under is
// that id; a request for it goes out under the provider's own, through
// the same wire names (settings' Wires) a ModelWires name goes out by.
// It is ModelWires turned round: a wire keeps magpie's id and changes the
// one sent, this keeps the one sent and changes magpie's.
//
// An account's and a plugin's provider take one too (zeewell on X:
// SuperGrok's grok-4.6@500k as grok-4.6, Cursor's, Devin's and
// WorkBuddy's plugin models). The body's model goes out by the wire name
// as at any provider, and what is read of the model by the vendor's id
// is read by the own id: a plugin's model (pluginModel, so its APIs,
// tiers and prices), Claude Code's run on a Claude account (the
// gateway's serveClaudeSubscription), Code Assist's and Antigravity's
// sent id (codeAssistBody, SentNameOnIn), and an account's own list
// (Account.Lists in the gateway's fallback, AccountServes).

// localIDs are the ids the user lists a provider's models by: from the
// provider's own id to the one the user gave, and back.
type localIDs struct {
	local map[string]string // own id → the id it is listed by
	own   map[string]string // the id it is listed by → own id
}

// modelIDsOf are the ids the user gave the provider pid's models.
func modelIDsOf(pid string) localIDs {
	return idsIn(settings.SharedModelIDs(), pid)
}

func idsIn(all map[string]string, pid string) localIDs {
	var l localIDs
	if len(all) == 0 {
		return l
	}
	for k, own := range all {
		model, ok := strings.CutPrefix(k, pid+"/")
		own = strings.TrimSpace(own)
		if !ok || model == "" || own == "" || own == model {
			continue
		}
		if l.local == nil {
			l.local, l.own = map[string]string{}, map[string]string{}
		}
		// two ids given one model (a hand-edited file): the first, in
		// order, is its
		if had, ok := l.local[own]; ok && had < model {
			continue
		}
		if had, ok := l.local[own]; ok {
			delete(l.own, had)
		}
		l.local[own], l.own[model] = model, own
	}
	return l
}

func (l localIDs) none() bool { return len(l.own) == 0 }

// ownOf is the provider's own id of the model listed as model.
func (l localIDs) ownOf(model string) string {
	if own, ok := l.own[model]; ok {
		return own
	}
	return model
}

// list is ms with each model given an id listed by the id the user gave
// it, and a model of the provider's own id one of those is given left
// out while the model given it is listed (the one id is one model).
func (l localIDs) list(ms []catalog.Model) []catalog.Model {
	if l.none() || len(ms) == 0 {
		return ms
	}
	has := make(map[string]bool, len(ms))
	for _, m := range ms {
		has[m.ID] = true
	}
	out := make([]catalog.Model, 0, len(ms))
	seen := make(map[string]bool, len(ms))
	for _, m := range ms {
		if local, ok := l.local[m.ID]; ok {
			if m.Name == "" || m.Name == m.ID {
				m.Name = local
			}
			m.ID = local
			// levels borrowed for the vendor's id are borrowed for the
			// one it is listed by too, and for the model the user said
			// that one is the same as: a plugin's qfmodel listed as
			// claude-opus-5.5 has its levels (axelburks on Discord)
			if m.EffortsBorrowed {
				if e := borrowedEfforts(local); len(e) > 0 {
					m.Efforts = e
				}
			}
		} else if own, ok := l.own[m.ID]; ok && has[own] {
			continue // listed by the model given its id
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

// ids is set (of the provider's own ids) with each given another id by it.
func (l localIDs) ids(set map[string]bool) map[string]bool {
	if l.none() {
		return set
	}
	out := make(map[string]bool, len(set))
	for id := range set {
		if _, ok := l.local[id]; ok {
			continue
		}
		if own, ok := l.own[id]; ok && set[own] {
			continue
		}
		out[id] = true
	}
	for own, local := range l.local {
		if set[own] {
			out[local] = true
		}
	}
	return out
}

// takesModelIDs is whether the user can list p's models by ids of their
// own: every provider with a chat list of its own, a key's, an account's
// or a plugin's. A provider that only decides (DecideOnly) lists Jev's
// models and nothing else.
func (p Provider) takesModelIDs() bool { return !p.DecideOnly() }

// TakesModelIDs is whether p's models can be listed by ids the user gives
// them (SetModelID).
func (p Provider) TakesModelIDs() bool { return p.takesModelIDs() }

// ids are the ids the user gave p's models, none for a provider that
// takes none.
func (p Provider) ids() localIDs {
	if !p.takesModelIDs() {
		return localIDs{}
	}
	return modelIDsOf(p.ID)
}

// OwnModelID is the provider's own id of its model listed as model: the
// id the vendor is asked for it by, the same as model unless the user
// lists it by another (SetModelID).
func (p Provider) OwnModelID(model string) string { return p.ids().ownOf(model) }

// ListedModelID is the id p lists its model of its own id own by: the one
// the user gave it, else own.
func (p Provider) ListedModelID(own string) string {
	if local, ok := p.ids().local[own]; ok {
		return local
	}
	return own
}

// ModelIDs are the ids the user lists p's models by, from each one to the
// provider's own id of it.
func (p Provider) ModelIDs() map[string]string {
	out := map[string]string{}
	for local, own := range p.ids().own {
		out[local] = own
	}
	return out
}

// rawIDs are the ids of the models the provider itself lists for chat,
// before any is listed by another id, and those the user picked that its
// list doesn't have (typed in, or not fetched yet).
func (p Provider) rawIDs() map[string]bool {
	out := map[string]bool{}
	for _, m := range p.available() {
		out[m.ID] = true
	}
	for _, id := range p.Models {
		if _, given := p.ids().own[id]; !given {
			out[id] = true
		}
	}
	return out
}

// checkModelID is why id can't be one a model is listed by, if it can't.
func checkModelID(id string) error {
	switch {
	case id == "*" || strings.Contains(id, "*"):
		return errors.New(`a model's id can't have "*" in it`)
	case strings.HasPrefix(id, "-"):
		return fmt.Errorf("a model's id can't start with -, not %q", id)
	case strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return fmt.Errorf("a model's id has no spaces in it, not %q", id)
	case IsGroupRef(id):
		return errors.New("that is a routing group's id, not a model's")
	}
	return nil
}

// SetModelID lists a provider's model, spelled "provider/model" by the id
// it is listed by now or by the provider's own, by id instead, and says
// which of its models had the id before (replaced) and whether anything
// changed: what
// magpie lists, picks, groups, routes and records it by, while the
// provider is still asked for it by its own id. A blank id, or the
// provider's own, lists it by its own again.
//
// One id is one model of a provider, so an id already given another of its
// models is that model's no more, which it answers (the latest one given
// it wins): that model is listed by its own id again. A model of the
// provider's own id is not listed while another is listed by it, and an
// agent's or a group's ref to the id reaches the model given it.
//
// What the user picked, grouped, set and said of the model goes with it to
// the new id: its picks, a key's models, its context, the routing groups'
// refs and fallbacks, the pickers' lists and every per-model setting — but
// a setting the user already has under the new id, which stays theirs.
// Agents' own files are not rewritten: a "provider/<own id>" they still
// name resolves, and is asked for under that id (Resolve).
func SetModelID(ref, id string) (replaced string, changed bool, err error) {
	replaced, changed, err = setModelID(ref, id)
	return replaced, changed, touchedIf(changed, err)
}

func setModelID(ref, id string) (string, bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return "", false, err
	}
	if !p.takesModelIDs() {
		return "", false, fmt.Errorf("%s only decides for the routing groups: it has no models of its own to list by another id", p.Name)
	}
	id = strings.TrimSpace(id)
	if id != "" {
		if err := checkModelID(id); err != nil {
			return "", false, err
		}
	}
	s := settings.Load()
	ids := idsIn(s.ModelIDs, p.ID)
	raw := p.rawIDs()
	// model is the id it is listed by now, or the provider's own
	cur, own := model, ids.ownOf(model)
	if own == model {
		if local, ok := ids.local[model]; ok {
			cur = local
		} else if !raw[model] {
			return "", false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
	if id == "" {
		id = own
	}
	if id == cur {
		return "", false, nil
	}
	var replaced string
	if had, ok := ids.own[id]; ok && had != own {
		// the latest model given the id has it
		replaced = had
	}
	if s.ModelIDs == nil {
		s.ModelIDs = map[string]string{}
	}
	delete(s.ModelIDs, p.ID+"/"+cur)
	for k, v := range s.ModelIDs {
		// any other id the model went by (a hand-edited file)
		if strings.HasPrefix(k, p.ID+"/") && strings.TrimSpace(v) == own {
			delete(s.ModelIDs, k)
		}
	}
	if id != own {
		s.ModelIDs[p.ID+"/"+id] = own
	}
	if len(s.ModelIDs) == 0 {
		s.ModelIDs = nil
	}
	if err := moveModelRefs(p.ID, cur, id, p.Account != nil); err != nil {
		return "", false, err
	}
	s.MoveModel(p.ID+"/"+cur, p.ID+"/"+id)
	moveModelLists(&s, p.ID+"/"+cur, p.ID+"/"+id)
	if err := settings.Save(s); err != nil {
		return "", false, err
	}
	if ModelMoved != nil {
		if err := ModelMoved(p.ID+"/"+cur, p.ID+"/"+id); err != nil {
			return replaced, true, err
		}
	}
	return replaced, true, nil
}

// ModelMoved, when set, gives what another package keeps of a provider's
// model ("<provider>/<model>") the id it is listed by now (SetModelID): the
// gateway keys' models (access), which this package can't reach.
var ModelMoved func(from, to string) error

// moveModelRefs gives every ref the providers file keeps to the provider
// pid's model from as to: its picks, those set aside and listed when
// picked, a key's models and its context, the fallbacks and the routing
// groups' refs. A signed-in account's or a plugin's provider is kept
// there only once the user has set something on it (withPicks): with
// none kept (account), only the other providers' refs to it move.
func moveModelRefs(pid, from, to string, account bool) error {
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(q Provider) bool { return q.ID == pid })
	if i < 0 && !account {
		return fmt.Errorf("no provider %q", pid)
	}
	moved := func(ids []string) []string {
		if !slices.Contains(ids, from) {
			return ids
		}
		out := make([]string, 0, len(ids))
		for _, m := range ids {
			if m == from {
				m = to
			}
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
		return out
	}
	if i >= 0 {
		q := &f.Providers[i]
		q.Models, q.PickedFrom, q.Delisted = moved(q.Models), moved(q.PickedFrom), moved(q.Delisted)
		for k, ms := range q.AccountModels {
			q.AccountModels[k] = moved(ms)
		}
		if n, ok := q.Contexts[from]; ok {
			if _, has := q.Contexts[to]; !has {
				q.Contexts[to] = n
			}
			delete(q.Contexts, from)
		}
	}
	ref := func(m string) string {
		pre, rest := "", m
		if r, ok := strings.CutPrefix(m, "magpie/"); ok {
			pre, rest = "magpie/", r
		}
		if rest == pid+"/"+from {
			return pre + pid + "/" + to
		}
		return m
	}
	mapModelRefs(&f, ref)
	return store(f)
}

// moveModelLists gives the pickers' lists of the settings (hidden, picked
// for an agent, ordered, fast, Ultrafast) the model from as to.
func moveModelLists(s *settings.Settings, from, to string) {
	for _, m := range []map[string][]string{s.HiddenModels, s.PickedModels, s.OrderedModels, s.FastPicks, s.UltraPicks} {
		for k, ids := range m {
			if !slices.Contains(ids, from) {
				continue
			}
			out := make([]string, 0, len(ids))
			for _, id := range ids {
				if id == from {
					id = to
				}
				if !slices.Contains(out, id) {
					out = append(out, id)
				}
			}
			m[k] = out
		}
	}
}
