package gui

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// The Sessions page: every session of an agent, by folder, to pick up again
// or delete. A delete moves the session's files into magpie's trash, and a
// restore moves them back. Only the reader's Delete forever or Empty trash
// erases what is in the trash; nothing is erased by itself.

type manageAgentJSON struct {
	sessions.AgentCount
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// managedJSON is a session as the Sessions page lists it: its files, and
// what it spent (tokens, cost and models, which the Usage page's list shows
// too, #752) with where magpie's gateway sent its calls
type managedJSON struct {
	sessions.Managed
	Via []usage.Via `json:"via,omitempty"`
}

type trashedJSON struct {
	sessions.Trashed
	Name string `json:"name"`
	Icon string `json:"icon"`
}

func agentLooks() map[string]*agent.Agent {
	out := map[string]*agent.Agent{}
	for _, a := range agent.Clients() {
		out[a.ID] = a
	}
	return out
}

func trashJSON(looks map[string]*agent.Agent) []trashedJSON {
	out := []trashedJSON{}
	for _, t := range sessions.Trash() {
		j := trashedJSON{Trashed: t, Name: t.Agent, Icon: "generic"}
		if a := looks[t.Agent]; a != nil {
			j.Name, j.Icon = a.Name, a.Icon
		}
		j.Cwd = tilde(j.Cwd)
		for i := range j.Items {
			j.Items[i].From = tilde(j.Items[i].From)
		}
		out = append(out, j)
	}
	return out
}

func sessionManageRoutes(mux *http.ServeMux, w Windows) {
	// manage is the agents with sessions, and every session of ?agent=
	// (the one with the most when none is named), and the trash.
	mux.HandleFunc("GET /api/sessions/manage", func(rw http.ResponseWriter, r *http.Request) {
		looks := agentLooks()
		out := struct {
			Agents   []manageAgentJSON `json:"agents"`
			Agent    string            `json:"agent"`
			Sessions []managedJSON     `json:"sessions"`
			Terminal bool              `json:"terminal"`
			Trash    []trashedJSON     `json:"trash"`
			TrashDir string            `json:"trashDir"`
		}{Agents: []manageAgentJSON{}, Sessions: []managedJSON{}, Terminal: runtime.GOOS == "darwin" && !isWeb(w),
			Trash: trashJSON(looks), TrashDir: tilde(sessions.TrashDir())}
		want := r.URL.Query().Get("agent")
		for _, a := range sessions.Agents() {
			j := manageAgentJSON{AgentCount: a, Name: a.Agent, Icon: "generic"}
			if l := looks[a.Agent]; l != nil {
				j.Name, j.Icon = l.Name, l.Icon
			}
			out.Agents = append(out.Agents, j)
			if a.Agent == want {
				out.Agent = want
			}
		}
		if out.Agent == "" && len(out.Agents) > 0 {
			out.Agent = out.Agents[0].Agent
		}
		if out.Agent != "" {
			list := sessions.ListAgent(out.Agent)
			since := time.Now()
			for _, s := range list {
				if !s.Start.IsZero() && s.Start.Before(since) {
					since = s.Start
				}
			}
			vias := usage.Vias(since.Add(-time.Minute))
			for _, s := range list {
				s.Path = tilde(s.Path)
				out.Sessions = append(out.Sessions, managedJSON{Managed: s, Via: vias[s.Agent+"|"+s.ID]})
			}
		}
		writeJSON(rw, out)
	})
	// delete moves the sessions named to magpie's trash, one by one; one
	// still being written is left, and said so.
	mux.HandleFunc("POST /api/sessions/delete", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string   `json:"agent"`
			IDs   []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if !sessions.Deletable(in.Agent) {
			fail(rw, errors.New("magpie can't delete this agent's sessions"))
			return
		}
		type refused struct {
			ID     string `json:"id"`
			Error  string `json:"error"`
			Active bool   `json:"active"`
		}
		out := struct {
			Deleted []string  `json:"deleted"`
			Refused []refused `json:"refused"`
		}{Deleted: []string{}, Refused: []refused{}}
		for _, id := range in.IDs {
			if _, err := sessions.Delete(in.Agent, id); err != nil {
				out.Refused = append(out.Refused, refused{ID: id, Error: err.Error(), Active: errors.Is(err, sessions.ErrActive)})
				continue
			}
			out.Deleted = append(out.Deleted, id)
		}
		forgetStats()
		writeJSON(rw, out)
	})
	mux.HandleFunc("POST /api/sessions/restore", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		t, err := sessions.Restore(in.Key)
		if err != nil {
			fail(rw, err)
			return
		}
		forgetStats()
		writeJSON(rw, map[string]string{"agent": t.Agent, "id": t.ID, "title": cmp.Or(t.Title, t.ID)})
	})
	// purge erases trashed sessions for good: the keys named, or every one
	// the trash lists with all. Each key is checked by sessions.Purge, which
	// removes only a session's folder in magpie's trash.
	mux.HandleFunc("POST /api/sessions/purge", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Keys []string `json:"keys"`
			All  bool     `json:"all"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.All {
			in.Keys = nil
			for _, t := range sessions.Trash() {
				in.Keys = append(in.Keys, t.Key)
			}
		}
		type refused struct {
			Key   string `json:"key"`
			Error string `json:"error"`
		}
		out := struct {
			Purged  []string  `json:"purged"`
			Refused []refused `json:"refused"`
		}{Purged: []string{}, Refused: []refused{}}
		for _, k := range in.Keys {
			if err := sessions.Purge(k); err != nil {
				out.Refused = append(out.Refused, refused{Key: k, Error: err.Error()})
				continue
			}
			out.Purged = append(out.Purged, k)
		}
		writeJSON(rw, out)
	})
}

// forgetStats has the Usage page's session stats read again: a session
// deleted or restored changes them.
func forgetStats() {
	statsMemo.Lock()
	statsMemo.m = nil
	statsMemo.Unlock()
}
