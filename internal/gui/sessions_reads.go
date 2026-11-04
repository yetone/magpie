package gui

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// Which agents' session history magpie may read (RustCats on X): the
// Sessions page asks once on a fresh install, and Settings changes it per
// agent later. sessions.Reads is the gate every reader asks.

type readAgentJSON struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	On    bool   `json:"on"`
}

type readsJSON struct {
	// Asked: the user said which agents may be read (or the install read
	// them all before there was a choice)
	Asked bool `json:"asked"`
	// All: every agent, those found later too, unless one is turned off
	All bool `json:"all"`
	// Found: the agents with session history on this computer, and those
	// the user named
	Found []readAgentJSON `json:"found"`
	// Gateway: gateway mode, which reads no agent's files here whatever
	// the choice (ownSessionsOff), so nothing is asked and none is found
	Gateway bool `json:"gateway,omitempty"`
}

func readsNow() readsJSON {
	r := sessions.SettleReads()
	if s := settings.Shared().SessionReads; s != nil {
		r = s
	}
	out := readsJSON{Found: []readAgentJSON{}, Gateway: ownSessionsOff()}
	if r != nil {
		out.Asked, out.All = r.Asked, r.All
	}
	agents := sessions.Found()
	if r != nil {
		for a := range r.Agents {
			if !slices.Contains(agents, a) {
				agents = append(agents, a)
			}
		}
	}
	looks := agentLooks()
	for _, a := range agents {
		j := readAgentJSON{Agent: a, Name: a, Icon: "generic", On: r.Reads(a)}
		if l := looks[a]; l != nil {
			j.Name, j.Icon = l.SharedName(), l.Icon
		}
		out.Found = append(out.Found, j)
	}
	sort.SliceStable(out.Found, func(i, j int) bool { return out.Found[i].Name < out.Found[j].Name })
	return out
}

func sessionReadsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/reads", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, readsNow())
	})
	// {all, agents: {id: on}}: all, when sent, is whether an agent not
	// named is read (allow all sends true, allow selected false), and each
	// agent named is read or not; either way the choice is made (asked).
	mux.HandleFunc("POST /api/sessions/reads", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			All    *bool           `json:"all"`
			Agents map[string]bool `json:"agents"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 16384)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := sessions.SetReads(in.All, in.Agents); err != nil {
			fail(rw, err)
			return
		}
		forgetStats()
		writeJSON(rw, readsNow())
	})
}
