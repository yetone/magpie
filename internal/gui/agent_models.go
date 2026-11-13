package gui

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// Which of the catalog an agent's lists show, picked one model at a time
// on the Agents page (settings.HiddenModels, or settings.PickedModels for an
// agent shown only the models picked, #1337): the line under an agent's
// name counts them, and opens the list to pick in.

// modelCountJSON is how many models an agent's lists show, of those its
// visibility gives it.
type modelCountJSON struct {
	Shown  int `json:"shown"`
	Listed int `json:"listed"`
	// By is the models shown by whose they are, a connected agent's
	// expanded row's chips (OpenAI 6 · OpenRouter 24)
	By []modelGroupJSON `json:"by,omitempty"`
	// Names are the first few models shown by name, for a row that says
	// which it lists rather than how many (Claude Desktop's)
	Names []string `json:"names,omitempty"`
}

// countNames is how many models' names modelCount gives.
const countNames = 3

// modelGroupJSON is how many of the models shown one provider (or the
// routing groups) gives.
type modelGroupJSON struct {
	Name  string   `json:"name"`
	Icon  string   `json:"icon,omitempty"`
	Icons []string `json:"icons,omitempty"`
	N     int      `json:"n"`
}

// agentModelJSON is a model an agent may be shown, as the list draws it.
type agentModelJSON struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Group string   `json:"group"` // its provider's name, or "Routing groups"
	Icon  string   `json:"icon,omitempty"`
	Icons []string `json:"icons,omitempty"`
	// Logo is its maker's: known by the model's family, or its provider's
	// when that is the maker; "" when neither says, and for a group, which
	// has Icons
	Logo    string `json:"logo,omitempty"`
	Context int    `json:"context,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	// InUse: the agent is set to it, so it can't be taken out
	InUse bool `json:"inUse,omitempty"`
	// Own: one of Codex's own, a ChatGPT account's, which Codex lists
	// ahead of magpie's until the user puts them in an order (#855)
	Own bool `json:"own,omitempty"`
	// Kept: the agent's visibility (magpie visible) keeps it out of its
	// lists, so it is off; ticked, the list sends it as widen, and its
	// provider or group is listed (whysugarsweet on X)
	Kept bool `json:"kept,omitempty"`
}

// takesCatalog reports whether an agent picks among magpie's catalog: some
// option of a field of its is a catalog entry.
func takesCatalog(fields []fieldJSON) bool {
	for _, f := range fields {
		for _, o := range f.Options {
			if o.Ref != "" {
				return true
			}
		}
	}
	return false
}

// agentFields are an agent's pickers as the Agents page draws them.
func agentFields(a *agent.Agent, vals map[string]string) []fieldJSON {
	out := []fieldJSON{}
	for _, f := range a.Fields {
		opts := a.Picks(f, vals)
		if opts == nil {
			opts = []agent.Option{}
		}
		out = append(out, fieldJSON{Key: f.Key, Label: f.Label, Value: vals[f.Key], Options: opts})
	}
	return out
}

// agentModelCount is the line under an agent's name, nil for an agent that
// doesn't pick among the catalog nor has its menu from the gateway's list
// (agent.ListsModels). Its pickers list only the models shown, so
// with every one taken out they have no catalog entry left, yet the line is
// the one way to put them back (#356): it stays while any is hidden.
func agentModelCount(a *agent.Agent, fields []fieldJSON) *modelCountJSON {
	id := a.ListsFor()
	if takesCatalog(fields) || a.ListsModels {
		return modelCount(id)
	}
	if _, only := provider.PickedModels(id); !only && len(provider.HiddenModels(id)) == 0 {
		return nil
	}
	if c := modelCount(id); c.Shown < c.Listed {
		return c
	}
	return nil
}

// modelCounts keeps each agent's count while provider.StampNow stays the
// same, for a minute at most, as the agents' model lists are kept
// (agent's magpieModelsFor): every poll of the Agents page counted each
// agent's list anew, ListedFor built whole for a number (0.27 GB of the
// 2.6 GB a real config allocated over 20 polls).
var modelCounts struct {
	sync.Mutex
	m map[string]keptCount
}

type keptCount struct {
	c     modelCountJSON
	stamp provider.Stamp
	at    time.Time
}

// modelCountFor is how long a count is kept while the stamp says nothing
// changed: what it can't see (another app rewriting an account's file, a
// model retired by its date) reaches the count within it.
const modelCountFor = time.Minute

func modelCount(id string) *modelCountJSON {
	modelCounts.Lock()
	k, ok := modelCounts.m[id]
	modelCounts.Unlock()
	// the stamp is read before the count is made: a change while it is
	// moves it past the one kept, and the next call counts again
	stamp := provider.StampNow()
	if !ok || k.stamp != stamp || time.Since(k.at) >= modelCountFor {
		k = keptCount{countModels(id), stamp, time.Now()}
		modelCounts.Lock()
		if modelCounts.m == nil {
			modelCounts.m = map[string]keptCount{}
		}
		modelCounts.m[id] = k
		modelCounts.Unlock()
	}
	c := k.c
	c.By, c.Names = slices.Clone(c.By), slices.Clone(c.Names)
	return &c
}

func countModels(id string) modelCountJSON {
	listed, _ := provider.ListedFor(id)
	off := provider.ModelOff(id)
	c := &modelCountJSON{Listed: len(listed)}
	at := map[string]int{}
	for _, e := range listed {
		if off(e.ID) {
			continue
		}
		c.Shown++
		if len(c.Names) < countNames {
			c.Names = append(c.Names, cmp.Or(e.Name, e.Model, e.ID))
		}
		g := modelGroupJSON{Name: e.Provider.Name, Icon: e.Provider.Icon}
		if e.Group != "" {
			g = modelGroupJSON{Name: agent.RoutingGroups, Icons: e.Icons}
		}
		i, ok := at[g.Name]
		if !ok {
			i = len(c.By)
			at[g.Name] = i
			c.By = append(c.By, g)
		}
		c.By[i].N++
	}
	return *c
}

// usedBy reports whether one of the agent's values is the entry: its id,
// with a prefix the agent's files give it (magpie/…), or, for the agent's
// own account, the vendor's model id alone.
func usedBy(a *agent.Agent, vals map[string]string, e provider.Entry) bool {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if v == e.ID || strings.HasSuffix(v, "/"+e.ID) {
			return true
		}
		if acc := e.Provider.Account; e.Group == "" && acc != nil && acc.Agent == a.ListsFor() && v == e.Model {
			return true
		}
	}
	return false
}

func agentModelList(a *agent.Agent) []agentModelJSON {
	id := a.ListsFor()
	listed, kept := provider.ListedFor(id)
	off := provider.ModelOff(id)
	vals := a.Values()
	out := []agentModelJSON{}
	// the ones its visibility keeps after the rest, off, so a provider
	// added after the agent was narrowed is still in its list to tick
	n := len(listed)
	for i, e := range append(listed, kept...) {
		m := agentModelJSON{ID: e.ID, Name: e.Name, Group: e.Provider.Name, Icon: e.Provider.Icon, Context: e.Context,
			Hidden: off(e.ID), InUse: usedBy(a, vals, e), Own: id == "codex" && provider.CodexOwn(e)}
		if i >= n {
			m.Hidden, m.Kept = true, true
		}
		if m.Name == "" {
			m.Name = e.Model
		}
		m.Logo = agent.ModelIcon(e.Model)
		if e.Group != "" {
			// a group shows its providers' icons, as it does everywhere else
			m.Group, m.Icons, m.Logo = agent.RoutingGroups, e.Icons, ""
		} else if m.Logo == "" {
			if p := provider.Preset(e.Provider.Preset); p != nil && p.Kind == provider.KindVendor {
				m.Logo = e.Provider.Icon
			}
		}
		out = append(out, m)
	}
	return out
}

func agentModelsAPI(mux *http.ServeMux) {
	// a model the agent picks sent in its vendor's fast mode, or not (#954):
	// for is the option's fastFor, the agent whose requests it goes on. Ultra
	// sends it as Ultrafast where the account offers that (#1954)
	mux.HandleFunc("POST /api/agent-fast", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			For, Ref    string
			Fast, Ultra bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		tier := ""
		switch {
		case in.Fast && in.Ultra:
			tier = "ultrafast"
		case in.Fast:
			tier = "priority"
		}
		if err := provider.SetFastTier(in.For, in.Ref, tier); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"fast": provider.IsFastPick(in.For, in.Ref), "ultra": provider.IsUltraPick(in.For, in.Ref)})
	})
	mux.HandleFunc("GET /api/agent-models/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		_, only := provider.PickedModels(a.ListsFor())
		// own: its pickers offer models of the agent's own beside
		// magpie's, which ownHidden leaves out (#1642)
		writeJSON(rw, map[string]any{"models": agentModelList(a), "ordered": len(provider.ModelOrder(a.ListsFor())) > 0, "only": only,
			"own": a.OwnModels(a.Values()), "ownHidden": provider.OwnModelsHidden(a.ListsFor())})
	})
	// hidden is every entry to take out of the agent's lists; the others
	// are shown, and one the agent is set to is kept in whatever is asked
	//
	// order, instead, is the order the agent's list puts them in (#855):
	// the ones named first, as named; none puts back magpie's own. Every
	// agent's (#1052): its list, the gateway's /models and the files magpie
	// writes for it all come from provider.CatalogFor, which keeps it
	//
	// only switches the agent between being shown new models and being
	// shown only the models picked for it (#1337), the ones shown now
	// staying as they are; shown, for an agent shown only its picks, is
	// every entry its lists show, the others not, a model that comes later
	// among them
	//
	// ownHidden switches the agent's pickers between offering its own
	// models beside magpie's and offering magpie's alone (#1642)
	mux.HandleFunc("POST /api/agent-models/{id}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Hidden []string
			Shown  *[]string
			Order  *[]string
			Only   *bool
			// OwnHidden, sent as ownHidden
			OwnHidden *bool
			// Widen: models its visibility keeps that were ticked, whose
			// providers or groups it is shown from now on
			Widen []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		if in.OwnHidden != nil {
			if err := provider.SetOwnModelsHidden(a.ListsFor(), *in.OwnHidden); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"ownHidden": provider.OwnModelsHidden(a.ListsFor())})
			return
		}
		if in.Order != nil {
			if err := provider.SetModelOrder(a.ListsFor(), *in.Order); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"models": agentModelList(a), "ordered": len(provider.ModelOrder(a.ListsFor())) > 0})
			return
		}
		id := a.ListsFor()
		answer := func() {
			_, only := provider.PickedModels(id)
			writeJSON(rw, map[string]any{"models": agentModelList(a), "count": modelCount(id), "only": only})
		}
		if in.Only != nil {
			if err := provider.SetOnlyPicked(id, *in.Only); err != nil {
				fail(rw, err)
				return
			}
			answer()
			return
		}
		if err := provider.Widen(id, in.Widen); err != nil {
			fail(rw, err)
			return
		}
		used := map[string]bool{}
		// what the visibility still keeps is neither picked nor taken out:
		// it isn't listed
		keptID := map[string]bool{}
		listed := slices.DeleteFunc(agentModelList(a), func(m agentModelJSON) bool {
			keptID[m.ID] = m.Kept
			return m.Kept
		})
		for _, m := range listed {
			used[m.ID] = m.InUse
		}
		// one the agent is set to is kept in whatever is asked, so it is
		// among the picks too
		if _, only := provider.PickedModels(id); only {
			shown := map[string]bool{}
			if in.Shown != nil {
				for _, s := range *in.Shown {
					shown[s] = true
				}
			} else {
				// a list still sending what it hides, as it did before
				// there were picks (one opened before the switch, in
				// another window): every other listed one is shown
				for _, m := range listed {
					shown[m.ID] = !slices.Contains(in.Hidden, m.ID)
				}
			}
			var picks []string
			for _, m := range listed {
				if shown[m.ID] || used[m.ID] {
					picks = append(picks, m.ID)
				}
			}
			if err := provider.SetPickedModels(id, picks); err != nil {
				fail(rw, err)
				return
			}
			answer()
			return
		}
		var hidden []string
		if in.Shown != nil {
			// a list opened while the agent was shown only its picks, sent
			// after it was switched back: every other listed one is hidden
			for _, m := range listed {
				if !slices.Contains(*in.Shown, m.ID) {
					hidden = append(hidden, m.ID)
				}
			}
		} else {
			hidden = in.Hidden
		}
		hidden = slices.DeleteFunc(slices.Clone(hidden), func(s string) bool { return used[s] || keptID[s] })
		if err := provider.SetHiddenModels(id, hidden); err != nil {
			fail(rw, err)
			return
		}
		answer()
	})
}
