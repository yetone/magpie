package gui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
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

// gatewayExternal is a gateway session without its days: a list, a manage
// page and a detail (MergeExternal) never read them, and copying every
// session's days for each poll of an open Sessions page was ~1.8 GB in 4
// minutes over ~720k calls (#2776). The stats add them (gatewayDays).
func gatewayExternal(g usage.GatewaySession) sessions.ExternalSession {
	e := sessions.ExternalSession{Agent: g.Agent, ID: g.ID, Start: g.Start, Last: g.Last, Tokens: g.Tokens, Cost: g.Cost, Unpriced: g.Unpriced}
	e.Models = make([]sessions.ExternalModel, 0, len(g.Models))
	for _, m := range g.Models {
		e.Models = append(e.Models, sessions.ExternalModel{Model: m.Model, Tokens: m.Tokens, Cost: m.Cost, Priced: m.Priced})
	}
	return e
}

// gatewayDays is a gateway session's usage by day, as the stats count it.
func gatewayDays(g usage.GatewaySession) []sessions.ExternalDayUsage {
	out := make([]sessions.ExternalDayUsage, 0, len(g.Daily))
	for _, d := range g.Daily {
		out = append(out, sessions.ExternalDayUsage{Date: d.Date, Agent: d.Agent, Model: d.Model, Tokens: d.Tokens, Cost: d.Cost, Priced: d.Priced})
	}
	return out
}

// gatewayListed is a gateway session as a list shows it: titled by what it
// was first asked, when magpie recorded it, not by its ses_… id (YOU on
// Discord). title is a sessions.GatewayTitler's.
func gatewayListed(g usage.GatewaySession, title func(agent, id string) string) sessions.ExternalSession {
	e := gatewayExternal(g)
	e.Title = title(g.Agent, g.ID)
	return e
}

// listedExternal are the gateway's sessions a list of n rows can show:
// those native doesn't hold (nativeKeys), as listedGateway keeps them and
// gatewayListed makes them, newest first as EachGatewaySession gives them,
// and no more than n, as a merge with the natives shows the newest n at
// most. Each of ~10k gateway sessions past the natives' cap was copied
// three times on every poll while a session grew, ~20 MB of a poll's
// ~150 MB, and each was looked for in the store of what was asked
// (GatewayTitler).
func listedExternal(nativeKeys map[string]bool, n int) []sessions.ExternalSession {
	hidden := sessions.GatewayHidden()
	title := sessions.GatewayTitler()
	var ext []sessions.ExternalSession
	usage.EachGatewaySession(nativeKeys, func(g usage.GatewaySession) bool {
		if len(ext) == n {
			return false
		}
		// one a list leaves out (externalSessions) takes no room
		if g.Agent != "" && g.ID != "" && !hidden(g.Agent, g.ID, g.Last) {
			ext = append(ext, gatewayListed(g, title))
		}
		return true
	})
	return ext
}

func gatewaySession(g usage.GatewaySession) sessions.Session {
	return sessions.MergeExternal(nil, []sessions.ExternalSession{gatewayListed(g, sessions.GatewayTitle)})[0]
}

func sessionRoutes(mux *http.ServeMux, w Windows) {
	warmSessions()
	mux.HandleFunc("GET /api/sessions", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		// the list and the Codex threads' effort each list every session file
		defer sessions.OneListing()()
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
		// what a row is named and drawn as, beside the list, for listTag
		var drawn strings.Builder
		for _, a := range agent.Clients() {
			agents[a.ID] = a
			drawn.WriteString(a.ID + "\x00" + a.SharedName() + "\x00" + a.Icon + "\x00")
		}
		out := sessionsJSON{Sessions: []sessionJSON{}, Terminal: terminalHere() && !isWeb(w),
			Dirs: []string{}}
		for _, d := range sessions.Dirs() {
			out.Dirs = append(out.Dirs, tilde(d))
			drawn.WriteString("\x01" + out.Dirs[len(out.Dirs)-1])
		}
		// the page has this very answer when the listing, the usage log,
		// the aliases and the agents are what they were when it was sent:
		// told so without the rows being made, encoded and hashed again,
		// ~60% of an unchanged poll's ~22 MB over ~17k real sessions
		logV, logKnown := usage.LogVersion()
		aliases := provider.UsageAliases()
		now := listTag{log: logV, drawn: drawn.String(), aliases: aliases}
		made := -1
		listTags.Lock()
		if t, ok := listTags.m[n]; ok && logKnown && t.same(now) && r.Header.Get("X-Magpie-Had") == t.tag {
			made = t.made
		}
		listTags.Unlock()
		list, made, same := sessions.ListUnless(sessions.All, made)
		if same {
			rw.Header().Set("X-Magpie-Tag", r.Header.Get("X-Magpie-Had"))
			rw.Header().Set("X-Magpie-Same", "1")
			rw.WriteHeader(http.StatusOK)
			return
		}
		nativeKeys := make(map[string]bool, len(list))
		for _, s := range list {
			// its copies' and subagents' ids among them
			for _, k := range sessions.GatewayKeys(s) {
				nativeKeys[k] = true
			}
		}
		ext := listedExternal(nativeKeys, n)
		// made to size: up to sessions.All rows, grown by append each poll
		// was copied through every size on the way, ~140 MB of eight (#2278).
		// The rows are made from the list and the gateway's sessions as
		// they merge, not from a merged copy of the list.
		out.Sessions = make([]sessionJSON, 0, min(n, len(list)+len(ext)))
		codex := false
		sessions.EachMerged(list, ext, func(s *sessions.Session) bool {
			if len(out.Sessions) == n {
				return false
			}
			codex = codex || s.Agent == "codex"
			out.Sessions = append(out.Sessions, sessionJSON{Session: *s})
			return true
		})
		var modelEffort map[string][]sessions.ModelEffort
		if codex {
			modelEffort = usage.CodexModelEffort(usage.All)
		}
		since := time.Now()
		for i := range out.Sessions {
			s := &out.Sessions[i].Session
			addCodexModelEffort(s, modelEffort)
			if !s.Start.IsZero() && s.Start.Before(since) {
				since = s.Start
			}
		}
		vias := usage.Vias(since.Add(-time.Minute))
		for i := range out.Sessions {
			j := &out.Sessions[i]
			s := j.Session
			// drawn as the agent it ran in: Claude Desktop's Code tab's
			// sessions are in Claude Code's folder (Session.App)
			j.Name, j.Icon, j.Via = s.Agent, "generic", viaOf(vias, s)
			if a := agents[cmp.Or(s.App, s.Agent)]; a != nil {
				j.Name, j.Icon = a.SharedName(), a.Icon
			}
			j.Name = wslName(j.Name, s)
			j.Path = tilde(j.Path)
		}
		listsRowed.Add(1)
		tag := writeJSONRows(rw, r, "sessions", out.Sessions, struct {
			Terminal bool     `json:"terminal"`
			Dirs     []string `json:"dirs"`
		}{out.Terminal, out.Dirs})
		if tag != "" && logKnown {
			now.made, now.tag = made, tag
			listTags.Lock()
			if listTags.m == nil || len(listTags.m) >= statsMemoMost {
				listTags.m = map[int]listTag{}
			}
			listTags.m[n] = now
			listTags.Unlock()
		}
	}))
	// stats is every session's usage by day over the last ?days=N days
	// (every day when 0 or none), cut by agent, folder and model for the
	// page to filter; names are the agents' as the page shows them.
	mux.HandleFunc("GET /api/sessions/stats", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		defer sessions.ShareListing()()
		n, _ := strconv.Atoi(r.URL.Query().Get("days"))
		st, gen := statsForGen(n)
		names := map[string]string{}
		for _, a := range agent.Clients() {
			names[a.ID] = a.SharedName()
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
		// the page has this very answer: told so without encoding and
		// hashing a long history's ~6 MB of it again on every poll (#2278)
		statsTags.Lock()
		t, ok := statsTags.m[n]
		statsTags.Unlock()
		if ok && t.gen == gen && maps.Equal(t.agents, agents) && r.Header.Get("X-Magpie-Had") == t.tag {
			rw.Header().Set("X-Magpie-Tag", t.tag)
			rw.Header().Set("X-Magpie-Same", "1")
			rw.WriteHeader(http.StatusOK)
			return
		}
		tag := writeSame(rw, r, func(w io.Writer) error { return statsAnswer(w, st, agents) })
		if tag != "" {
			statsTags.Lock()
			if statsTags.m == nil || len(statsTags.m) >= statsMemoMost {
				statsTags.m = map[int]statsTag{}
			}
			statsTags.m[n] = statsTag{gen, agents, tag}
			statsTags.Unlock()
		}
	}))
	// overview sums up the range's sessions under the page's filters:
	// how many, what the middle one spent, how many each day, and the ones
	// that spent the most.
	mux.HandleFunc("GET /api/sessions/overview", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		defer sessions.ShareListing()()
		q := r.URL.Query()
		n, _ := strconv.Atoi(q.Get("days"))
		writeOverview(rw, max(0, n), q.Get("agent"), q.Get("model"), q.Get("cwd"))
	}))
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
		if s.Agent == "codex" {
			addCodexModelEffort(&s, usage.CodexModelEffort(usage.All))
		}
		j := sessionJSON{Session: s, Name: s.Agent, Icon: "generic"}
		for _, a := range agent.Clients() {
			if a.ID == cmp.Or(s.App, s.Agent) {
				j.Name, j.Icon = a.SharedName(), a.Icon
			}
		}
		j.Name = wslName(j.Name, s)
		since := s.Start
		if since.IsZero() {
			since = s.Last
		}
		j.Via = viaOf(usage.Vias(since.Add(-time.Minute)), s)
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
		// sub: one of its subagents, found among those the session lists
		// (#2261)
		read := sessions.TranscriptOf
		if sub := q.Get("sub"); sub != "" {
			read = func(s sessions.Session) (sessions.Transcript, error) { return sessions.SubagentTranscript(s, sub) }
		}
		t, err := read(s)
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
		if !terminalHere() || isWeb(w) {
			fail(rw, errors.New("opening a terminal is only for the desktop app"))
			return
		}
		s, ok := sessions.Find(in.Agent, in.ID)
		run := s.Resume
		if ok && in.In == "" {
			run = sessions.ResumeNow(s)
		}
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
		if err := openTerminal(run, s.Title, settings.Load().SessionTerminal); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// agents/open opens Terminal on an agent of the Agents list: the command
	// is made here from the agent as detected (agentCommand), and the page
	// sends only its id.
	mux.HandleFunc("POST /api/agents/open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if !terminalHere() || isWeb(w) {
			fail(rw, errors.New("opening a terminal is only for the desktop app"))
			return
		}
		run, name := "", ""
		for _, a := range agent.Detected() {
			if a.ID == in.Agent {
				run, name = agentCommand(a), a.Name
			}
		}
		if run == "" {
			fail(rw, errors.New("no command starts this agent"))
			return
		}
		if err := agentTerminal(run, name, settings.Load().SessionTerminal); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// carry converts a session for another agent (one its Carry offers with
	// Convert): a new session of that agent's, in its own folder, the
	// source only read. It runs only when the user picks the agent, never
	// on a listing. It answers with the line that opens the new one, and,
	// with Open, opens Terminal on it.
	mux.HandleFunc("POST /api/sessions/carry", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent, ID, To string
			Open          bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Open && (!terminalHere() || isWeb(w)) {
			fail(rw, errors.New("opening a terminal is only for the desktop app"))
			return
		}
		s, ok := sessions.Find(in.Agent, in.ID)
		if !ok {
			fail(rw, errors.New("no such session"))
			return
		}
		c, err := sessions.CarryOver(s, in.To)
		if err != nil {
			fail(rw, err)
			return
		}
		if in.Open {
			if err := openTerminal(c.Command, s.Title, settings.Load().SessionTerminal); err != nil {
				fail(rw, err)
				return
			}
		}
		writeJSON(rw, map[string]any{"agent": c.Agent, "id": c.ID, "command": c.Command})
	})
}

// sessionMarkdown is a session's conversation as Markdown, and the name its
// file is saved under: the session found as listed, never a path from the
// page. It is made whole before any of it is sent, so a file that can't be
// read is an error and not half a download.
//
// A session only magpie's gateway recorded is written from that record, as
// transcript reads it: the agent of one that reaches this magpie through a
// remote magpie ran on another machine, so there is no file of it here, and
// its download was a 400 "no such session" (akic404 on Discord).
func sessionMarkdown(agentID, id string) (stem string, md []byte, err error) {
	s, ok := sessions.Find(agentID, id)
	var t sessions.Transcript
	if !ok {
		g, found := usage.GatewaySessionByID(agentID, id, nil)
		if !found {
			return "", nil, errors.New("no such session")
		}
		if t, err = sessions.GatewayTranscript(agentID, id); err != nil {
			return "", nil, err
		}
		s = gatewaySession(g)
	}
	name := s.Agent
	for _, a := range agent.Clients() {
		if a.ID == s.Agent {
			name = a.SharedName()
		}
	}
	var b bytes.Buffer
	if ok {
		err = sessions.WriteMarkdown(&b, s, wslName(name, s))
	} else {
		err = sessions.WriteGatewayMarkdown(&b, s, name, t)
	}
	if err != nil {
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
	m    map[int]*statsKept
	asks uint64
}

type statsKept struct {
	at   time.Time
	st   sessions.Stats
	gen  uint64 // st's, new only when a read found st changed
	busy bool
	// used is the memo's ask count when this range was last asked: a count,
	// not a clock, since Windows' clock can give two asks the same time.
	used uint64
}

// statsMemoMost is how many distinct ?days values the memo keeps: each one is
// a whole sessions.Stats, and ?days takes any number a caller sends, so the
// least recently asked one is let go when a new value comes past it.
const statsMemoMost = 8

// statsGen numbers each Stats the memo keeps, across memos: an answer's
// tag is known by it (statsTags).
var statsGen atomic.Uint64

// statsTags is the tag of the stats answer last sent for each ?days, the
// Stats it was made from known by its gen.
var statsTags struct {
	sync.Mutex
	m map[int]statsTag
}

type statsTag struct {
	gen    uint64
	agents map[string]string
	tag    string
}

// listTags is, by ?limit, what the last list sent was made from and its
// tag: an ask with that tag while all of it stands gets X-Magpie-Same
// without the list being made. What a row shows beyond these (a Codex
// thread's effort, a gateway session's title) is bounded by the listing,
// made again at least once in sessions' listKeptFor.
var listTags struct {
	sync.Mutex
	m map[int]listTag
}

// listsRowed counts the lists made into rows and sent, for tests.
var listsRowed atomic.Int64

type listTag struct {
	made    int    // sessions.ListUnless's listing
	log     uint64 // usage.LogVersion
	drawn   string // the agents' names and icons, and the folders
	aliases map[string]string
	tag     string
}

func (t listTag) same(u listTag) bool {
	return t.log == u.log && t.drawn == u.drawn && maps.Equal(t.aliases, u.aliases)
}

// sameStats is whether a and b would be sent the same: a read again
// behind finds the same stats most times, and keeping a's gen spares the
// page's next ask the encoding.
func sameStats(a, b sessions.Stats) bool {
	return a.From == b.From && a.To == b.To && slices.Equal(a.Keys, b.Keys) && reflect.DeepEqual(a.Days, b.Days)
}

// overviewKey is what an overview's answer is made from: the stats it
// was summed from (statsForGen's gen, which moves only when they do) and
// the page's filters. The Codex rows' model and effort are added after.
type overviewKey struct {
	days              int
	gen               uint64
	agent, model, cwd string
}

// overviews are the last few overviews sent, encoded. The Sessions page
// asks for one on every 15s poll, and summing a long history's 34k
// sessions by tool and skill again, to the same answer, was ~36 ms of CPU
// an ask.
var overviews struct {
	sync.Mutex
	m map[overviewKey]*overviewKept
}

type overviewKept struct {
	out   sessions.Overview // before the Codex rows' model and effort
	codex bool              // a top row is Codex's
	// the CodexModelEffort map body was made with, held so that a new one
	// is never told from it by an address the old one gave back
	effort map[string][]sessions.ModelEffort
	body   []byte
}

// overviewsMade counts the overviews summed, for tests.
var overviewsMade atomic.Int64

const overviewsKept = 8

func writeOverview(rw http.ResponseWriter, days int, agentID, model, cwd string) {
	st, gen := statsForGen(days)
	key := overviewKey{days, gen, agentID, model, cwd}
	overviews.Lock()
	k := overviews.m[key]
	overviews.Unlock()
	if k == nil {
		overviewsMade.Add(1)
		k = &overviewKept{out: st.Overview(agentID, model, cwd, 8)}
		for _, top := range k.out.Top {
			for i := range top {
				k.codex = k.codex || top[i].Agent == "codex"
			}
		}
	}
	var effort map[string][]sessions.ModelEffort
	if k.codex {
		period := usage.All
		if days > 0 {
			if p, ok := usage.RangeOf(st.From, st.To); ok {
				period = p
			}
		}
		effort = usage.CodexModelEffort(period)
	}
	body := k.body
	if body == nil || reflect.ValueOf(effort).UnsafePointer() != reflect.ValueOf(k.effort).UnsafePointer() {
		out := k.out
		out.Top = make(map[string][]sessions.Summary, len(k.out.Top))
		for by, top := range k.out.Top {
			top = slices.Clone(top)
			if k.codex {
				for i := range top {
					addCodexSummaryModelEffort(&top[i], effort)
				}
			}
			out.Top[by] = top
		}
		b, err := json.Marshal(out)
		if err != nil {
			fail(rw, err)
			return
		}
		body = append(b, '\n')
		overviews.Lock()
		if overviews.m == nil || len(overviews.m) >= overviewsKept && overviews.m[key] == nil {
			overviews.m = map[overviewKey]*overviewKept{} // the page asks a few at a time; the rest were left behind
		}
		overviews.m[key] = &overviewKept{out: k.out, codex: k.codex, effort: effort, body: body}
		overviews.Unlock()
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(body)
}

func statsFor(days int) sessions.Stats {
	st, _ := statsForGen(days)
	return st
}

func statsForGen(days int) (sessions.Stats, uint64) {
	days = max(0, days)
	keepStatsWhileAsked()
	statsMemo.Lock()
	if k := statsMemo.m[days]; k != nil {
		statsMemo.asks++
		k.used = statsMemo.asks
		if !k.busy && time.Since(k.at) > 10*time.Second {
			// every range kept that is as old is read again with it, in
			// one pass: the page asks for 7, 30 and every day, and each
			// read alone listed and summed every session file (#2278)
			var ranges []int
			var kept []*statsKept
			for d, o := range statsMemo.m {
				if !o.busy && time.Since(o.at) > 10*time.Second {
					o.busy = true
					ranges, kept = append(ranges, d), append(kept, o)
				}
			}
			// read beside the page's own reads: with their listing of
			// the session files, and handed back once they all rest
			shared, rest := sessions.ShareListing(), readingPage()
			go func() {
				defer rest()
				defer shared()
				sts := combinedSessionStatsAll(ranges)
				statsMemo.Lock()
				for i, o := range kept {
					if !sameStats(o.st, sts[i]) {
						o.st, o.gen = sts[i], statsGen.Add(1)
					}
					o.at, o.busy = time.Now(), false
				}
				statsMemo.Unlock()
			}()
		}
		st, gen := k.st, k.gen
		statsMemo.Unlock()
		return st, gen
	}
	statsMemo.Unlock()
	st, gen := combinedSessionStats(days), statsGen.Add(1)
	statsMemo.Lock()
	if statsMemo.m == nil {
		statsMemo.m = map[int]*statsKept{}
	}
	if statsMemo.m[days] == nil {
		if len(statsMemo.m) >= statsMemoMost {
			oldest := -1
			for d, k := range statsMemo.m {
				if oldest == -1 || k.used < statsMemo.m[oldest].used {
					oldest = d
				}
			}
			delete(statsMemo.m, oldest)
		}
		statsMemo.asks++
		statsMemo.m[days] = &statsKept{at: time.Now(), st: st, gen: gen, used: statsMemo.asks}
	}
	statsMemo.Unlock()
	return st, gen
}

// statsIdle is how long the Sessions page's stats are kept after the last
// ask. The page polls every 15 s while it is open; once it has been shut
// for this long, every range's Stats (a Summary for each session ever:
// ~15 MB of a 14k-session history), the overviews and the answers' tags
// go. The parsed session files stay, so the next ask sums them up again
// (~0.1 s a range) without reading a file.
const statsIdle = 2 * time.Minute

var statsIdleTimer struct {
	sync.Mutex
	t *time.Timer
	// off: tests, which ask for stats over minutes, have it armed only
	// when they ask for it
	off bool
}

func init() { statsIdleTimer.off = testing.Testing() }

// keepStatsWhileAsked puts off forgetIdleStats for statsIdle.
func keepStatsWhileAsked() {
	statsIdleTimer.Lock()
	defer statsIdleTimer.Unlock()
	if statsIdleTimer.off {
		return
	}
	if statsIdleTimer.t != nil {
		statsIdleTimer.t.Reset(statsIdle)
		return
	}
	statsIdleTimer.t = time.AfterFunc(statsIdle, forgetIdleStats)
}

// forgetIdleStats lets go of what statsForGen and writeOverview kept, and
// hands the pages back to the system at once; a range being read again
// puts it off.
func forgetIdleStats() {
	statsMemo.Lock()
	for _, k := range statsMemo.m {
		if k.busy {
			statsMemo.Unlock()
			keepStatsWhileAsked()
			return
		}
	}
	statsMemo.m = nil
	statsMemo.Unlock()
	overviews.Lock()
	overviews.m = nil
	overviews.Unlock()
	statsTags.Lock()
	statsTags.m = nil
	statsTags.Unlock()
	sessions.ForgetKeptRanges()
	debug.FreeOSMemory()
}

func combinedSessionStats(days int) sessions.Stats {
	return combinedSessionStatsAll([]int{days})[0]
}

// combinedSessionStatsAll is combinedSessionStats for each of days, the
// session files read once for all of them.
func combinedSessionStatsAll(days []int) []sessions.Stats {
	sts, nativeKeys := sessions.StatsAndNativeAll(days)
	for i := range sts {
		sts[i] = withGatewaySessions(sts[i], days[i], nativeKeys)
	}
	return sts
}

func withGatewaySessions(st sessions.Stats, days int, nativeKeys map[string]bool) sessions.Stats {
	var since time.Time
	if days > 0 && st.From != "" {
		since, _ = time.ParseInLocation(time.DateOnly, st.From, time.Local)
	}
	gs, gd := usage.GatewaySessionWindowExcept(since, nativeKeys)
	ext := make([]sessions.ExternalSession, 0, len(gs))
	title := sessions.GatewayTitler()
	for _, g := range gs {
		e := gatewayListed(g, title)
		e.Daily = gatewayDays(g)
		ext = append(ext, e)
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
// Once a process: the app's page served to a browser (apppage.go) is a
// second Handler beside the window's.
func warmSessions() {
	if testing.Testing() {
		return
	}
	warmOnce.Do(warmStart)
}

var warmOnce sync.Once

func warmStart() {
	steadyGCStart()
	go func() {
		// a fresh install reads no agent's sessions until asked; one
		// that read them before keeps reading them (decided once)
		sessions.SettleReads()
		time.Sleep(3 * time.Second)
		// with few readers, as nobody waits on it yet: after an update
		// that reads every file again, it held every core (#1665)
		sessions.Warm(func() { warmUp(warmSteps()...) })
	}()
}

// warmSteps are the reads the warm-up makes before any page asks for them.
// The last is Codex's per-thread effort, when there are Codex sessions: a
// first Codex page after a start read it from the whole ledger while it
// waited (#2776), and every later ask is answered from what it kept.
func warmSteps() []func() {
	return []func(){
		func() { statsFor(0) },
		func() { statsFor(30) },
		func() { underTightGC(func() { usage.QueryPage(usage.All, usage.Filter{}, 0, 50) }) },
		func() {
			if slices.ContainsFunc(statsFor(0).Keys, func(k string) bool { return strings.HasPrefix(k, "codex:") }) {
				underTightGC(warmCodexEffort)
			}
		},
	}
}

// warmCodexEffort reads what the Sessions page's Codex rows ask for.
var warmCodexEffort = func() { usage.CodexModelEffort(usage.All) }

// warmUp runs the warm-up's reads, then hands what they threw away back to
// the system at once. Reading a long history allocates many times what it
// keeps (a 13k-session Codex and Claude Code history: ~830 MB allocated to
// keep ~80 MB of indexes), and Go's scavenger returns those pages only over
// the next minutes: a magpie that had just started read ~470 MB for its
// first two minutes at rest.
func warmUp(steps ...func()) {
	// each file read looked at once a second, not for each session or row
	// that asks: an empty sandbox's warm-up looked at settings.json ~300
	// times on Windows (#1665); a write still reads the disk (edit.Read)
	defer filememo.Hold()()
	defer coldTurns.over.Store(true)
	for _, step := range steps {
		func() {
			coldTurns.turn <- struct{}{}
			defer coldTurnDone()
			step()
		}()
	}
	debug.FreeOSMemory()
}

// coldTurns has the warm-up's reads and the pages' first asks take turns
// until the warm-up is over. A first start's window asks the Sessions
// page's stats and the Usage page's requests and heatmap while the
// warm-up reads the same history, each a first read of it held beside the
// others: ~150 MB of the asks' and ~40 MB of the warm-up's at once, a
// ~300 MB live heap and a 420–480 MB footprint (#2278). Each reads after
// the one before it has kept what it read, and most then find it kept.
var coldTurns = struct {
	turn chan struct{}
	over atomic.Bool
}{turn: make(chan struct{}, 1)}

// coldTurnDone ends a turn. What the request matching kept for a next
// read goes with it: kept for scratchLinger, it sat under the next
// turn's read of the history.
func coldTurnDone() {
	usage.ReleaseMatchScratch()
	<-coldTurns.turn
}

// coldTurnMost is how long a page waits for its turn: a warm-up stuck on
// one read never keeps the pages from answering.
const coldTurnMost = 2 * time.Minute

// coldTurnsTaken is false in tests, whose pages and warm-ups are their own.
var coldTurnsTaken = !testing.Testing()

// coldTurn is h taking its turn while the warm-up isn't over; the
// warm-up's reading goes at full speed while it waits (held's
// sessions.Ask).
func coldTurn(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if coldTurnsTaken && !coldTurns.over.Load() {
			t := time.NewTimer(coldTurnMost)
			select {
			case coldTurns.turn <- struct{}{}:
				defer coldTurnDone()
			case <-t.C:
			}
			t.Stop()
		}
		h(rw, r)
	}
}

// underTightGC runs fn letting the heap grow by a tenth of what it holds,
// or by tightGCRoom when that is more (tightPercent), before a
// collection, not Go's default of all of it, and then puts back
// what was set; a GOGC the user set lower, or off, is kept. The usage
// page's read of the whole history holds every row of it at once (~270 MB
// live for 390k calls), and a cold start that rests at ~200 MB peaked at
// 555–587 MB in Activity Monitor there; under a quarter, 410–438 MB. That read
// and Codex's effort, which reads the whole ledger again while the page
// still holds its rows, run under it: the warm-up as a whole did too for
// ~11 s more CPU.
//
// It sets no memory limit. It held the memory Go asked of the system to
// what it held when fn began, and a cold start then reached the limit
// with the heap all live: from ~11 s to ~24 s it collected every ~160 ms,
// a core's worth, 3:39 of CPU in its first 60 s for 390k calls and 1166
// collections. Without it, 2:03–2:06 and 618, and the footprint's peak
// the same, 835–863 MB against 855 (#2278).
func underTightGC(fn func()) {
	defer tightenGC()()
	fn()
}

// tightGC is what the reads under a tight GC hold: the first to begin sets
// it, and the last to end puts back what was set before it. Each read
// putting back what it found let two at once leave GOGC tight for good,
// when the first to begin ended before the second.
var tightGC struct {
	sync.Mutex
	n      int  // reads under it
	was    int  // GOGC before the first
	hooked bool // a retightenGC is waiting for the next collection
	steady bool // steadyGCStart has run: retightenGC sets steadyPercent between reads
}

// tightenGC begins a read under a tight GC; undo ends it.
func tightenGC() (undo func()) {
	tightGC.Lock()
	defer tightGC.Unlock()
	if tightGC.n == 0 {
		was := debug.SetGCPercent(tightGCPercent)
		if was < tightGCPercent {
			debug.SetGCPercent(was)
		}
		tightGC.was = was
		if tightGCRoom > 0 && !tightGC.hooked {
			tightGC.hooked = true
			afterNextGC(retightenGC)
		}
	}
	tightGC.n++
	return sync.OnceFunc(func() {
		tightGC.Lock()
		defer tightGC.Unlock()
		if tightGC.n--; tightGC.n == 0 {
			debug.SetGCPercent(tightGC.was)
		}
	})
}

// tightGCPercent is a tenth. Under a quarter, a first start's footprint
// peaked at 390–397 MB, and a start with its caches at 304–321 MB, on a
// live heap of ~280 MB and ~250 MB: each read, and the window's polls
// beside it, let the heap grow by ~70 MB first. Under a tenth, 350–352
// MB and 271–275 MB, for ~20% more CPU in the first start's minute and
// ~2 s a minute of polls asking ten answers every 5 s (#2278).
const tightGCPercent = 10

// tightGCRoom is the least a tight GC lets the heap grow by before a
// collection. A tenth of a small heap is a few MB: a first start's reads
// began on a ~50 MB heap and allocate ~400 MB a second, so it collected
// ~28 times a second, 2,205 times in its first 78 s, and the collections
// were 69–72 s of its ~4:10 of CPU (#2278). With this room a heap of
// over 320 MB is held to a tenth still; one below grows by 32 MB. 0 in
// tests, whose GOGC is read as tightGCPercent while a read is under it.
var tightGCRoom = func() int64 {
	if testing.Testing() {
		return 0
	}
	return 32 << 20
}()

// tightPercent is the GOGC a tight GC sets on a live heap of live bytes:
// tightGCPercent, or enough for tightGCRoom, never above was, what was
// set before (off stays off).
func tightPercent(live int64, was int) int {
	p := tightGCPercent
	if live > 0 && tightGCRoom > 0 {
		p = max(p, int(min(tightGCRoom*100/live, math.MaxInt32)))
	}
	if was < p {
		return was
	}
	return p
}

// retightenGC sets the GOGC for the heap the collection just left, while
// a read is under a tight GC, and waits for the next one.
func retightenGC() {
	tightGC.Lock()
	defer tightGC.Unlock()
	if tightGC.n == 0 && !tightGC.steady {
		tightGC.hooked = false
		return
	}
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() == metrics.KindUint64 {
		live := int64(s[0].Value.Uint64())
		if tightGC.n > 0 {
			debug.SetGCPercent(tightPercent(live, tightGC.was))
		} else {
			debug.SetGCPercent(steadyPercent(live))
		}
	}
	afterNextGC(retightenGC)
}

// steadyGCStart has the heap grow by a quarter of what it holds before a
// collection, or by steadyGCRoom when that is more, not Go's default of
// all of it, for the life of the process; a GOGC the user set is kept.
// A window resting on ~60 MB live polls /api/state, /api/drift and
// /api/providers, ~10 MB thrown away a round, so at the default its
// footprint peaked at 138 MB and under half at 107-108 MB. On a real
// account's history (~100 MB live) a quarter took the resting footprint
// from 171 to 146 MB and its peak from 200 to 166 MB, with 60 busy poll
// rounds at 5.14s of CPU against 5.12s. Reads under a tight GC still set
// their tenth while they run.
func steadyGCStart() {
	if os.Getenv("GOGC") != "" {
		return
	}
	tightGC.Lock()
	defer tightGC.Unlock()
	tightGC.steady = true
	if tightGC.n == 0 {
		debug.SetGCPercent(steadyPercent(0))
	}
	if !tightGC.hooked {
		tightGC.hooked = true
		afterNextGC(retightenGC)
	}
}

// steadyGCPercent is the quarter steadyGCStart lets the heap grow by.
const steadyGCPercent = 25

// steadyGCRoom is the least it grows by: a quarter of a small heap is a
// few MB, and a heap under 64 MB grows by 16 MB, never by more than Go's
// default.
const steadyGCRoom = 16 << 20

// steadyPercent is the GOGC steadyGCStart sets on a live heap of live
// bytes (0 before the first collection: steadyGCPercent).
func steadyPercent(live int64) int {
	p := steadyGCPercent
	if live > 0 {
		p = max(p, int(min(steadyGCRoom*100/live, 100)))
	}
	return p
}

// afterNextGC runs fn on the finalizer goroutine once the next collection
// has finished.
func afterNextGC(fn func()) {
	type tick struct{ _ *byte } // a pointer keeps it off the tiny allocator
	runtime.SetFinalizer(&tick{}, func(*tick) { go fn() })
}

// agentCommand is the shell line that starts a: its Launch where it has
// one (agy, Claude Science, Cursor Private Inference), else its own
// program, quoted; on Windows a PowerShell line, one in a WSL distro
// through wsl.exe; "" for one in a WSL distro elsewhere or with no
// program here (a desktop app). Nothing of the page's goes into it.
func agentCommand(a *agent.Agent) string {
	launch := ""
	if a.Launch != nil {
		launch = a.Launch()
	}
	return agentLine(a, launch, runtime.GOOS)
}

// agentLine is agentCommand with a's Launch already asked, for goos.
func agentLine(a *agent.Agent, launch, goos string) string {
	if a.WSL != "" {
		if goos != "windows" || !plainBin.MatchString(a.Bin) {
			return ""
		}
		if launch == "" {
			launch = a.Bin
		}
		ps := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		return "wsl.exe -d " + ps(a.WSL) + " -e sh -lc " + ps(`exec "${SHELL:-sh}" -lic `+shellQuote(launch))
	}
	if launch != "" {
		return launch
	}
	if bin := a.BinPath(); bin != "" {
		if goos == "windows" {
			return "& '" + strings.ReplaceAll(bin, "'", "''") + "'"
		}
		return shellQuote(bin)
	}
	return ""
}

// plainBin is a program name a distro's shell takes as it is.
var plainBin = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// agentTerminal is openTerminal, a var so a test opens no terminal.
var agentTerminal = openTerminal

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// openTerminal runs a command in the chosen terminal, the shell left open
// when the agent quits: on Windows and Linux as terminal_open.go says.
func openTerminal(command, label, choice string) error {
	switch runtime.GOOS {
	case "windows":
		return openWinTerminal(command, choice)
	case "linux":
		return openLinuxTerminal(command, choice)
	}
	return openMacTerminal(command, label, choice)
}

// openMacTerminal runs a command in the chosen Mac terminal through a
// .command file, as it would open from Finder: no Automation consent; in
// Rex, through its CLI, in a tab labelled label (terminal_rex.go).
func openMacTerminal(command, label, choice string) error {
	var found terminalDiscovery
	if choice != terminalBundleID {
		var err error
		// the system default falls back to Terminal when nothing is found
		if found, err = discoverTerminals(); err != nil && choice != "" && choice != "system" {
			return err
		}
	}
	if choice == rexBundleID {
		for _, app := range found.Apps {
			if app.ID == rexBundleID {
				return openInRex(app.Path, command, label)
			}
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

// viaOf is what the gateway sent a session's calls to, under any key its
// gateway record can carry (sessions.GatewayKeys: Raft's Pi says it is Pi).
func viaOf(x usage.ViaIndex, s sessions.Session) []usage.Via {
	for _, k := range sessions.GatewayKeys(s) {
		if v := x.Of(k); v != nil {
			return v
		}
	}
	return nil
}

// statsAnswer writes the stats answer to w as an Encoder of
// struct{ sessions.Stats; Agents } does, a part at a time: encoded whole,
// the Encoder's own buffer grew to the ~6 MB answer from nothing after
// each collection, ~200 MB of 90 s of polls while an agent worked, and a
// buffer kept for the next held it between polls (#2278).
func statsAnswer(w io.Writer, st sessions.Stats, agents map[string]string) error {
	var part bytes.Buffer
	enc := json.NewEncoder(&part)
	// v as Marshal writes it: Encode ends it with a newline
	put := func(v any) error {
		part.Reset()
		if err := enc.Encode(v); err != nil {
			return err
		}
		_, err := w.Write(part.Bytes()[:part.Len()-1])
		return err
	}
	text := func(s string) error {
		_, err := io.WriteString(w, s)
		return err
	}
	if err := text(`{"from":`); err != nil {
		return err
	}
	if err := put(st.From); err != nil {
		return err
	}
	if err := text(`,"to":`); err != nil {
		return err
	}
	if err := put(st.To); err != nil {
		return err
	}
	if err := text(`,"days":`); err != nil {
		return err
	}
	if st.Days == nil {
		if err := text("null"); err != nil {
			return err
		}
	} else {
		if err := text("["); err != nil {
			return err
		}
		for i := range st.Days {
			if i > 0 {
				if err := text(","); err != nil {
					return err
				}
			}
			if err := put(&st.Days[i]); err != nil {
				return err
			}
		}
		if err := text("]"); err != nil {
			return err
		}
	}
	if err := text(`,"keys":`); err != nil {
		return err
	}
	if err := put(st.Keys); err != nil {
		return err
	}
	if err := text(`,"agents":`); err != nil {
		return err
	}
	if err := put(agents); err != nil {
		return err
	}
	return text("}\n")
}
