package gui

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// sessionJSON is a session with what the UI needs to draw its agent.
type sessionJSON struct {
	sessions.Session
	Name string `json:"name"`
	Icon string `json:"icon"`
	// Via: the models the gateway sent its calls to, at the reasoning
	// each was asked for, when it went through magpie
	Via []usage.Via `json:"via,omitempty"`
}

type sessionsJSON struct {
	Sessions []sessionJSON `json:"sessions"`
	// Terminal is set where magpie can open Terminal on the session: the
	// Mac app, not a browser tab that may be on another computer.
	Terminal bool     `json:"terminal"`
	Dirs     []string `json:"dirs"` // where they were read from
}

func sessionRoutes(mux *http.ServeMux, w Windows) {
	warmSessions()
	mux.HandleFunc("GET /api/sessions", func(rw http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		// The page draws a session count taken from every session
		// (statsFor's Overview) beside this list, so the list is read as
		// long as the count: at sessions.Limit the page said "238 sessions"
		// over a list that stopped at 200, and the agents whose sessions
		// were oldest fell off it whole — Codex had 121 and the list showed
		// 7. Only a caller that asks for a number of its own gets one. The
		// page draws this many rows in pages of its own (renderSessions).
		if n <= 0 {
			n = sessions.All
		}
		agents := map[string]*agent.Agent{}
		for _, a := range agent.Clients() {
			agents[a.ID] = a
		}
		out := sessionsJSON{Sessions: []sessionJSON{}, Terminal: runtime.GOOS == "darwin" && !isWeb(w),
			Dirs: []string{}}
		for _, d := range sessions.Dirs() {
			out.Dirs = append(out.Dirs, tilde(d))
		}
		list := sessions.List(n)
		since := time.Now()
		for _, s := range list {
			if !s.Start.IsZero() && s.Start.Before(since) {
				since = s.Start
			}
		}
		vias := usage.Vias(since.Add(-time.Minute))
		for _, s := range list {
			j := sessionJSON{Session: s, Name: s.Agent, Icon: "generic", Via: vias[s.Agent+"|"+s.ID]}
			if a := agents[s.Agent]; a != nil {
				j.Name, j.Icon = a.Name, a.Icon
			}
			j.Name = wslName(j.Name, s)
			j.Path = tilde(j.Path)
			out.Sessions = append(out.Sessions, j)
		}
		writeJSON(rw, out)
	})
	// stats is every session's usage by day over the last ?days=N days
	// (every day when 0 or none), cut by agent, folder and model for the
	// page to filter; names are the agents' as the page shows them.
	mux.HandleFunc("GET /api/sessions/stats", func(rw http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("days"))
		st := statsFor(n)
		names := map[string]string{}
		for _, a := range agent.Clients() {
			names[a.ID] = a.Name
		}
		agents := map[string]string{}
		for _, d := range st.Days {
			for _, u := range d.Usage {
				agents[u.Agent] = cmp.Or(names[u.Agent], u.Agent)
			}
			for _, a := range d.Active {
				agents[a.Agent] = cmp.Or(names[a.Agent], a.Agent)
			}
		}
		writeJSON(rw, struct {
			sessions.Stats
			Agents map[string]string `json:"agents"`
		}{st, agents})
	})
	// overview sums up the range's sessions under the page's filters:
	// how many, what the middle one spent, how many each day, and the ones
	// that spent the most.
	mux.HandleFunc("GET /api/sessions/overview", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, _ := strconv.Atoi(q.Get("days"))
		writeJSON(rw, statsFor(n).Overview(q.Get("agent"), q.Get("model"), q.Get("cwd"), 8))
	})
	// progress is how far the reading of the session files has got, for
	// the page to show while its first read takes a while.
	mux.HandleFunc("GET /api/sessions/progress", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, sessions.Indexing())
	})
	// one is a session by its stats key, read however long ago it was at
	// work: the page's top sessions reach past the latest List reads.
	mux.HandleFunc("GET /api/sessions/one", func(rw http.ResponseWriter, r *http.Request) {
		s, ok := sessions.Get(r.URL.Query().Get("key"))
		if !ok {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(rw).Encode(map[string]string{"error": "no such session"})
			return
		}
		j := sessionJSON{Session: s, Name: s.Agent, Icon: "generic"}
		for _, a := range agent.Clients() {
			if a.ID == s.Agent {
				j.Name, j.Icon = a.Name, a.Icon
			}
		}
		j.Name = wslName(j.Name, s)
		since := s.Start
		if since.IsZero() {
			since = s.Last
		}
		j.Via = usage.Vias(since.Add(-time.Minute))[s.Agent+"|"+s.ID]
		j.Path = tilde(j.Path)
		writeJSON(rw, j)
	})
	// transcript is what was said in a session, read from the agent's own
	// file (found from the session as listed, never a path from the page),
	// which is only read.
	mux.HandleFunc("GET /api/sessions/transcript", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s, ok := sessions.Find(q.Get("agent"), q.Get("id"))
		if !ok {
			fail(rw, errors.New("no such session"))
			return
		}
		t, err := sessions.TranscriptOf(s)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, t)
	})
	// terminal opens Terminal on a session's resume command, or, with In, on
	// the command that carries it on in that other agent. The command is
	// made here from the session as listed, never taken from the page.
	mux.HandleFunc("POST /api/sessions/terminal", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, ID, In string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if runtime.GOOS != "darwin" || isWeb(w) {
			fail(rw, errors.New("opening Terminal is only for the Mac app"))
			return
		}
		s, ok := sessions.Find(in.Agent, in.ID)
		run := s.Resume
		if in.In != "" {
			run = ""
			for _, c := range s.Carry {
				if c.Agent == in.In {
					run = c.Command
				}
			}
		}
		if !ok || run == "" {
			fail(rw, errors.New("no such session"))
			return
		}
		if err := openTerminal(run, settings.Load().SessionTerminal); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

// wslName is an agent's name for a session it ran in a WSL distro, as the
// agent there is named (Claude Code · WSL Ubuntu); its own name otherwise.
func wslName(name string, s sessions.Session) string {
	if s.WSL == "" {
		return name
	}
	return name + " · WSL " + s.WSL
}

// statsFor is sessions.StatsFor for a range, kept: the page asks for the
// stats and the overview one after the other, and for the same range again
// each time it is shown. What was read is answered at once; when it is more
// than a few seconds old it is read again behind, for the next ask.
var statsMemo struct {
	sync.Mutex
	m map[int]*statsKept
}

type statsKept struct {
	at   time.Time
	st   sessions.Stats
	busy bool
}

func statsFor(days int) sessions.Stats {
	days = max(0, days)
	statsMemo.Lock()
	if k := statsMemo.m[days]; k != nil {
		if !k.busy && time.Since(k.at) > 10*time.Second {
			k.busy = true
			go func() {
				st := sessions.StatsFor(days)
				statsMemo.Lock()
				k.at, k.st, k.busy = time.Now(), st, false
				statsMemo.Unlock()
			}()
		}
		st := k.st
		statsMemo.Unlock()
		return st
	}
	statsMemo.Unlock()
	st := sessions.StatsFor(days)
	statsMemo.Lock()
	if statsMemo.m == nil {
		statsMemo.m = map[int]*statsKept{}
	}
	if statsMemo.m[days] == nil {
		statsMemo.m[days] = &statsKept{at: time.Now(), st: st}
	}
	statsMemo.Unlock()
	return st
}

// warmSessions reads every session file once magpie is up, so the Sessions
// page opens on the kept index and not on a first read of them all. Usage ›
// Requests' whole history is read after it, not beside it on the disk: a
// first All parsing 12k sessions kept that page a skeleton for 40 s.
func warmSessions() {
	if testing.Testing() {
		return
	}
	go func() {
		time.Sleep(3 * time.Second)
		statsFor(0)
		statsFor(30)
		usage.QueryPage(usage.All, usage.Filter{}, 0, 50)
	}()
}

// openTerminal runs a command in the chosen Mac terminal through a .command
// file, as it would open from Finder: no Automation consent.
// The shell is left open when the agent quits.
func openTerminal(command, choice string) error {
	var found terminalDiscovery
	if choice != terminalBundleID {
		var err error
		// the system default falls back to Terminal when nothing is found
		if found, err = discoverTerminals(); err != nil && choice != "" && choice != "system" {
			return err
		}
	}
	args, err := terminalOpenArgs(choice, found)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "magpie-resume-*.command")
	if err != nil {
		return err
	}
	script := "#!/bin/sh\n" + command + "\nexec \"${SHELL:-/bin/zsh}\" -l\n"
	_, err = f.WriteString(script)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o700)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	args = append(args, f.Name())
	if err := proc.Command("open", args...).Run(); err != nil {
		os.Remove(f.Name())
		return err
	}
	// Terminal has read it long before
	time.AfterFunc(time.Minute, func() { os.Remove(f.Name()) })
	return nil
}
