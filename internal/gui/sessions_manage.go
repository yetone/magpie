package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
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
	// Keys: the gateway's other "agent|id" keys for it than its own
	// (sessions.GatewayKeys), for the page to find its speed by
	Keys []string `json:"keys,omitempty"`
}

// otherKeys are s's gateway keys but its own agent|id.
func otherKeys(s sessions.Session) []string {
	k := sessions.GatewayKeys(s)
	if len(k) < 2 {
		return nil
	}
	return k[1:]
}

// listedGateway are the gateway sessions the session lists show: not those
// deleted here (sessions.DeleteGateway) and not used since.
func listedGateway(gs []usage.GatewaySession) []usage.GatewaySession {
	hidden := sessions.GatewayHidden()
	out := gs[:0:0]
	for _, g := range gs {
		if !hidden(g.Agent, g.ID, g.Last) {
			out = append(out, g)
		}
	}
	return out
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
			j.Name, j.Icon = a.SharedName(), a.Icon
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
	sessionReadsRoutes(mux)
	mux.HandleFunc("POST /api/sessions/relocate-claude", func(rw http.ResponseWriter, r *http.Request) {
		var in sessions.ClaudeRelocation
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 16384)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		out, err := sessions.RelocateClaudeProject(in)
		if err != nil {
			fail(rw, err)
			return
		}
		if out.Backup != "" {
			forgetStats()
		}
		writeJSON(rw, out)
	})
	mux.HandleFunc("POST /api/sessions/recording", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On    bool `json:"on"`
			Clear bool `json:"clear"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1024)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := sessions.SetGatewayRecording(in.On, in.Clear); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"recording": settings.Load().GatewayConversations})
	})
	// speeds: each session's output speed now (gateway.Speeds), for the
	// page's curves while it is open. Not under /api/sessions, which
	// holds the session files' warm-up for a page that reads them: this
	// is asked every second and reads none.
	mux.HandleFunc("GET /api/speeds", func(rw http.ResponseWriter, r *http.Request) {
		sp := []gateway.Speed{}
		if gw := served.Load(); gw != nil {
			sp = append(sp, gw.Speeds()...)
		}
		writeJSON(rw, sp)
	})
	// manage is the agents with sessions, and every session of ?agent=
	// (the one with the most when none is named), and the trash.
	mux.HandleFunc("GET /api/sessions/manage", func(rw http.ResponseWriter, r *http.Request) {
		// the Codex app's list forgets the threads deleted before it
		// was told of each delete, once a run
		sessions.TidyCodexCatalog()
		defer sessions.OneListing()()
		looks := agentLooks()
		out := struct {
			Agents   []manageAgentJSON `json:"agents"`
			Agent    string            `json:"agent"`
			Sessions []managedJSON     `json:"sessions"`
			Terminal bool              `json:"terminal"`
			Trash    []trashedJSON     `json:"trash"`
			TrashDir string            `json:"trashDir"`
			// Home: the home folder, which a folder's row says as ~
			Home      string `json:"home,omitempty"`
			Recording bool   `json:"recording"`
			// Recorded: some gateway conversation text is kept, to delete
			Recorded bool `json:"recorded"`
			// Reads: which agents' own sessions magpie may read; the page
			// asks first when nothing is decided (asked false)
			Reads readsJSON `json:"reads"`
		}{Reads: readsNow(), Agents: []manageAgentJSON{}, Sessions: []managedJSON{}, Terminal: runtime.GOOS == "darwin" && !isWeb(w),
			Trash: trashJSON(looks), TrashDir: tilde(sessions.TrashDir()), Recording: settings.Load().GatewayConversations,
			Recorded: sessions.HasGatewayConversations()}
		out.Home, _ = os.UserHomeDir()
		want := r.URL.Query().Get("agent")
		gateway := listedGateway(usage.GatewaySessions(time.Time{}, nil))
		// a gateway session its agent keeps too is counted once; only the
		// agents with gateway sessions, and the one shown, are read (#2776)
		of := map[string]bool{}
		for _, g := range gateway {
			of[g.Agent] = true
		}
		view := sessions.Manage(of, func(agents []sessions.AgentCount, native map[string]map[string]bool) string {
			counts := map[string]*manageAgentJSON{}
			for _, a := range agents {
				j := manageAgentJSON{AgentCount: a, Name: a.Agent, Icon: "generic"}
				if l := looks[a.Agent]; l != nil {
					j.Name, j.Icon = l.SharedName(), l.Icon
				}
				counts[a.Agent] = &j
			}
			for _, s := range gateway {
				if native[s.Agent][s.ID] {
					continue
				}
				j := counts[s.Agent]
				if j == nil {
					j = &manageAgentJSON{AgentCount: sessions.AgentCount{Agent: s.Agent}, Name: s.Agent, Icon: "generic"}
					if l := looks[s.Agent]; l != nil {
						j.Name, j.Icon = l.SharedName(), l.Icon
					}
					counts[s.Agent] = j
				}
				j.Count++
				// what magpie keeps of it can be deleted, whatever the agent
				j.Deletable = true
			}
			for _, j := range counts {
				out.Agents = append(out.Agents, *j)
			}
			sort.Slice(out.Agents, func(i, j int) bool {
				if out.Agents[i].Count != out.Agents[j].Count {
					return out.Agents[i].Count > out.Agents[j].Count
				}
				return out.Agents[i].Agent < out.Agents[j].Agent
			})
			if _, ok := counts[want]; ok {
				return want
			}
			if len(out.Agents) > 0 {
				return out.Agents[0].Agent
			}
			return ""
		})
		out.Agent = view.Agent
		if out.Agent != "" {
			list := view.Sessions
			var modelEffort map[string][]sessions.ModelEffort
			if out.Agent == "codex" {
				modelEffort = usage.CodexModelEffort(usage.All)
			}
			for i := range list {
				addCodexModelEffort(&list[i].Session, modelEffort)
			}
			native := map[string]bool{}
			for _, s := range list {
				native[s.ID] = true
			}
			since := time.Now()
			for _, s := range list {
				if !s.Start.IsZero() && s.Start.Before(since) {
					since = s.Start
				}
			}
			for _, g := range gateway {
				if g.Agent == out.Agent && !g.Start.IsZero() && g.Start.Before(since) {
					since = g.Start
				}
			}
			vias := usage.Vias(since.Add(-time.Minute))
			for _, s := range list {
				s.Path = tilde(s.Path)
				out.Sessions = append(out.Sessions, managedJSON{Managed: s, Via: viaOf(vias, s.Session), Keys: otherKeys(s.Session)})
			}
			sizes := sessions.GatewaySizes()
			for _, g := range gateway {
				if g.Agent != out.Agent || native[g.ID] {
					continue
				}
				s := gatewaySession(g)
				addCodexModelEffort(&s, modelEffort)
				// its size is the conversation text magpie recorded of it
				m := sessions.Managed{Session: s, Deletable: true, Size: sizes(s.Agent, s.ID)}
				out.Sessions = append(out.Sessions, managedJSON{Managed: m, Via: vias.Of(s.Agent + "|" + s.ID)})
			}
			sort.SliceStable(out.Sessions, func(i, j int) bool {
				return out.Sessions[i].Last.After(out.Sessions[j].Last)
			})
		}
		writeJSON(rw, out)
	})
	// search is the sessions of ?agent= whose conversation says ?q=, newest
	// first, each with the words round its first match (YOU on Discord). A
	// new query from the page cancels the request, and with it the search.
	mux.HandleFunc("GET /api/sessions/search", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(rw, searchSessions(r.Context(), q.Get("agent"), q.Get("q"), searchBudget))
	})
	// delete moves the sessions named to magpie's trash, one by one; one
	// still being written is left, and said so. One seen only through the
	// gateway, of any agent, has what magpie keeps of it moved there.
	mux.HandleFunc("POST /api/sessions/delete", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string   `json:"agent"`
			IDs   []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		// the gateway sessions as listed: those with no native session
		native := sessions.NativeIDs(map[string]bool{in.Agent: true})[in.Agent]
		listed := map[string]usage.GatewaySession{}
		for _, g := range listedGateway(usage.GatewaySessions(time.Time{}, nil)) {
			if g.Agent == in.Agent && !native[g.ID] {
				listed[g.ID] = g
			}
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
			var err error
			if g, ok := listed[id]; ok {
				_, err = sessions.DeleteGateway(g.Agent, g.ID, g.ID, g.Last)
			} else if !sessions.Deletable(in.Agent) {
				err = errors.New("magpie can't delete this agent's sessions")
			} else {
				_, err = sessions.Delete(in.Agent, id)
			}
			if err != nil {
				out.Refused = append(out.Refused, refused{ID: id, Error: err.Error(), Active: errors.Is(err, sessions.ErrActive)})
				continue
			}
			out.Deleted = append(out.Deleted, id)
		}
		forgetStats()
		writeJSON(rw, out)
	})
	// codex-provider moves Codex sessions to another provider, each to its
	// own (the one Codex uses now, or the one it had, to undo): Codex's
	// history lists only the sessions of the provider it uses (#887).
	mux.HandleFunc("POST /api/sessions/codex-provider", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Moves []struct {
				ID string `json:"id"`
				To string `json:"to"`
			} `json:"moves"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		type refused struct {
			ID     string `json:"id"`
			Error  string `json:"error"`
			Active bool   `json:"active"`
		}
		out := struct {
			Moved   []sessions.CodexMove `json:"moved"`
			Refused []refused            `json:"refused"`
		}{Moved: []sessions.CodexMove{}, Refused: []refused{}}
		for _, m := range in.Moves {
			mv, err := sessions.MoveCodexProvider(m.ID, m.To)
			if err != nil {
				out.Refused = append(out.Refused, refused{ID: m.ID, Error: err.Error(), Active: errors.Is(err, sessions.ErrActive)})
				continue
			}
			mv.Backup = tilde(mv.Backup)
			out.Moved = append(out.Moved, mv)
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

// searchHitMax is the most sessions a search lists, and searchBudget how
// long it reads before it says what it found so far.
const searchHitMax = 200

var searchBudget = 8 * time.Second

type searchHitJSON struct {
	ID string `json:"id"`
	sessions.Hit
}

type searchJSON struct {
	Hits []searchHitJSON `json:"hits"`
	// Searched is how many sessions were read, of Total that could be
	Searched int `json:"searched"`
	Total    int `json:"total"`
	// Cut: it stopped before reading them all, at searchHitMax hits or
	// out of time
	Cut bool `json:"cut,omitempty"`
	// Unread: the agent's own sessions can't be read here (a database),
	// only those magpie recorded through the gateway are searched
	Unread bool `json:"unread,omitempty"`
}

// searchSessions reads an agent's sessions, newest first, a few at once, and
// keeps those that say what was asked; every one is read only to its first
// match.
func searchSessions(ctx context.Context, agentID, query string, budget time.Duration) searchJSON {
	out := searchJSON{Hits: []searchHitJSON{}}
	needle := sessions.NeedleOf(query)
	if agentID == "" || utf8.RuneCountInString(needle) < 2 {
		return out
	}
	type job struct {
		s    sessions.Session
		last time.Time
	}
	var jobs []job
	native := map[string]bool{}
	if sessions.HasTranscript(agentID) {
		for _, m := range sessions.ListAgent(agentID) {
			// every id the gateway can know it by (a ZCode session's bare
			// id, #5754), so its record is not searched again below
			for _, k := range sessions.GatewayKeys(m.Session) {
				if a, id, ok := strings.Cut(k, "|"); ok && a == agentID {
					native[id] = true
				}
			}
			jobs = append(jobs, job{m.Session, m.Last})
		}
	} else {
		// what magpie recorded of them through the gateway is still read
		out.Unread = true
		for _, m := range sessions.ListAgent(agentID) {
			native[m.ID] = true
			jobs = append(jobs, job{sessions.Session{Gateway: true, Agent: m.Agent, ID: m.ID}, m.Last})
		}
	}
	for _, g := range listedGateway(usage.GatewaySessions(time.Time{}, nil)) {
		if g.Agent == agentID && !native[g.ID] {
			jobs = append(jobs, job{sessions.Session{Gateway: true, Agent: g.Agent, ID: g.ID}, g.Last})
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].last.After(jobs[j].last) })
	out.Total = len(jobs)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type found struct {
		i   int
		hit sessions.Hit
		ok  bool
	}
	work := make(chan int)
	results := make(chan found)
	var wg sync.WaitGroup
	for range min(4, max(1, runtime.NumCPU()/2)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				var f found
				f.i = i
				s := jobs[i].s
				if s.Gateway {
					f.hit, f.ok, _ = sessions.SearchGateway(ctx, s.Agent, s.ID, needle)
				} else {
					f.hit, f.ok, _ = sessions.SearchSession(ctx, s, needle)
				}
				select {
				case results <- f:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(work)
		for i := range jobs {
			select {
			case work <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	var hits []found
	for f := range results {
		if ctx.Err() != nil {
			break
		}
		out.Searched++
		if f.ok {
			hits = append(hits, f)
			if len(hits) >= searchHitMax {
				break
			}
		}
	}
	if out.Searched < out.Total {
		out.Cut = true
	}
	cancel()
	for range results {
		// what the readers were on ends with the cancel
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].i < hits[j].i })
	for _, f := range hits {
		out.Hits = append(out.Hits, searchHitJSON{ID: jobs[f.i].s.ID, Hit: f.hit})
	}
	return out
}
