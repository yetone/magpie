package gui

// The Context tab: each agent's context windows as the gateway saw them —
// what its prompts hold, how fast they fill, how much its vendor read from
// its cache — with a score and tags worked out from those, its sessions'
// fill turn by turn, and each request's prompt one click away in Routing.
// Read from the routing history (gateway.Route.Prompt), the latest
// requests from the live trace.

import (
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
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
}

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

func cacheOf(r gateway.Route) int {
	if len(r.Usage) == 0 {
		return 0
	}
	return r.Usage[len(r.Usage)-1].CacheRead
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

// compacted says a prompt is a compaction of the one before it: under 60%
// of a prompt of some size.
func compacted(prev, next int) bool { return prev >= 20000 && next < prev*6/10 }

// contextOf works the agents and their sessions out of their routes.
func contextOf(routes []gateway.Route, days int) ctxJSON {
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
	var order []string
	for _, r := range routes {
		if !r.Done || r.Agent == "" {
			continue
		}
		acc := agents[r.Agent]
		if acc == nil {
			acc = &agentAcc{a: ctxAgent{Agent: r.Agent}, parts: map[string]int{}, models: map[string]int{}, sessions: map[string]bool{}}
			agents[r.Agent] = acc
		}
		acc.a.Calls++
		if r.Status >= 400 || r.Error != "" {
			acc.a.Errors++
			continue
		}
		p := r.Prompt
		if p == nil || p.Tokens <= 0 || r.Kind != "" {
			// a title or another side call is neither the conversation's
			// nor what the agent's prompts are like
			continue
		}
		window := p.Window
		if window <= 0 {
			window = defaultWindow
		}
		acc.a.Requests++
		acc.prompts = append(acc.prompts, p.Tokens)
		acc.bases = append(acc.bases, partTokens(p, gateway.PartSystem, gateway.PartTools, gateway.PartMemory))
		acc.mcps = append(acc.mcps, mcpTokens(p))
		for _, x := range p.Parts {
			acc.parts[x.Kind] += x.Tokens
		}
		if p.Counted {
			acc.counted += p.Tokens
			acc.cached += min(cacheOf(r), p.Tokens)
		}
		acc.models[r.Model]++
		if r.Time.After(acc.a.LatestTime) {
			acc.a.LatestTime, acc.a.LatestID = r.Time, r.ID
		}
		acc.a.Window = max(acc.a.Window, window)
		key := sessionOfRoute(r)
		if key == "" {
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
			if compacted(prev, p.Tokens) {
				s.Compacts++
				acc.a.Compacts++
			} else if d := p.Tokens - prev; d > 0 {
				acc.growths = append(acc.growths, d)
			}
		}
		s.Points = append(s.Points, ctxPoint{ID: r.ID, Time: r.Time, Tokens: p.Tokens, Window: p.Window, Cache: cacheOf(r)})
		s.Requests++
		s.Last, s.Latest, s.LatestID, s.Model = r.Time, p, r.ID, r.Model
		s.Peak = max(s.Peak, p.Tokens)
		s.Window = max(s.Window, window)
	}
	// the sessions, the latest first
	slices.SortFunc(order, func(a, b string) int { return sess[b].Last.Compare(sess[a].Last) })
	var codexIDs []string
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
			if s.Agent == "codex" {
				codexIDs = append(codexIDs, s.Key)
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
	// every other agent's sessions go by the name the Sessions page shows
	untitled := map[string]int{}
	for i, s := range out.Sessions {
		if s.Title == "" {
			untitled[s.Agent+"\x00"+s.Key] = i
		}
	}
	if len(untitled) > 0 {
		for _, s := range sessions.List(0) {
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

// contextRoutes reads the routes of the days asked for: the history kept,
// and the live trace's not yet in it.
func contextRoutes(r *http.Request, days int) []gateway.Route {
	seen := map[int64]bool{}
	var out []gateway.Route
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		_, rs, _ := gateway.History(day)
		for _, x := range rs {
			if !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	if gw := served.Load(); gw != nil {
		for _, x := range gw.Trace(r.Context(), 0, 0).Routes {
			if x.Done && !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	return out
}

func contextRoutesAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/context", func(rw http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 || days > 30 {
			days = 7
		}
		writeJSON(rw, contextOf(contextRoutes(r, days), days))
	})
}
