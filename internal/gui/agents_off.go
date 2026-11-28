package gui

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// The models turned off for agents on the Gateway page's model list
// (provider.AgentsOff, 辰林鸭~ on Discord): left out of every agent's
// lists at once, still served through a routing group or a request that
// names them.

// agentsOffJSON is what the Gateway page's list draws its switches from.
type agentsOffJSON struct {
	// Off are the models turned off for agents, by catalog id
	Off []string `json:"off"`
	// Keep is, by catalog id, the agent set to a model, which can't be
	// turned off while it is: its list would lose the model it runs on,
	// as each agent's own list keeps it ("{agent} is set to it, so it
	// stays")
	Keep map[string]string `json:"keep,omitempty"`
}

// agentsOffState is the set and who keeps what, for the page.
func agentsOffState() agentsOffJSON {
	out := agentsOffJSON{Off: []string{}, Keep: agentsKeep()}
	for id := range provider.AgentsOff() {
		out.Off = append(out.Off, id)
	}
	slices.Sort(out.Off)
	return out
}

// agentsKeep is each provider's model an agent found here is set to, with
// that agent's name: the first one, as agents come.
func agentsKeep() map[string]string {
	var es []provider.Entry
	for _, e := range provider.Served() {
		if e.Group == "" {
			es = append(es, e)
		}
	}
	out := map[string]string{}
	for _, a := range agent.Detected() {
		vals := a.Values()
		for _, e := range es {
			if _, ok := out[e.ID]; !ok && usedBy(a, vals, e) {
				out[e.ID] = a.Name
			}
		}
	}
	return out
}

func agentsOffAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents-off", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		writeJSON(rw, agentsOffState())
	})
	// ids are turned off for agents, or back on (off false); one an agent
	// is set to is passed by, as the agent's own list keeps it
	mux.HandleFunc("POST /api/agents-off", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string
			Off bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ids := in.IDs
		if in.Off {
			keep := agentsKeep()
			ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return keep[id] != "" })
		}
		if err := provider.SetAgentsOff(ids, in.Off); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, agentsOffState())
	})
}
