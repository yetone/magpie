package gui

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// Which of the catalog an agent's lists show, picked one model at a time
// on the Agents page (settings.HiddenModels): the line under an agent's
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
		opts := f.Options(vals)
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
	if len(provider.HiddenModels(id)) == 0 {
		return nil
	}
	if c := modelCount(id); c.Shown < c.Listed {
		return c
	}
	return nil
}

func modelCount(id string) *modelCountJSON {
	listed, _ := provider.ListedFor(id)
	off := provider.HiddenModels(id)
	c := &modelCountJSON{Listed: len(listed)}
	at := map[string]int{}
	for _, e := range listed {
		if off[e.ID] {
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
	return c
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
	listed, _ := provider.ListedFor(id)
	off := provider.HiddenModels(id)
	vals := a.Values()
	out := []agentModelJSON{}
	for _, e := range listed {
		m := agentModelJSON{ID: e.ID, Name: e.Name, Group: e.Provider.Name, Icon: e.Provider.Icon, Context: e.Context,
			Hidden: off[e.ID], InUse: usedBy(a, vals, e), Own: id == "codex" && provider.CodexOwn(e)}
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
	// for is the option's fastFor, the agent whose requests it goes on
	mux.HandleFunc("POST /api/agent-fast", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			For, Ref string
			Fast     bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetFastPick(in.For, in.Ref, in.Fast); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"fast": provider.IsFastPick(in.For, in.Ref)})
	})
	mux.HandleFunc("GET /api/agent-models/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"models": agentModelList(a), "ordered": len(provider.ModelOrder(a.ListsFor())) > 0})
	})
	// hidden is every entry to take out of the agent's lists; the others
	// are shown, and one the agent is set to is kept in whatever is asked
	//
	// order, instead, is the order the agent's list puts them in (#855):
	// the ones named first, as named; none puts back magpie's own. Every
	// agent's (#1052): its list, the gateway's /models and the files magpie
	// writes for it all come from provider.CatalogFor, which keeps it
	mux.HandleFunc("POST /api/agent-models/{id}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Hidden []string
			Order  *[]string
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
		if in.Order != nil {
			if err := provider.SetModelOrder(a.ListsFor(), *in.Order); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"models": agentModelList(a), "ordered": len(provider.ModelOrder(a.ListsFor())) > 0})
			return
		}
		used := map[string]bool{}
		for _, m := range agentModelList(a) {
			used[m.ID] = m.InUse
		}
		var hidden []string
		for _, id := range in.Hidden {
			if !used[id] {
				hidden = append(hidden, id)
			}
		}
		if err := provider.SetHiddenModels(a.ListsFor(), hidden); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"models": agentModelList(a), "count": modelCount(a.ListsFor())})
	})
}
