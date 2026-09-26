package gateway

// The routing trace: what routing did with each request, as it did it, for
// the Gateway view to play back. Every account or key in the order it was
// weighed and what put it there — the share of its allowance used and when
// that renews, the tokens it served lately, whose turn it was, why it
// rests — then each try, what it answered, and how long one that failed
// now sits out. It's recorded where the decisions are made, from the same
// values they are made with; nothing is worked out again afterwards.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// traceKeep is how many requests the trace keeps.
const traceKeep = 60

// Route is one request's way through routing.
type Route struct {
	Seq      int64     `json:"seq"` // the trace's count when it last changed
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Agent    string    `json:"agent"`
	Model    string    `json:"model"`           // as the agent asked
	Provider string    `json:"provider"`        // the provider the model resolved to
	Group    *GroupRef `json:"group,omitempty"` // the routing group the agent asked for
	Rule     *RuleHit  `json:"rule,omitempty"`  // the group's rules for it, when it has any
	// Nested: the rules of the groups in the group, down the way to the
	// one that went first, each as it decided
	Nested   []NestedRule `json:"nested,omitempty"`
	Affinity *Affinity    `json:"affinity,omitempty"` // its conversation, and whether it stayed put
	Order    []Weighed    `json:"order"`              // who was to try it, first first
	Left     []Weighed    `json:"left,omitempty"`
	Tries    []Try        `json:"tries"`
	Done     bool         `json:"done"`
	Status   int          `json:"status,omitempty"`
	Error    string       `json:"error,omitempty"`
	Millis   int64        `json:"ms,omitempty"`
	Tokens   int          `json:"tokens,omitempty"`
}

// GroupRef is the routing group a request asked for.
type GroupRef struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Auto     bool     `json:"auto,omitempty"`
	Members  []string `json:"members"` // those ready, as provider/model
	// Subs: the groups in the group, at any depth, outermost first
	Subs []SubGroup `json:"subs,omitempty"`
	// Via: for each of Members, the groups in the group it is of, as
	// "fast>cheap" ("" for the group's own), when it has groups in it
	Via []string `json:"via,omitempty"`
}

// SubGroup is a routing group in the group a request asked for.
type SubGroup struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Routing string `json:"routing"`
	In      string `json:"in"`              // the group it is in
	Rules   int    `json:"rules,omitempty"` // how many rules it has
}

// NestedRule is a group in the group's rules, as they decided.
type NestedRule struct {
	Group string   `json:"group"`
	Name  string   `json:"name"`
	Rule  *RuleHit `json:"rule"`
}

// groupRef is the trace's g, with its models ms.
func groupRef(g provider.Group, ms []provider.Member) *GroupRef {
	ref := &GroupRef{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
	seen := map[string]bool{}
	for _, m := range ms {
		ref.Members = append(ref.Members, m.Provider.ID+"/"+m.Model)
		ref.Via = append(ref.Via, strings.Join(m.Groups(), ">"))
		in := g.ID
		for _, v := range m.Via {
			if !seen[v.ID] {
				seen[v.ID] = true
				ref.Subs = append(ref.Subs, SubGroup{ID: v.ID, Name: v.Name, Routing: v.Routing, In: in, Rules: len(v.Rules)})
			}
			in = v.ID
		}
	}
	if len(ref.Subs) == 0 {
		ref.Via = nil
	}
	return ref
}

// Weighed is one account or key as routing weighed it.
type Weighed struct {
	ID       string            `json:"id"` // what rests after a failure
	Provider string            `json:"provider"`
	Name     string            `json:"name"` // the provider's
	Icon     string            `json:"icon,omitempty"`
	Preset   string            `json:"preset,omitempty"`
	Who      string            `json:"who,omitempty"` // the account, or the key's name or its masked self
	Kind     string            `json:"kind"`          // "account", "key", or "provider" when it has one
	Agent    string            `json:"agent,omitempty"`
	Plan     string            `json:"plan,omitempty"`
	Model    string            `json:"model"`
	Routing  string            `json:"routing"` // its provider's: "", order, rotate, usage
	Fallback bool              `json:"fallback,omitempty"`
	Shared   bool              `json:"shared,omitempty"` // its provider has more than one on
	Known    bool              `json:"known,omitempty"`  // the vendor said what the account has left
	Used     float64           `json:"used"`             // share of the allowance counting the model, used
	Renews   []time.Time       `json:"renews,omitempty"` // when those windows renew, the biggest first
	Tokens   float64           `json:"tokens,omitempty"` // least used: tokens it served lately
	Turn     bool              `json:"turn,omitempty"`   // in turn: it was this one's turn
	Fit      int               `json:"fit,omitempty"`    // keyFit
	Speaks   provider.Protocol `json:"speaks,omitempty"` // a key made for one protocol only
	Rest     *Rest             `json:"rest,omitempty"`   // resting after a failure, when the request came
	Unlisted bool              `json:"unlisted,omitempty"`
	// Aside: a key made for another protocol than the keys routed over,
	// tried only after them
	Aside bool `json:"aside,omitempty"`
	// Via: the groups in the group it is of, outermost first, when it is
	// of a group in the group asked for
	Via []string `json:"via,omitempty"`
}

// Try is one candidate trying the request.
type Try struct {
	ID     string    `json:"id"`
	Model  string    `json:"model,omitempty"`  // the model it was asked for: a group's members may share a provider's keys
	Effort string    `json:"effort,omitempty"` // the reasoning it was asked for in place of the agent's, as its model takes the turn's pick
	Start  time.Time `json:"start"`
	Done   bool      `json:"done"`
	Status int       `json:"status,omitempty"`
	Millis int64     `json:"ms,omitempty"`
	Fail   string    `json:"fail,omitempty"` // why it failed, as rest tells it
	Error  string    `json:"error,omitempty"`
	Rest   *Rest     `json:"rest,omitempty"`  // how long it now sits out; none when it was the last to try
	Again  int64     `json:"again,omitempty"` // ms waited before it was tried again, the last one left
}

type planned struct {
	order, left []Weighed
}

func weighed(c candidate, p provider.Provider, wg weighing, fallback bool, from provider.Protocol) Weighed {
	w := Weighed{ID: c.rest, Provider: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Model: c.model,
		Routing: p.Routing, Fallback: fallback, Shared: c.rest != p.ID}
	switch {
	case c.p.Account != nil:
		w.Kind, w.Who, w.Agent, w.Plan = "account", c.p.Account.User, c.p.Account.Agent, c.p.Account.Plan
	case c.rest != p.ID:
		w.Kind, w.Who = "key", c.p.KeyName
		if w.Who == "" {
			w.Who = provider.Mask(c.p.Key)
		}
		w.Fit, w.Speaks = keyFit(c.p, c.model, from), c.p.KeyProtocol
	default:
		w.Kind = "provider"
	}
	if l, ok := wg.lefts[c.rest]; ok {
		w.Known, w.Used, w.Renews = true, l.used, l.renews
	}
	if wg.tokens != nil {
		w.Tokens = wg.tokens[c.rest]
	}
	return w
}

// trace keeps the last requests' routes, and wakes those waiting for more.
type trace struct {
	mu     sync.Mutex
	seq    int64
	ids    int64
	routes []*Route
	wake   chan struct{}
	totals Totals
}

// Totals count the requests routed since the gateway started.
type Totals struct {
	Requests int `json:"requests"` // answered, one way or the other
	Rerouted int `json:"rerouted"` // tries that failed and handed the request on
	Errors   int `json:"errors"`   // requests whose agent got an error
}

// TraceState is the routes changed since a seq, and the totals.
type TraceState struct {
	Seq    int64   `json:"seq"`
	Routes []Route `json:"routes"`
	Totals Totals  `json:"totals"`
}

func (t *trace) changed() {
	t.seq++
	if t.wake != nil {
		close(t.wake)
		t.wake = nil
	}
}

func (t *trace) begin(r Route) *Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ids++
	r.ID = t.ids
	if r.Tries == nil {
		r.Tries = []Try{}
	}
	rp := &r
	t.routes = append(t.routes, rp)
	if len(t.routes) > traceKeep {
		t.routes = t.routes[len(t.routes)-traceKeep:]
	}
	t.changed()
	rp.Seq = t.seq
	return rp
}

// update changes a route under the lock.
func (t *trace) update(r *Route, f func(r *Route)) {
	if r == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	done := r.Done
	f(r)
	if r.Done && !done {
		t.totals.Requests++
		for _, try := range r.Tries {
			if try.Rest != nil {
				t.totals.Rerouted++
			}
		}
		if r.Status >= 400 {
			t.totals.Errors++
		}
	}
	t.changed()
	r.Seq = t.seq
}

// Trace is the routes changed after seq, oldest first. With none, it waits
// up to wait for one.
func (s *Server) Trace(ctx context.Context, after int64, wait time.Duration) TraceState {
	t := &s.trace
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		if t.seq < after {
			after = 0 // the gateway started over since
		}
		st := TraceState{Seq: t.seq, Routes: []Route{}, Totals: t.totals}
		for _, r := range t.routes {
			if r.Seq > after {
				c := *r
				c.Order = append([]Weighed(nil), r.Order...)
				c.Left = append([]Weighed(nil), r.Left...)
				c.Tries = append([]Try{}, r.Tries...)
				st.Routes = append(st.Routes, c)
			}
		}
		if len(st.Routes) > 0 || wait <= 0 {
			t.mu.Unlock()
			return st
		}
		if t.wake == nil {
			t.wake = make(chan struct{})
		}
		wake := t.wake
		t.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			return st
		case <-ctx.Done():
			return st
		}
	}
}
