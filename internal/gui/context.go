package gui

// The Context tab: each agent's context windows as the gateway saw them —
// what its prompts hold, how fast they fill, how much its vendor read from
// its cache — with a score and tags worked out from those, its sessions'
// fill turn by turn, and each request's prompt one click away in Routing.
// Read from the routing history (gateway.Route.Prompt), the latest
// requests from the live trace.

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// defaultWindow is the window a prompt is measured against when the
// model's isn't known.
const defaultWindow = 200000

type ctxPoint struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	Tokens int       `json:"tokens"`
	Window int       `json:"window,omitempty"`
	Cache  int       `json:"cache,omitempty"` // of Tokens, read from the vendor's cache
	// CacheUnknown: the vendor counted no cache for it, so Cache's 0 is
	// not a miss (#3464)
	CacheUnknown bool `json:"cache_unknown,omitempty"`
}

type ctxSession struct {
	Agent    string          `json:"agent"`
	Key      string          `json:"key"`
	Title    string          `json:"title,omitempty"`
	Model    string          `json:"model"`
	First    time.Time       `json:"first"`
	Last     time.Time       `json:"last"`
	Requests int             `json:"requests"`
	Peak     int             `json:"peak"`
	Window   int             `json:"window"`
	Compacts int             `json:"compacts,omitempty"`
	Latest   *gateway.Prompt `json:"latest"` // the newest request's prompt
	LatestID int64           `json:"latestId"`
	Points   []ctxPoint      `json:"points"` // its requests' prompts, oldest first
}

// ctxScore is one of what makes an agent's score: its points of most.
type ctxScore struct {
	Key    string  `json:"key"` // cache, lean, pace, reliable
	Points float64 `json:"points"`
	Most   float64 `json:"most"`
}

// ctxTag is something an agent's prompts tell of it.
type ctxTag struct {
	Key   string  `json:"key"`
	Tone  string  `json:"tone"`            // good, warn, info
	Value float64 `json:"value,omitempty"` // the number it is told by
}

type ctxAgent struct {
	Agent      string             `json:"agent"`
	Requests   int                `json:"requests"` // with a prompt read
	Sessions   int                `json:"sessions"`
	Errors     int                `json:"errors"`
	Calls      int                `json:"calls"` // all of its requests, for the error rate
	Median     int                `json:"median"`
	P90        int                `json:"p90"`
	Baseline   int                `json:"baseline"` // its system prompt, tools and memory: what every request starts with
	Window     int                `json:"window"`
	Cache      float64            `json:"cache"`  // of its prompts, read from the cache
	Growth     int                `json:"growth"` // a request's prompt more than the one before it, the median
	PeakFill   float64            `json:"peakFill"`
	Compacts   int                `json:"compacts"`
	Shares     map[string]float64 `json:"shares"` // each part's share of its prompts
	MCP        int                `json:"mcp"`    // MCP servers' tool definitions, the median
	Score      int                `json:"score"`
	Scores     []ctxScore         `json:"scores"`
	Tags       []ctxTag           `json:"tags"`
	Models     []string           `json:"models"`
	LatestID   int64              `json:"latestId"`
	LatestTime time.Time          `json:"latestTime"`
	// Clients are the libraries scriptsAgent's card is of, the most used
	// first
	Clients []string `json:"clients,omitempty"`
}

// scriptsAgent is the card the requests of a script go under: those whose
// User-Agent names only an HTTP library, a runtime or an API SDK
// (usage.IsLibrary: curl, Bun, python-httpx), or nothing at all ("other").
// Each had a card of its own, scored as an agent's (theforeveriris on X).
// Read from each route's agent as kept, so the history's older days go
// there too; the Usage page's Agents list still has each by its own name.
const scriptsAgent = "scripts"

type ctxJSON struct {
	Days     int          `json:"days"`
	Agents   []ctxAgent   `json:"agents"`
	Sessions []ctxSession `json:"sessions"`
}

// ctxSessions is how many sessions are listed, the latest; ctxPoints how
// many requests a session's line keeps, the latest.
const (
	ctxSessions = 80
	ctxPoints   = 160
)

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

func quantile(xs []int, q float64) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*q))]
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

func partTokens(p *gateway.Prompt, kinds ...string) int {
	n := 0
	for _, x := range p.Parts {
		if slices.Contains(kinds, x.Kind) {
			n += x.Tokens
		}
	}
	return n
}

func mcpTokens(p *gateway.Prompt) int {
	n := 0
	for _, x := range p.Parts {
		if x.Kind != gateway.PartTools {
			continue
		}
		for _, it := range x.Items {
			if it.Tag == "mcp" {
				n += it.Tokens
			}
		}
	}
	return n
}

// ctxRec is what the tab reads of a route: its prompt's parts by their
// tokens, its items left out, and the whole prompt only where it can be its
// session's latest (keepLatest). The tab held a week of routes whole, the
// prompts' items and the tries' accounts with them, and read them again
// at every refresh (Yayoi_no_yume on X: a 1-core server with 962 MB went
// down with the tab open).
type ctxRec struct {
	ID                         int64
	Time                       time.Time
	Agent, Model, Kind, Sub    string
	Session, Parent, Conv      string
	ResponseID                 string
	Done, Failed               bool
	Asked                      bool // it has a prompt of some tokens
	Tokens, Window, MCP, Cache int
	Counted, Tools, CacheKnown bool
	Parts                      []gateway.PromptPart // items left out
	Prompt                     *gateway.Prompt      // whole, when kept
}

// recOf is r as the tab reads it, its whole prompt kept when whole.
func recOf(r gateway.Route, whole bool) ctxRec {
	c := ctxRec{ID: r.ID, Time: r.Time, Agent: r.Agent, Model: r.Model, Kind: r.Kind, Sub: r.Sub,
		Session: r.Session, Parent: r.ParentSession, Conv: r.Conv, ResponseID: r.ResponseID, Done: r.Done,
		Failed: r.Status >= 400 || r.Error != "", Cache: cacheOf(r), CacheKnown: cacheKnown(r)}
	if p := r.Prompt; p != nil {
		c.Asked = p.Tokens > 0
		c.Tokens, c.Window, c.Counted = p.Tokens, p.Window, p.Counted
		c.MCP, c.Tools = mcpTokens(p), withTools(p)
		c.Parts = make([]gateway.PromptPart, len(p.Parts))
		for i, x := range p.Parts {
			c.Parts[i] = gateway.PromptPart{Kind: x.Kind, Tokens: x.Tokens}
		}
		if whole {
			c.Prompt = p
		}
	}
	return c
}

// keepLatest is routes as the tab reads them, the whole prompt kept only on
// each session's latest route with tools and its latest without: of these
// two, the one the tab counts (toolSessions) is its session's latest, over
// the other days' routes too.
func keepLatest(routes []gateway.Route) []ctxRec {
	latest := map[string]int{}
	for i := range routes {
		r := &routes[i]
		// as contextOfRecs counts a session's request
		if !r.Done || r.Agent == "" || r.Status >= 400 || r.Error != "" || r.Prompt == nil || r.Prompt.Tokens <= 0 || r.Kind != "" || r.Sub != "" {
			continue
		}
		k := r.Agent + "\x00" + sessionOfRoute(*r) + "\x00" + strconv.FormatBool(withTools(r.Prompt))
		if j, ok := latest[k]; !ok || !r.Time.Before(routes[j].Time) {
			latest[k] = i
		}
	}
	whole := make(map[int]bool, len(latest))
	for _, i := range latest {
		whole[i] = true
	}
	out := make([]ctxRec, len(routes))
	for i, r := range routes {
		out[i] = recOf(r, whole[i])
	}
	return out
}

func cacheOf(r gateway.Route) int {
	if len(r.Usage) == 0 {
		return 0
	}
	return r.Usage[len(r.Usage)-1].CacheRead
}

// cacheKnown is whether the vendor counted the cache of r's answering try:
// one that counts none (Kiro) is left out of a hit rate rather than read as
// a miss (#3464).
func cacheKnown(r gateway.Route) bool {
	return len(r.Usage) == 0 || !cacheNotCounted(r.Usage[len(r.Usage)-1])
}

// sessionOfRoute is the conversation a route is part of.
func sessionOfRoute(r gateway.Route) string {
	switch {
	case r.ParentSession != "":
		return r.ParentSession
	case r.Session != "":
		return r.Session
	}
	return r.Conv
}

func sessionOfRec(r *ctxRec) string {
	switch {
	case r.Parent != "":
		return r.Parent
	case r.Session != "":
		return r.Session
	}
	return r.Conv
}

// joinConvs puts a route that names no session under the agent's session
// its reply or its conversation id was read from (sessions' ResponseIDs and
// Convs): dsh's chat-completions and messages routes send no session id, so
// the gateway keys each request by its first prompt's hash, and the page
// showed "magpie-a…" where the Sessions page shows the session's title
// (Kumo31 on Discord). A hash is the conversation's opening, which a fork
// copies, so the reply's own id is asked first: a fork's reply has one its
// source does not, and names the fork. An id two sessions share stays as it
// is.
func joinConvs(routes []ctxRec, list func() []sessions.Session) {
	loose := false
	for _, r := range routes {
		if r.Session == "" && r.Parent == "" && (r.ResponseID != "" || r.Conv != "") {
			loose = true
			break
		}
	}
	if !loose {
		return
	}
	of := routeOwners(list())
	for i, r := range routes {
		if r.Session == "" && r.Parent == "" {
			if id := ownerOf(r.Agent, r.ResponseID, r.Conv, of); id != "" {
				routes[i].Session = id
			}
		}
	}
}

// ownerOf is the session an agent's route that names none belongs to: the
// one whose own replies carry the route's reply id, and, when no one's does,
// the one whose conversation the route opens. A reply id is the vendor's,
// which some give out again (Ollama's chatcmpl-<0-998>), so it names a
// session over the opening only where the two agree: the opening is no
// one's, or two sessions', or the reply's session's own, or one it was
// forked from (sessions' ForkOf). Another session's opening goes by the
// opening, as it did before reply ids were read.
func ownerOf(agent, responseID, conv string, of map[string]string) string {
	opened := ""
	if conv != "" {
		opened = of[ownerKey(agent, ownerConv, conv)]
	}
	if responseID != "" {
		if id := of[ownerKey(agent, ownerResponse, responseID)]; id != "" && (opened == "" || forkedFrom(agent, id, opened, of)) {
			return id
		}
	}
	return opened
}

// forkedFrom says session id is from, or a fork of it, or of a fork of it.
func forkedFrom(agent, id, from string, of map[string]string) bool {
	for n := 0; id != "" && n < 64; n++ {
		if id == from {
			return true
		}
		id = of[ownerKey(agent, ownerFork, id)]
	}
	return false
}

// The kinds of id a route without a session id is named by: the reply it
// got, and the conversation it opens; and a session's source, for the
// session a fork's reply names (ForkOf).
const (
	ownerResponse = "response"
	ownerConv     = "conv"
	ownerFork     = "fork"
)

func ownerKey(agent, kind, id string) string { return agent + "\x00" + kind + "\x00" + id }

// routeOwners is each agent's reply ids and conversation ids (agent, NUL,
// kind, NUL, the id) and the session of that agent each was read from
// (sessions' ResponseIDs and Convs); "" for an id two sessions share, which
// stays as it is. A fork's session id is kept with the one it was forked
// from (ownerFork).
func routeOwners(list []sessions.Session) map[string]string {
	of := map[string]string{}
	put := func(k, id string) {
		if was, ok := of[k]; ok && was != id {
			of[k] = ""
		} else if !ok {
			of[k] = id
		}
	}
	for _, s := range list {
		for _, c := range s.Convs {
			put(ownerKey(s.Agent, ownerConv, c), s.ID)
		}
		for _, r := range s.ResponseIDs {
			put(ownerKey(s.Agent, ownerResponse, r), s.ID)
		}
		if s.ForkOf != "" {
			of[ownerKey(s.Agent, ownerFork, s.ID)] = s.ForkOf
		}
	}
	return of
}

// compacted says a prompt is a compaction of the one before it: under 60%
// of a prompt of some size.
func compacted(prev, next int) bool { return prev >= 20000 && next < prev*6/10 }

// ctxKey is the agent's session a route is in, "" when it names none.
func ctxKey(r *ctxRec) string {
	if key := sessionOfRec(r); key != "" {
		return r.Agent + "\x00" + key
	}
	return ""
}

// withTools says a prompt carries tool definitions.
func withTools(p *gateway.Prompt) bool { return partTokens(p, gateway.PartTools) > 0 }

// toolSessions are the sessions whose own turns carry tool definitions.
// In one of them a request without any is a call the agent makes on the
// side under the conversation's session id — Claude Code's auto mode
// classifier ("You are a security monitor…", a transcript and no tools),
// its title and its quota check — not a turn of the conversation, whose
// prompt can't stand for it: a classifier's 40K after a 100K turn is no
// compaction (Player on Discord: 45 in a session that never compacted).
func toolSessions(routes []ctxRec) map[string]bool {
	out := map[string]bool{}
	for i := range routes {
		if r := &routes[i]; r.Kind == "" && r.Sub == "" && r.Tools {
			if sk := ctxKey(r); sk != "" {
				out[sk] = true
			}
		}
	}
	return out
}

// contextOf works the agents and their sessions out of their routes.
func contextOf(routes []gateway.Route, days int) ctxJSON {
	recs := make([]ctxRec, len(routes))
	for i, r := range routes {
		recs[i] = recOf(r, true)
	}
	return contextOfRecs(recs, days)
}

// contextOfRecs is contextOf of the routes as the tab reads them.
func contextOfRecs(routes []ctxRec, days int) ctxJSON {
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Time.Before(routes[j].Time) })
	out := ctxJSON{Days: days, Agents: []ctxAgent{}, Sessions: []ctxSession{}}
	type agentAcc struct {
		a                       ctxAgent
		prompts, bases, growths []int
		mcps                    []int
		cached, counted         int
		parts                   map[string]int
		models                  map[string]int
		sessions                map[string]bool
		fills                   []float64
	}
	agents := map[string]*agentAcc{}
	sess := map[string]*ctxSession{}
	var listed []sessions.Session
	list := func() []sessions.Session {
		if listed == nil {
			listed = sessions.List(0)
		}
		return listed
	}
	joinConvs(routes, list)
	// routes are the tab's copies, so the history kept (ctxKept) keeps
	// each route's own agent
	scripts := map[string]int{}
	for i := range routes {
		if a := routes[i].Agent; a == "other" || usage.IsLibrary(a) {
			if a != "other" {
				scripts[a]++
			}
			routes[i].Agent = scriptsAgent
		}
	}
	tooled := toolSessions(routes)
	var order []string
	for i := range routes {
		r := &routes[i]
		if !r.Done || r.Agent == "" {
			continue
		}
		acc := agents[r.Agent]
		if acc == nil {
			acc = &agentAcc{a: ctxAgent{Agent: r.Agent}, parts: map[string]int{}, models: map[string]int{}, sessions: map[string]bool{}}
			agents[r.Agent] = acc
		}
		acc.a.Calls++
		if r.Failed {
			acc.a.Errors++
			continue
		}
		if !r.Asked || r.Kind != "" || !r.Tools && tooled[ctxKey(r)] {
			// a title or another side call is neither the conversation's
			// nor what the agent's prompts are like
			continue
		}
		window := r.Window
		if window <= 0 {
			window = defaultWindow
		}
		acc.a.Requests++
		acc.prompts = append(acc.prompts, r.Tokens)
		acc.bases = append(acc.bases, partTokens(&gateway.Prompt{Parts: r.Parts}, gateway.PartSystem, gateway.PartTools, gateway.PartSkills, gateway.PartMemory))
		acc.mcps = append(acc.mcps, r.MCP)
		for _, x := range r.Parts {
			acc.parts[x.Kind] += x.Tokens
		}
		if r.Counted && r.CacheKnown {
			acc.counted += r.Tokens
			acc.cached += min(r.Cache, r.Tokens)
		}
		acc.models[r.Model]++
		if r.Time.After(acc.a.LatestTime) {
			acc.a.LatestTime, acc.a.LatestID = r.Time, r.ID
		}
		acc.a.Window = max(acc.a.Window, window)
		key := sessionOfRec(r)
		if key == "" || r.Sub != "" {
			// a subagent's prompt is its own conversation's, not the
			// one whose session id it goes under
			continue
		}
		acc.sessions[key] = true
		sk := r.Agent + "\x00" + key
		s := sess[sk]
		if s == nil {
			s = &ctxSession{Agent: r.Agent, Key: key, First: r.Time, Points: []ctxPoint{}}
			sess[sk] = s
			order = append(order, sk)
		}
		if n := len(s.Points); n > 0 {
			prev := s.Points[n-1].Tokens
			if compacted(prev, r.Tokens) {
				s.Compacts++
				acc.a.Compacts++
			} else if d := r.Tokens - prev; d > 0 {
				acc.growths = append(acc.growths, d)
			}
		}
		s.Points = append(s.Points, ctxPoint{ID: r.ID, Time: r.Time, Tokens: r.Tokens, Window: r.Window, Cache: r.Cache, CacheUnknown: !r.CacheKnown})
		s.Requests++
		s.Last, s.Latest, s.LatestID, s.Model = r.Time, r.Prompt, r.ID, r.Model
		s.Peak = max(s.Peak, r.Tokens)
		s.Window = max(s.Window, window)
	}
	// the sessions, the latest first
	slices.SortFunc(order, func(a, b string) int { return sess[b].Last.Compare(sess[a].Last) })
	var codexIDs, desktopIDs, piIDs []string
	for _, sk := range order {
		s := sess[sk]
		if acc := agents[s.Agent]; acc != nil && s.Requests > 1 {
			acc.fills = append(acc.fills, float64(s.Peak)/float64(s.Window))
		}
		if len(out.Sessions) < ctxSessions {
			if len(s.Points) > ctxPoints {
				s.Points = s.Points[len(s.Points)-ctxPoints:]
			}
			out.Sessions = append(out.Sessions, *s)
			switch s.Agent {
			case "codex":
				codexIDs = append(codexIDs, s.Key)
			case "claude-desktop":
				desktopIDs = append(desktopIDs, s.Key)
			case "pi-desktop", "pi":
				piIDs = append(piIDs, s.Key)
			}
		}
	}
	if len(codexIDs) > 0 {
		titles := sessions.CodexTitles(codexIDs)
		for i := range out.Sessions {
			if out.Sessions[i].Agent == "codex" {
				out.Sessions[i].Title = titles[out.Sessions[i].Key]
			}
		}
	}
	// Claude Desktop's go by the name Desktop saved, which a session with
	// no transcript on this computer has too (#1675)
	if len(desktopIDs) > 0 {
		titles := sessions.DesktopTitles(desktopIDs)
		for i := range out.Sessions {
			if out.Sessions[i].Agent == "claude-desktop" {
				out.Sessions[i].Title = titles[out.Sessions[i].Key]
			}
		}
	}
	// PI-Desktop's by the name it keeps in pi.sqlite, which no file of the
	// chat has (#1967); Pi's own sessions find none there and go on below
	if len(piIDs) > 0 {
		titles := sessions.PIDesktopTitles(piIDs)
		for i := range out.Sessions {
			if a := out.Sessions[i].Agent; (a == "pi-desktop" || a == "pi") && out.Sessions[i].Title == "" {
				out.Sessions[i].Title = titles[out.Sessions[i].Key]
			}
		}
	}
	// every other agent's sessions go by the name the Sessions page shows
	untitled := map[string]int{}
	for i, s := range out.Sessions {
		if s.Title == "" {
			untitled[s.Agent+"\x00"+s.Key] = i
		}
	}
	if len(untitled) > 0 {
		for _, s := range list() {
			if i, ok := untitled[s.Agent+"\x00"+s.ID]; ok && s.Title != "" {
				out.Sessions[i].Title = s.Title
			}
		}
	}
	for _, acc := range agents {
		a := &acc.a
		if a.Requests == 0 {
			continue
		}
		a.Sessions = len(acc.sessions)
		a.Median, a.P90 = median(acc.prompts), quantile(acc.prompts, 0.9)
		a.Baseline, a.Growth, a.MCP = median(acc.bases), median(acc.growths), median(acc.mcps)
		if acc.counted > 0 {
			a.Cache = float64(acc.cached) / float64(acc.counted)
		}
		for _, f := range acc.fills {
			a.PeakFill = math.Max(a.PeakFill, f)
		}
		total := 0
		for _, n := range acc.parts {
			total += n
		}
		a.Shares = map[string]float64{}
		for k, n := range acc.parts {
			a.Shares[k] = float64(n) / float64(max(1, total))
		}
		for m := range acc.models {
			a.Models = append(a.Models, m)
		}
		slices.SortFunc(a.Models, func(x, y string) int { return acc.models[y] - acc.models[x] })
		if len(a.Models) > 3 {
			a.Models = a.Models[:3]
		}
		if a.Agent == scriptsAgent {
			a.Clients = slices.SortedFunc(maps.Keys(scripts), func(x, y string) int {
				if scripts[x] != scripts[y] {
					return scripts[y] - scripts[x]
				}
				return strings.Compare(x, y)
			})
		}
		scoreAgent(a, acc.counted > 0)
		out.Agents = append(out.Agents, *a)
	}
	slices.SortFunc(out.Agents, func(x, y ctxAgent) int {
		if x.Requests != y.Requests {
			return y.Requests - x.Requests
		}
		return y.LatestTime.Compare(x.LatestTime)
	})
	return out
}

// scoreAgent scores an agent's use of its context out of 100, and tags it:
//   - cache, 35: the share of its prompts its vendor read from the cache,
//     full at 90%
//   - lean, 25: what every request starts with (system prompt, tools,
//     memory) against the window, full at 5% or less, none at 30%
//   - pace, 20: how much a request adds to the one before it, full at 1%
//     of the window or less, none at 6%
//   - reliable, 20: its requests that didn't fail, none at 20% failing
func scoreAgent(a *ctxAgent, counted bool) {
	w := float64(max(a.Window, 1))
	cache := 0.5 // not known: neither good nor bad
	if counted {
		cache = clamp01(a.Cache / 0.9)
	}
	lean := clamp01(1 - (float64(a.Baseline)/w-0.05)/0.25)
	pace := clamp01(1 - (float64(a.Growth)/w-0.01)/0.05)
	errRate := float64(a.Errors) / float64(max(1, a.Calls))
	reliable := clamp01(1 - errRate/0.2)
	a.Scores = []ctxScore{
		{Key: "cache", Points: math.Round(cache * 35), Most: 35},
		{Key: "lean", Points: math.Round(lean * 25), Most: 25},
		{Key: "pace", Points: math.Round(pace * 20), Most: 20},
		{Key: "reliable", Points: math.Round(reliable * 20), Most: 20},
	}
	score := 0.0
	for _, s := range a.Scores {
		score += s.Points
	}
	a.Score = int(score)

	tag := func(key, tone string, v float64) { a.Tags = append(a.Tags, ctxTag{Key: key, Tone: tone, Value: v}) }
	a.Tags = []ctxTag{}
	switch {
	case counted && a.Cache >= 0.8:
		tag("cache-friendly", "good", a.Cache)
	case counted && a.Cache < 0.4 && a.Requests >= 5:
		tag("cache-misses", "warn", a.Cache)
	}
	switch base := float64(a.Baseline) / w; {
	case base <= 0.06:
		tag("lean", "good", float64(a.Baseline))
	case base >= 0.2:
		tag("heavy-start", "warn", float64(a.Baseline))
	}
	if a.MCP >= 5000 && float64(a.MCP) >= 0.3*float64(a.Baseline) {
		tag("mcp-heavy", "info", float64(a.MCP))
	}
	if s := a.Shares[gateway.PartResults]; s >= 0.3 {
		tag("tool-heavy", "info", s)
	}
	if s := a.Shares[gateway.PartFiles]; s >= 0.25 {
		tag("file-reader", "info", s)
	}
	if s := a.Shares[gateway.PartMemory]; s >= 0.1 {
		tag("memory-heavy", "info", s)
	}
	if g := float64(a.Growth) / w; g >= 0.03 {
		tag("fills-fast", "warn", float64(a.Growth))
	}
	if a.PeakFill >= 0.85 {
		tag("near-full", "warn", a.PeakFill)
	}
	if a.Compacts > 0 {
		tag("compacts", "info", float64(a.Compacts))
	}
	errRate = float64(a.Errors) / float64(max(1, a.Calls))
	switch {
	case a.Calls >= 20 && errRate < 0.01:
		tag("steady", "good", errRate)
	case a.Calls >= 5 && errRate >= 0.1:
		tag("flaky", "warn", errRate)
	}
}

// ctxKept is what the tab has read of the routing history: each day's
// routes as it reads them, by the size and time its files had, and its
// last answer by what it was worked out of. The tab asks at every refresh
// of the Usage page (5s by default); a day whose files are as they were
// isn't read again, and an answer whose routes are the same is given
// again. A day is its latest routes at most (gateway.History), so what is
// kept doesn't grow with the history.
var ctxKept = struct {
	sync.Mutex
	days map[string]ctxDay
	key  string
	at   time.Time
	out  ctxJSON
}{days: map[string]ctxDay{}}

type ctxDay struct {
	key  string
	recs []ctxRec
}

// ctxAgain is how long an answer stands while nothing it was worked out of
// changed: the sessions' titles and the models' names it has are read
// again after it.
const ctxAgain = time.Minute

// readingDay, in tests, is told each day the tab reads from the history.
var readingDay func(day string)

// dayStamp is what a day's files are now: their sizes and times.
func dayStamp(day string) string {
	var b strings.Builder
	base := filepath.Join(gateway.HistoryDir(), day+".jsonl")
	for _, p := range []string{base, base + ".gz"} {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%d/%d;", fi.Size(), fi.ModTime().UnixNano())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// contextRecs reads the routes of the days asked for: the history kept,
// and the live trace's not yet in it. stamp is what they were read from.
func contextRecs(r *http.Request, days int) (out []ctxRec, stamp string) {
	ctxKept.Lock()
	defer ctxKept.Unlock()
	return contextRecsLocked(r, days)
}

func contextRecsLocked(r *http.Request, days int) (out []ctxRec, stamp string) {
	seen := map[int64]bool{}
	now := time.Now()
	var key strings.Builder
	asked := map[string]bool{}
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		asked[day] = true
		st := dayStamp(day)
		fmt.Fprintf(&key, "%s=%s", day, st)
		d, ok := ctxKept.days[day]
		if !ok || d.key != st {
			if readingDay != nil {
				readingDay(day)
			}
			_, rs, _ := gateway.History(day)
			d = ctxDay{key: st, recs: keepLatest(rs)}
			ctxKept.days[day] = d
		}
		for _, x := range d.recs {
			if !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	// a day out of the range is let go; the range's days are kept for the
	// next refresh
	for day := range ctxKept.days {
		if !asked[day] {
			delete(ctxKept.days, day)
		}
	}
	if gw := served.Load(); gw != nil {
		for _, x := range gw.Trace(r.Context(), 0, 0).Routes {
			if x.Done && !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, recOf(x, true))
				fmt.Fprintf(&key, ",%d", x.ID)
			}
		}
	}
	// a group magpie found by the id agents pick it by (publicRoute)
	pub := publicRefs()
	for i := range out {
		out[i].Model = pub(out[i].Model)
	}
	return out, key.String()
}

// contextFor is the tab's answer for the days asked for: the last one
// again when its routes are the same and it isn't ctxAgain old.
func contextFor(r *http.Request, days int) ctxJSON {
	ctxKept.Lock()
	defer ctxKept.Unlock()
	recs, stamp := contextRecsLocked(r, days)
	stamp = strconv.Itoa(days) + "|" + stamp
	if stamp == ctxKept.key && time.Since(ctxKept.at) < ctxAgain {
		return ctxKept.out
	}
	out := contextOfRecs(recs, days)
	ctxKept.key, ctxKept.at, ctxKept.out = stamp, time.Now(), out
	return out
}

func contextRoutesAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/context", func(rw http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 || days > 30 {
			days = 7
		}
		writeJSON(rw, contextFor(r, days))
	})
	// the text of a part of a request's prompt (masaka on Discord): the
	// part's items, and one item's text when item is given. From the
	// gateway this GUI runs, which keeps the last requests' masked bodies
	// in memory only, else from the request archive; 404 with why when
	// neither has it.
	mux.HandleFunc("GET /api/context/text", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(rw, "invalid route id", http.StatusBadRequest)
			return
		}
		item := -1
		if v := q.Get("item"); v != "" {
			if item, err = strconv.Atoi(v); err != nil || item < 0 {
				http.Error(rw, "invalid item", http.StatusBadRequest)
				return
			}
		}
		var pt gateway.PromptText
		ok := false
		if gw := served.Load(); gw != nil {
			pt, ok = gw.PromptText(id, q.Get("kind"), item >= 0)
		}
		if !ok {
			// gone from memory: the request archive's copy, when it
			// kept one (context_archive.go)
			if pt, ok = archivedPromptText(rw, r, id, q.Get("kind"), item >= 0); !ok {
				return
			}
		}
		if item >= 0 {
			// the one item's text; the rest by name and size only
			if item >= len(pt.Items) {
				http.Error(rw, "invalid item", http.StatusBadRequest)
				return
			}
			for i := range pt.Items {
				if i != item {
					pt.Items[i].Pieces = nil
				}
			}
			if pt.Items[item].Pieces == nil {
				pt.Items[item].Pieces = []gateway.PromptPiece{}
			}
		}
		rw.Header().Set("Cache-Control", "no-store")
		writeJSON(rw, pt)
	})
	contextReplyAPI(mux)
}
