package gui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
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

func gatewayExternal(g usage.GatewaySession) sessions.ExternalSession {
	e := sessions.ExternalSession{Agent: g.Agent, ID: g.ID, Start: g.Start, Last: g.Last, Tokens: g.Tokens, Cost: g.Cost, Unpriced: g.Unpriced}
	for _, m := range g.Models {
		e.Models = append(e.Models, sessions.ExternalModel{Model: m.Model, Tokens: m.Tokens, Cost: m.Cost, Priced: m.Priced})
	}
	for _, d := range g.Daily {
		e.Daily = append(e.Daily, sessions.ExternalDayUsage{Date: d.Date, Agent: d.Agent, Model: d.Model, Tokens: d.Tokens, Cost: d.Cost, Priced: d.Priced})
	}
	return e
}

func gatewaySession(g usage.GatewaySession) sessions.Session {
	return sessions.MergeExternal(nil, []sessions.ExternalSession{gatewayExternal(g)})[0]
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
		list := sessions.List(sessions.All)
		nativeKeys := map[string]bool{}
		for _, s := range list {
			nativeKeys[s.Agent+"|"+s.ID] = true
		}
		gateway := usage.GatewaySessions(time.Time{}, nativeKeys)
		ext := make([]sessions.ExternalSession, 0, len(gateway))
		for _, g := range gateway {
			ext = append(ext, gatewayExternal(g))
		}
		list = sessions.MergeExternal(list, ext)
		if len(list) > n {
			list = list[:n]
		}
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
		key := r.URL.Query().Get("key")
		s, ok := sessions.Get(key)
		if !ok {
			if a, id, found := strings.Cut(key, ":"); found {
				if g, gok := usage.GatewaySessionByID(a, id, nil); gok {
					s = gatewaySession(g)
					ok = true
				}
			}
		}
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
			if _, found := usage.GatewaySessionByID(q.Get("agent"), q.Get("id"), nil); found {
				t, err := sessions.GatewayTranscript(q.Get("agent"), q.Get("id"))
				if err != nil {
					fail(rw, err)
					return
				}
				writeJSON(rw, t)
				return
			}
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
	// markdown is a session's whole conversation as a Markdown file (#1276),
	// read from the agent's own file as transcript is: to the browser that
	// asks (magpie web), which saves it itself, and, in the app, to
	// Downloads.
	mux.HandleFunc("GET /api/sessions/markdown", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		stem, md, err := sessionMarkdown(q.Get("agent"), q.Get("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		rw.Header().Set("Content-Disposition", `attachment; filename="`+stem+`.md"`)
		rw.Write(md)
	})
	mux.HandleFunc("POST /api/sessions/export", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		stem, md, err := sessionMarkdown(q.Get("agent"), q.Get("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		path, err := saveDownload(downloads(), stem, ".md", bytes.NewReader(md))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"path": tilde(path)})
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

// sessionMarkdown is a session's conversation as Markdown, and the name its
// file is saved under: the session found as listed, never a path from the
// page. It is made whole before any of it is sent, so a file that can't be
// read is an error and not half a download.
func sessionMarkdown(agentID, id string) (stem string, md []byte, err error) {
	s, ok := sessions.Find(agentID, id)
	if !ok {
		return "", nil, errors.New("no such session")
	}
	name := s.Agent
	for _, a := range agent.Clients() {
		if a.ID == s.Agent {
			name = a.Name
		}
	}
	var b bytes.Buffer
	if err := sessions.WriteMarkdown(&b, s, wslName(name, s)); err != nil {
		return "", nil, err
	}
	short := []rune(s.ID)
	if len(short) > 12 {
		short = short[:12]
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s.Agent+"-"+string(short))
	return "magpie-session-" + safe, b.Bytes(), nil
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
				st := combinedSessionStats(days)
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
	st := combinedSessionStats(days)
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

func combinedSessionStats(days int) sessions.Stats {
	st := sessions.StatsFor(days)
	nativeKeys := map[string]bool{}
	for _, s := range sessions.List(sessions.All) {
		nativeKeys[s.Agent+"|"+s.ID] = true
	}
	var since time.Time
	if days > 0 && st.From != "" {
		since, _ = time.ParseInLocation(time.DateOnly, st.From, time.Local)
	}
	gs, gd := usage.GatewaySessionWindowExcept(since, nativeKeys)
	ext := make([]sessions.ExternalSession, 0, len(gs))
	for _, g := range gs {
		ext = append(ext, gatewayExternal(g))
	}
	daily := make([]sessions.ExternalDayUsage, 0, len(gd))
	for _, d := range gd {
		daily = append(daily, sessions.ExternalDayUsage{Date: d.Date, Agent: d.Agent, Model: d.Model, Tokens: d.Tokens, Cost: d.Cost, Priced: d.Priced})
	}
	return sessions.MergeExternalStats(st, daily, ext)
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
		warmUp(
			func() { statsFor(0) },
			func() { statsFor(30) },
			func() { usage.QueryPage(usage.All, usage.Filter{}, 0, 50) },
		)
	}()
}

// warmUp runs the warm-up's reads, then hands what they threw away back to
// the system at once. Reading a long history allocates many times what it
// keeps (a 13k-session Codex and Claude Code history: ~830 MB allocated to
// keep ~80 MB of indexes), and Go's scavenger returns those pages only over
// the next minutes: a magpie that had just started read ~470 MB for its
// first two minutes at rest.
func warmUp(steps ...func()) {
	for _, step := range steps {
		step()
	}
	debug.FreeOSMemory()
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
