package gui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// traceJSON is the gateway's routing trace since the page last asked.
type traceJSON struct {
	gateway.TraceState
	Mine bool      `json:"mine"` // this magpie serves the gateway: another's trace isn't here
	Now  time.Time `json:"now"`
}

// mainView is the tab the window is asked to open on, as the page's view
// parameter: the Routing page on one request (req, its id) when the tray
// panel's Routing tab asks for it, or the Usage page's Requests on one
// provider or agent when its Usage tab does. Anything but an id, or a name
// of the kind a provider or an agent has, is dropped.
func mainView(q url.Values) string {
	view := q.Get("view")
	switch view {
	case "routing":
		if id, err := strconv.ParseInt(q.Get("req"), 10, 64); err == nil && id > 0 {
			view += "&req=" + strconv.FormatInt(id, 10)
		}
	case "usage":
		if q.Get("tab") == "requests" {
			view += "&tab=requests"
		}
		for _, k := range []string{"provider", "agent"} {
			if v := q.Get(k); mainName.MatchString(v) {
				view += "&" + k + "=" + url.QueryEscape(v)
			}
		}
	}
	// the panel's picker making a routing group of a model kept for groups
	// and in none: the window opens on a new group of it
	if m := q.Get("newgroup"); view == "routing" && m != "" {
		view += "&newgroup=" + url.QueryEscape(m)
	}
	return view
}

// mainName is the id of a provider or an agent.
var mainName = regexp.MustCompile(`^[A-Za-z0-9._@:/ -]{1,80}$`)

// mainURL is the window's page for a view as mainView gives it, the
// request's id kept as its own parameter (the panel's link to one request,
// which escaping the whole of it lost).
func mainURL(view, query string) string { return "/?view=" + view + query }

// argView is a tab named on the command line (`magpie gui settings`, a
// restart to update) as ShowMain takes it: a name, never parameters.
func argView(s string) string { return url.QueryEscape(s) }

// traceRoutes serves the routing trace for the Gateway view to play: it
// waits up to 25 s for something to change after the seq it is given, so
// the page hears of a request as it happens.
func traceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/route", func(rw http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(rw, "invalid route id", http.StatusBadRequest)
			return
		}
		if gw := served.Load(); gw != nil {
			for _, route := range gw.Trace(r.Context(), 0, 0).Routes {
				if route.ID == id {
					writeJSON(rw, route)
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
			writeJSON(rw, route)
			return
		}
		http.Error(rw, "route history is no longer available", http.StatusNotFound)
	})
	mux.HandleFunc("GET /api/gateway/trace", func(rw http.ResponseWriter, r *http.Request) {
		gw := served.Load()
		out := traceJSON{TraceState: gateway.TraceState{Routes: []gateway.Route{}}, Mine: gw != nil}
		if gw != nil {
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			var wait time.Duration
			if r.URL.Query().Get("wait") != "" {
				wait = 25 * time.Second
			}
			out.TraceState = gw.Trace(r.Context(), after, wait)
		}
		out.Now = time.Now()
		writeJSON(rw, out)
	})
	// the routes of a day gone by, from the history the gateway keeps on
	// disk — read whichever magpie serves the gateway
	mux.HandleFunc("GET /api/gateway/history", func(rw http.ResponseWriter, r *http.Request) {
		days, routes, cut := gateway.History(r.URL.Query().Get("day"))
		writeJSON(rw, map[string]any{"days": days, "routes": routes, "cut": cut})
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
