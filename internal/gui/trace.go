package gui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/capre"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// traceJSON is the gateway's routing trace since the page last asked.
type traceJSON struct {
	gateway.TraceState
	Mine   bool        `json:"mine"` // the trace here is live: this magpie serves the gateway, or this console reads the magpie that does (#2606)
	Now    time.Time   `json:"now"`
	Routes []routeJSON `json:"routes"`
}

type routeJSON struct {
	gateway.Route
	SessionTitle string  `json:"sessionTitle,omitempty"`
	Cost         float64 `json:"cost"`
	Priced       bool    `json:"priced"`
	Unpriced     int     `json:"unpriced"`
}

// Name polling reuses the history evidence already loaded by this view. Never
// reread a day's routing log every 15 seconds just to notice a title write.
var titleHistory struct {
	sync.Mutex
	days map[string][]gateway.Route
}

func rememberTitleHistory(day string, rows []gateway.Route) {
	if day == "" {
		return
	}
	evidence := make([]gateway.Route, 0, len(rows))
	for _, r := range rows {
		if r.Agent != "codex" || (r.TitleLink == nil && r.ParentSession == "") {
			continue
		}
		failed := ""
		if r.Error != "" {
			failed = "failed"
		}
		evidence = append(evidence, gateway.Route{ID: r.ID, Agent: r.Agent, Session: r.Session,
			ParentSession: r.ParentSession, Kind: r.Kind, Time: r.Time, Millis: r.Millis,
			Done: r.Done, Status: r.Status, Error: failed, TitleLink: r.TitleLink})
	}
	titleHistory.Lock()
	defer titleHistory.Unlock()
	if titleHistory.days == nil {
		titleHistory.days = map[string][]gateway.Route{}
	}
	if _, exists := titleHistory.days[day]; !exists && len(titleHistory.days) >= 4 {
		for key := range titleHistory.days {
			delete(titleHistory.days, key)
			break
		}
	}
	titleHistory.days[day] = evidence
}

func recalledTitleHistory(day string) []gateway.Route {
	titleHistory.Lock()
	defer titleHistory.Unlock()
	return titleHistory.days[day] // immutable after publication
}

func pricedRoutes(routes []gateway.Route) []routeJSON {
	gw := served.Load()
	if gw == nil {
		gw = &gateway.Server{}
	}
	routes = gw.ResolveTitleParents(routes)
	out := make([]routeJSON, 0, len(routes))
	priceOf := usage.NewPricer()
	ids := make([]string, 0, len(routes))
	for _, r := range routes {
		if r.Agent == "codex" {
			id := r.Session
			if r.ParentSession != "" {
				id = r.ParentSession
			}
			ids = append(ids, id)
		}
	}
	titles := sessions.CodexTitles(ids)
	var loose []string
	need, now := false, time.Now()
	for _, r := range routes {
		need = need || looseRoute(r)
		loose = append(loose, looseKeys(r, now)...)
	}
	owners := routeSessions(need, loose)
	pub := publicRefs()
	for _, r := range routes {
		r = publicRoute(r, pub)
		if looseRoute(r) {
			if id := ownerOfRoute(r, owners); id != "" {
				r.Session = id
			}
		}
		r.LastConvs = nil // the page names no session by them
		r.Usage = cacheCounted(r.Usage)
		recs := make([]usage.Record, len(r.Usage))
		for i, u := range r.Usage {
			recs[i] = u.PricingRecord()
		}
		sum := priceOf(recs)
		// Old history and calls without token counts stay unknown, not free.
		priced := sum.Calls > sum.Unpriced && sum.Input+sum.Output > 0
		name := ""
		if r.Agent == "codex" {
			id := r.Session
			if r.ParentSession != "" {
				id = r.ParentSession
			}
			name = titles[id]
		}
		out = append(out, routeJSON{Route: r, SessionTitle: name, Cost: sum.Cost, Priced: priced, Unpriced: sum.Unpriced})
	}
	return out
}

// convAgents are the agents whose own session files say which of the
// gateway's reply and conversation ids are theirs (sessions' ResponseIDs
// and Convs): dsh's, whose chat-completions and messages routes send no
// session id (changqiu4890 on Discord: Routing put every dsh request under
// "No session ID").
var convAgents = map[string]bool{"dsh": true}

// looseKeys are the agent and ids (ownerKey's) a route of such an agent
// that names no session is to be found by, and whose absence from the
// listing means it is behind: its conversation's, and its reply's while
// dsh may still be writing the reply down — up to convsKept after the
// route ended. A reply dsh keeps no id of (a title call's, a refusal's)
// is never listed, and the page polls every second or two: past that it
// would list every agent's sessions again each convsFresh. None for any
// other route.
func looseKeys(r gateway.Route, now time.Time) []string {
	if !looseRoute(r) {
		return nil
	}
	var out []string
	if r.ResponseID != "" && r.Done && now.Sub(r.Time.Add(time.Duration(r.Millis)*time.Millisecond)) < convsKept {
		out = append(out, ownerKey(r.Agent, ownerResponse, r.ResponseID))
	}
	if r.Conv != "" {
		out = append(out, ownerKey(r.Agent, ownerConv, r.Conv))
	}
	// the messages its turn carried, while it is still answering: a
	// session's file gains them before the request goes out, and a listing
	// read before that has no key to name it by (Route.LastConvs). Only
	// for convsKept from its start, as a reply's: a message no file keeps
	// as sent (an image, a rewritten one) would list every agent's
	// sessions again each convsFresh for as long as it answers
	if !r.Done && now.Sub(r.Time) < convsKept {
		for _, c := range r.LastConvs {
			out = append(out, ownerKey(r.Agent, ownerLast, c))
		}
	}
	return out
}

// looseRoute says r is a route of such an agent that names no session.
func looseRoute(r gateway.Route) bool {
	return r.Session == "" && r.ParentSession == "" && convAgents[r.Agent] && (r.ResponseID != "" || r.Conv != "")
}

// routeConvs is routeOwners of the sessions listed, for every trace the
// Routing page polls: listing the sessions reads every agent's folder, so
// it is made again only when it is convsKept old, or convsFresh old and a
// route's id isn't in it (a reply or a session begun since).
var routeConvs struct {
	sync.Mutex
	at time.Time
	of map[string]string // replaced, never changed: read outside the lock
}

var convsFresh, convsKept = 2 * time.Second, time.Minute

// routeSessions is routeOwners for the routes of a page that has a route
// that names no session (need), nil for one with none, so it lists
// nothing; keys are looseKeys'.
func routeSessions(need bool, keys []string) map[string]string {
	if !need {
		return nil
	}
	routeConvs.Lock()
	defer routeConvs.Unlock()
	age := time.Since(routeConvs.at)
	stale := routeConvs.of == nil || age >= convsKept
	if !stale && age >= convsFresh {
		for _, k := range keys {
			if _, ok := routeConvs.of[k]; !ok {
				stale = true
				break
			}
		}
	}
	if stale {
		routeConvs.of, routeConvs.at = routeOwners(sessions.List(0)), time.Now()
	}
	return routeConvs.of
}

// cacheCounted marks the usage of a vendor that counts no cache as such,
// kept before its plugin said so (usage.CountsNoCache): its hit rate is
// none rather than 0% (#3464). us is copied before it is changed.
func cacheCounted(us []gateway.RouteUsage) []gateway.RouteUsage {
	var out []gateway.RouteUsage
	for i, u := range us {
		if u.CacheUnknown || !cacheNotCounted(u) {
			continue
		}
		if out == nil {
			out = slices.Clone(us)
		}
		out[i].CacheUnknown = true
	}
	if out == nil {
		return us
	}
	return out
}

// cacheNotCounted is whether u's vendor counted no cache for it: it said
// so, or it never does and none was recorded.
func cacheNotCounted(u gateway.RouteUsage) bool {
	return u.CacheUnknown || u.CacheRead+u.CacheWrite == 0 && usage.CountsNoCache(u.Provider)
}

// publicRefs is provider.PublicRef, each ref looked up once: a page of
// routes names a few models many times over.
func publicRefs() func(string) string {
	seen := map[string]string{}
	return func(ref string) string {
		if ref == "" {
			return ref
		}
		out, ok := seen[ref]
		if !ok {
			out = provider.PublicRef(ref)
			seen[ref] = out
		}
		return out
	}
}

// publicRoute is r as the Routing page names it: a group magpie found by
// the id agents pick it by, auto/<model>, wherever r names one — the
// model asked for, the request a search or an image was for, a subagent's
// pick, the group and the groups in it (未寒 on Discord: the live view
// still said group/auto-<model>). The gateway keeps group/auto-<slug> in
// the trace and its history (provider.CanonicalRef); r's own pointers are
// the trace's, so what changes is copied first.
func publicRoute(r gateway.Route, pub func(string) string) gateway.Route {
	r.Model = pub(r.Model)
	if r.For != nil {
		f := *r.For
		f.Model = pub(f.Model)
		r.For = &f
	}
	if r.Subagent != nil {
		s := *r.Subagent
		s.Asked, s.To = pub(s.Asked), pub(s.To)
		r.Subagent = &s
	}
	if g := r.Group; g != nil {
		c := *g
		if c.Ref == "" { // history written before the trace kept it
			c.Ref = pub(provider.GroupPrefix + c.ID)
		}
		c.Subs = slices.Clone(c.Subs)
		for i, sub := range c.Subs {
			if sub.Ref == "" {
				c.Subs[i].Ref = pub(provider.GroupPrefix + sub.ID)
			}
		}
		r.Group = &c
	}
	return r
}

// mainView is the tab the window is asked to open on, as the page's view
// parameter: the Routing page on one request (req, its id) when the tray
// panel's Routing tab asks for it, Masking on one request's record (key)
// when a masked call's badge there does, or the Usage page's Requests on one
// provider or agent when its Usage tab does, its Overview (the allowances)
// when a menu-bar cell's click does, or Plugins' Discover, looking for what
// the panel's add sheet found nothing by (q), when its More in Plugins
// does. Anything but an id, a name of the kind a provider or an agent has,
// or a line of text to look for, is dropped.
func mainView(q url.Values) string {
	view := q.Get("view")
	switch view {
	case "plugins":
		// q, even empty, says Discover: without it the page opens on the
		// tab visited last (#3579)
		if f, ok := q["q"]; ok {
			s := ""
			if mainFind.MatchString(f[0]) {
				s = f[0]
			}
			view += "&q=" + url.QueryEscape(s)
		}
	case "routing":
		if id, err := strconv.ParseInt(q.Get("req"), 10, 64); err == nil && id > 0 {
			view += "&req=" + strconv.FormatInt(id, 10)
		}
	case "masking":
		if key, err := strconv.ParseInt(q.Get("key"), 10, 64); err == nil && key > 0 {
			view += "&key=" + strconv.FormatInt(key, 10)
		}
	case "usage":
		if tab := q.Get("tab"); tab == "requests" || tab == "usage" {
			view += "&tab=" + tab
		}
		if card := q.Get("card"); q.Get("tab") == "usage" && card != "" {
			view += "&card=" + url.QueryEscape(card)
		}
		for _, k := range []string{"provider", "agent"} {
			if v := q.Get(k); mainName.MatchString(v) {
				view += "&" + k + "=" + url.QueryEscape(v)
			}
		}
	}
	// the panel's picker making a routing group of a model kept for groups
	// and in none: the window opens on a new group of it, on the Routing
	// groups page (routing, from a build before it, still does)
	if m := q.Get("newgroup"); (view == "routing" || view == "groups") && m != "" {
		view += "&newgroup=" + url.QueryEscape(m)
	}
	return view
}

// mainName is the id of a provider or an agent.
var mainName = capre.MustCompile(`^[A-Za-z0-9._@:/ -]+$`, 80)

// mainFind is a line of text to look for, as a search field holds one.
var mainFind = capre.MustCompile(`^[^\x00-\x1f\x7f]*$`, 100)

// mainURL is the window's page for a view as mainView gives it, the
// request's id kept as its own parameter (the panel's link to one request,
// which escaping the whole of it lost).
func mainURL(view, query string) string { return "/?view=" + view + query }

// argView is a tab named on the command line (`magpie gui settings`, a
// restart to update) as ShowMain takes it: a name, never parameters.
func argView(s string) string { return url.QueryEscape(s) }

// quotaView opens the Usage overview at a provider/account card, or at
// the period of one of magpie's own totals (trayTotal). Invalid provider
// names fall back to the overview without a target.
func quotaView(id string) string {
	view := "usage&tab=usage"
	if p, ok := trayTotalPeriod(id); ok {
		// magpie's own total: the Requests tab at its period, which sums
		// what the total does (trayTotalSum)
		return "usage&tab=requests&period=" + string(p)
	}
	provider, _, hasAccount := strings.Cut(id, "|")
	if mainName.MatchString(provider) {
		view += "&provider=" + url.QueryEscape(provider)
		if hasAccount {
			view += "&card=" + url.QueryEscape(id)
		}
	}
	return view
}

// withGatewayTitles fills in, for each agent:id key the agent's own files
// left without a title, the first words its user said through the gateway,
// as the Sessions page heads a session it knows only from there
// (flandern1211_58540 on Discord: Routing named a session by its folder and
// id). Nothing is read that the Sessions page doesn't show already.
func withGatewayTitles(out map[string]string, keys []string) map[string]string {
	var title func(agent, id string) string
	for _, k := range keys {
		agent, id, ok := strings.Cut(k, ":")
		if !ok || id == "" || out[k] != "" {
			continue
		}
		if title == nil {
			title = sessions.GatewayTitler()
		}
		// recorded under the agent its User-Agent named, as a route's
		// agent may not have been
		name := title(agent, id)
		if name == "" {
			if a := usage.AgentOf(agent); a != agent {
				name = title(a, id)
			}
		}
		if name != "" {
			out[k] = name
		}
	}
	return out
}

// codexReadableTitles is each Codex thread's name from Codex's index, else
// the title its own files give (its first prompt, as the Sessions page
// heads it), else the first words recorded through the gateway: a thread
// never named was headed by its folder and id alone.
func codexReadableTitles(ids []string) map[string]string {
	out := sessions.CodexTitles(ids)
	var keys []string
	for _, id := range ids {
		if id != "" && out[id] == "" {
			keys = append(keys, "codex:"+id)
		}
	}
	if len(keys) == 0 {
		return out
	}
	for k, name := range withGatewayTitles(sessions.Titles(keys), keys) {
		if name != "" {
			out[strings.TrimPrefix(k, "codex:")] = name
		}
	}
	return out
}

// traceRoutes serves the routing trace for the Gateway view to play: it
// waits up to 25 s for something to change after the seq it is given, so
// the page hears of a request as it happens.
func traceRoutes(mux *http.ServeMux) {
	// Names can arrive or change after a route finishes, independently of the
	// trace sequence. Read only the IDs the page currently lists.
	mux.HandleFunc("POST /api/gateway/session-titles", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs      []string `json:"ids"`
			Groups   bool     `json:"groups"`
			RouteIDs []int64  `json:"routeIds"`
			Day      string   `json:"day"`
			// Sessions are other agents' sessions as agent:id, named
			// from their own files (sessions.Titles) rather than Codex's
			// index (#1293)
			Sessions []string `json:"sessions"`
		}
		r.Body = http.MaxBytesReader(rw, r.Body, 256<<10)
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.IDs) > 2000 || len(in.RouteIDs) > 2000 || len(in.Sessions) > 2000 {
			http.Error(rw, "invalid session IDs", http.StatusBadRequest)
			return
		}
		if len(in.Sessions) > 0 && len(in.IDs) == 0 {
			writeJSON(rw, map[string]any{"titles": withGatewayTitles(sessions.Titles(in.Sessions), in.Sessions)})
			return
		}
		if !in.Groups {
			writeJSON(rw, sessions.CodexTitles(in.IDs))
			return
		}
		var rows []gateway.Route
		gw := served.Load()
		if in.Day != "" {
			if _, err := time.Parse("2006-01-02", in.Day); err != nil {
				http.Error(rw, "invalid route day", http.StatusBadRequest)
				return
			}
			rows = recalledTitleHistory(in.Day)
		} else if gw != nil {
			rows = gw.Trace(r.Context(), 0, 0).Routes
		}
		if gw == nil {
			gw = &gateway.Server{}
		}
		rows = gw.ResolveTitleParents(rows)
		wanted := make(map[int64]bool, len(in.RouteIDs))
		for _, id := range in.RouteIDs {
			wanted[id] = true
		}
		parents := map[int64]string{}
		matched := map[int64]bool{}
		for _, row := range rows {
			if !wanted[row.ID] || row.Agent != "codex" {
				continue
			}
			parents[row.ID], matched[row.ID] = row.ParentSession, row.ParentMatched
			if row.ParentSession != "" {
				in.IDs = append(in.IDs, row.ParentSession)
			}
		}
		writeJSON(rw, map[string]any{"names": codexReadableTitles(in.IDs), "parents": parents, "matched": matched,
			"resetInferred": in.Day != "" && rows == nil})
	})
	mux.HandleFunc("GET /api/gateway/route", func(rw http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(rw, "invalid route id", http.StatusBadRequest)
			return
		}
		if gw := served.Load(); gw != nil {
			for _, route := range gw.Trace(r.Context(), 0, 0).Routes {
				if route.ID == id {
					writeJSON(rw, pricedRoutes([]gateway.Route{route})[0])
					return
				}
			}
		}
		day, err := time.Parse("2006-01-02", r.URL.Query().Get("day"))
		if err != nil {
			http.Error(rw, "invalid route day", http.StatusBadRequest)
			return
		}
		if route, ok := gateway.HistoryRoute(id, day); ok {
			writeJSON(rw, pricedRoutes([]gateway.Route{route})[0])
			return
		}
		http.Error(rw, "route history is no longer available", http.StatusNotFound)
	})
	mux.HandleFunc("GET /api/gateway/trace", func(rw http.ResponseWriter, r *http.Request) {
		gw := served.Load()
		out := traceJSON{TraceState: gateway.TraceState{Routes: []gateway.Route{}}, Mine: gw != nil}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		var wait time.Duration
		if r.URL.Query().Get("wait") != "" {
			wait = 25 * time.Second
		}
		if gw != nil {
			out.TraceState = gw.Trace(r.Context(), after, wait)
		} else if st, ok := gateway.OwnerTrace(r.Context(), after, wait); ok {
			// another magpie on this computer serves the gateway: show the
			// routing it decides here too, live, and let the page name the
			// process that is routing (#2606)
			out.TraceState = st
			out.Mine = true
		}
		out.Now = time.Now()
		out.Routes = pricedRoutes(out.TraceState.Routes)
		writeJSON(rw, out)
	})
	// the routes of a day gone by, from the history the gateway keeps on
	// disk — read whichever magpie serves the gateway
	mux.HandleFunc("GET /api/gateway/history", func(rw http.ResponseWriter, r *http.Request) {
		day := r.URL.Query().Get("day")
		days, routes, cut := gateway.History(day)
		rememberTitleHistory(day, routes)
		out := map[string]any{"days": days, "routes": pricedRoutes(routes), "cut": cut}
		if day == "" {
			// the live page's accounts and keys say tried and answered of
			// the day the Usage page reads, not of the last few requests
			// the trace still keeps (#3351)
			out["tallies"] = gateway.DayTallies()
		}
		writeJSON(rw, out)
	})
	// the report saved of a request held too long before routing, for its
	// row's Copy Report: the app's log goes only to stderr, which its user
	// can't reach. A magpie serving the gateway for this one (OwnerTrace)
	// saves it in the same folder.
	mux.HandleFunc("GET /api/gateway/held/{name}", func(rw http.ResponseWriter, r *http.Request) {
		text, err := gateway.HeldReport(r.PathValue("name"))
		if err != nil {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(rw).Encode(map[string]string{"error": "This report is no longer kept: magpie keeps the last 5."})
			return
		}
		writeJSON(rw, map[string]string{"text": text})
	})
	// an account's rest lifted by hand: verified with its vendor, say
	mux.HandleFunc("POST /api/gateway/unrest", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"key"`
		}
		gw := served.Load()
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Key == "" || gw == nil {
			http.Error(rw, "nothing to lift", http.StatusBadRequest)
			return
		}
		writeJSON(rw, map[string]bool{"lifted": gw.Unrest(in.Key)})
	})
}
