package gateway

// GET /v1/magpie/route: where a session's turn went, for an agent's UI to
// show while the reply is still on its way (#405). Routing decides the
// member, and a group's decision model the effort, before the vendor is
// asked; each fallback is in the trace as it happens. This tells one
// session's latest route from the trace, nothing the trace doesn't hold.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// routeWaitMax caps how long one ask waits for the route to change.
const routeWaitMax = 60 * time.Second

// SessionRoute is a session's latest request as routing has it so far.
type SessionRoute struct {
	ID          int64        `json:"id"`
	Time        time.Time    `json:"time"`
	Kind        string       `json:"kind,omitempty"`         // a side call's: a title, a subagent…
	Asked       string       `json:"asked"`                  // the model as the agent asked
	AskedEffort string       `json:"asked_effort,omitempty"` // the reasoning it asked for
	Group       string       `json:"group,omitempty"`        // the routing group's id, when it asked one
	Rule        *RuleHit     `json:"rule,omitempty"`         // the group's rules, as the Routing page has them
	Nested      []NestedRule `json:"nested,omitempty"`
	// Model and Effort: the try under way, or the last, as provider/model
	// and the reasoning it was sent at
	Model  string     `json:"model,omitempty"`
	Effort string     `json:"effort,omitempty"`
	Served string     `json:"served,omitempty"` // the model the reply says answered
	Tries  []RouteTry `json:"tries"`
	Done   bool       `json:"done"`
	Status int        `json:"status,omitempty"`
	Error  string     `json:"error,omitempty"`
	Millis int64      `json:"ms,omitempty"`
	TTFT   int64      `json:"ttft_ms,omitempty"`
}

// RouteTry is one member trying the request: each failed one was fallen
// over from.
type RouteTry struct {
	Model  string `json:"model"` // provider/model
	Effort string `json:"effort,omitempty"`
	Done   bool   `json:"done"`
	Status int    `json:"status,omitempty"`
	Fail   string `json:"fail,omitempty"` // why it failed, as the trace tells it
	Millis int64  `json:"ms,omitempty"`
}

func sessionRouteOf(r Route) SessionRoute {
	out := SessionRoute{ID: r.ID, Time: r.Time, Kind: r.Kind, Asked: r.Model, AskedEffort: r.Effort, Rule: r.Rule, Nested: r.Nested,
		Served: r.Served, Tries: []RouteTry{}, Done: r.Done, Status: r.Status, Error: r.Error, Millis: r.Millis, TTFT: r.TTFT}
	if r.Group != nil {
		out.Group = r.Group.ID
	}
	// a try names its seat, whose provider is in the order weighed
	providerOf := func(id string) string {
		for _, ws := range [][]Weighed{r.Order, r.Left} {
			for _, w := range ws {
				if w.ID == id {
					return w.Provider
				}
			}
		}
		return r.Provider
	}
	for _, t := range r.Tries {
		model := t.Model
		if p := providerOf(t.ID); p != "" && model != "" {
			model = p + "/" + model
		}
		out.Tries = append(out.Tries, RouteTry{Model: model, Effort: t.Effort, Done: t.Done, Status: t.Status, Fail: t.Fail, Millis: t.Millis})
	}
	if n := len(out.Tries); n > 0 {
		out.Model, out.Effort = out.Tries[n-1].Model, out.Tries[n-1].Effort
	}
	return out
}

// sessionRoute answers GET /v1/magpie/route?session=<id>, or with the
// session in X-Magpie-Session (or the agent's own session header): the
// session's latest route, null before it has one. With after=<seq> and
// wait=<seconds> it waits, up to a minute, for the route to change past
// seq. Like the quotas, it answers this machine, and another only with the
// shared gateway's key.
func (s *Server) sessionRoute(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's routes are told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	q := r.URL.Query()
	session := strings.TrimSpace(q.Get("session"))
	if len(session) > 128 {
		session = session[:128] // as sessionOf keeps it
	}
	if session == "" {
		session = sessionOf(r.Header)
	}
	if session == "" {
		writeError(w, provider.Chat, http.StatusBadRequest, "name the session: ?session=<id> or the "+SessionHeader+" header")
		return
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	secs, _ := strconv.ParseFloat(q.Get("wait"), 64)
	wait := min(time.Duration(secs*float64(time.Second)), routeWaitMax)
	seq, route := s.trace.sessionLatest(r.Context(), session, after, wait)
	out := map[string]any{"session": session, "seq": seq, "route": nil}
	if route != nil {
		out["route"] = sessionRouteOf(*route)
	}
	writeJSON(w, 200, out)
}

// sessionLatest is a copy of session's latest route, and its seq; with
// wait, it waits up to that long for it to change past after.
func (t *trace) sessionLatest(ctx context.Context, session string, after int64, wait time.Duration) (int64, *Route) {
	deadline := time.NewTimer(max(wait, 0))
	defer deadline.Stop()
	for {
		t.mu.Lock()
		var seq int64
		var found *Route
		for i := len(t.routes) - 1; i >= 0; i-- {
			if r := t.routes[i]; r.Session == session {
				c := *r
				c.Order = append([]Weighed{}, r.Order...) // [] for none: the GUI reads it as a list
				c.Left = append([]Weighed(nil), r.Left...)
				c.Tries = append([]Try{}, r.Tries...)
				seq, found = r.Seq, &c
				break
			}
		}
		// after past the trace's count: the gateway started over since
		if wait <= 0 || seq > after || t.seq < after {
			t.mu.Unlock()
			return seq, found
		}
		if t.wake == nil {
			t.wake = make(chan struct{})
		}
		wake := t.wake
		t.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			return seq, found
		case <-ctx.Done():
			return seq, found
		}
	}
}
