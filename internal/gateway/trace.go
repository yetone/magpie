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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// traceKeep is how many requests the trace keeps.
const traceKeep = 60

// Route is one request's way through routing.
type Route struct {
	imageTurn     string
	imageCaller   string
	imageProvider string
	Seq           int64        `json:"seq"` // the trace's count when it last changed
	ID            int64        `json:"id"`
	Time          time.Time    `json:"time"`
	Agent         string       `json:"agent"`
	ParentSession string       `json:"parentSession,omitempty"` // title helper's explicit originating chat; does not affect routing
	Session       string       `json:"session,omitempty"`       // the client's session id, never inferred from its model or account
	TitleLink     *TitleLink   `json:"titleLink,omitempty"`     // digests only; display evidence, never account affinity
	ParentMatched bool         `json:"parentMatched,omitempty"` // parent inferred for this view, not supplied by Codex
	Usage         []RouteUsage `json:"usage,omitempty"`         // token tiers of billable tries; priced when read
	Kind          string       `json:"kind,omitempty"`          // what the call is for, as Call's
	For           *CallFor     `json:"for,omitempty"`           // the request it was made for, as Call's
	Model         string       `json:"model"`                   // as the agent asked
	Effort        string       `json:"effort,omitempty"`        // the reasoning the agent asked for; "" for none
	Provider      string       `json:"provider"`                // the provider the model resolved to
	Group         *GroupRef    `json:"group,omitempty"`         // the routing group the agent asked for
	Rule          *RuleHit     `json:"rule,omitempty"`          // the group's rules for it, when it has any
	SealedTask    bool         `json:"sealedTask,omitempty"`    // only ChatGPT accounts can read this subagent's task
	LeadAccount   string       `json:"leadAccount,omitempty"`   // the parent account put first for a sealed task
	// Nested: the rules of the groups in the group, down the way to the
	// one that went first, each as it decided
	Nested   []NestedRule `json:"nested,omitempty"`
	Affinity *Affinity    `json:"affinity,omitempty"` // its conversation, and whether it stayed put
	Pinned   string       `json:"pinned,omitempty"`   // the account AccountHeader named: only it was tried
	Order    []Weighed    `json:"order"`              // who was to try it, first first
	Left     []Weighed    `json:"left,omitempty"`
	Tries    []Try        `json:"tries"`
	Done     bool         `json:"done"`
	Status   int          `json:"status,omitempty"`
	Error    string       `json:"error,omitempty"`
	Millis   int64        `json:"ms,omitempty"`
	Tokens   int          `json:"tokens,omitempty"`
	Output   int          `json:"out,omitempty"` // of Tokens, the reply's
	// TTFT: ms from the request to its reply's first content (text,
	// reasoning or a tool call), FirstText to its first text, as Millis
	// counts: streamed replies only (#196)
	TTFT      int64 `json:"ttft,omitempty"`
	FirstText int64 `json:"firstText,omitempty"`
	// Served: the model the reply says answered, as the last try has it;
	// Swapped: another than the one that try asked for; Routed: that try
	// asked another magpie's routing group, and Served is its member
	Served  string `json:"served,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
	Routed  bool   `json:"routed,omitempty"`
	// Upstream: the provider an aggregator said answered behind it
	Upstream string `json:"upstream,omitempty"`
}

// RouteUsage is one billable attempt's pricing inputs, kept in routing history.
// Its JSON keys also read the earlier history that stored full usage records.
type RouteUsage struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Input      int    `json:"in"`
	Output     int    `json:"out"`
	CacheRead  int    `json:"cache_read,omitempty"`
	CacheWrite int    `json:"cache_write,omitempty"`
	Reasoning  int    `json:"reasoning,omitempty"`
}

// PricingRecord lets the routing view reuse the ledger's effective prices.
func (u RouteUsage) PricingRecord() usage.Record {
	return usage.Record{Provider: u.Provider, Model: u.Model, Input: u.Input,
		Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning}
}

// GroupRef is the routing group a request asked for.
type GroupRef struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Auto     bool     `json:"auto,omitempty"`
	Sink     bool     `json:"sink,omitempty"` // provider.Group.Sink, as it applies
	Members  []string `json:"members"`        // those ready, as provider/model[:effort fixed on it]
	// Subs: the groups in the group, at any depth, outermost first
	Subs []SubGroup `json:"subs,omitempty"`
	// Via: for each of Members, the groups in the group it is of, as
	// "fast>cheap" ("" for the group's own), when it has groups in it
	Via []string `json:"via,omitempty"`
	// Fast: those of Members sent in their vendor's fast mode
	Fast []string `json:"fast,omitempty"`
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
	ref := &GroupRef{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto, Sink: sinks(g.Sink, g.Routing)}
	seen := map[string]bool{}
	for _, m := range ms {
		ref.Members = append(ref.Members, provider.WithMemberEffort(m.Provider.ID+"/"+m.Model, m.Effort))
		if m.Fast {
			ref.Fast = append(ref.Fast, ref.Members[len(ref.Members)-1])
		}
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
	ID       string  `json:"id"` // what rests after a failure
	Provider string  `json:"provider"`
	Name     string  `json:"name"` // the provider's
	Icon     string  `json:"icon,omitempty"`
	Preset   string  `json:"preset,omitempty"`
	Who      string  `json:"who,omitempty"` // the account, or the key's name or its masked self
	Kind     string  `json:"kind"`          // "account", "key", or "provider" when it has one
	Agent    string  `json:"agent,omitempty"`
	Plan     string  `json:"plan,omitempty"`
	Model    string  `json:"model"`
	Fixed    string  `json:"fixed,omitempty"` // the effort the group's member it is of is fixed at
	Fast     bool    `json:"fast,omitempty"`  // the group's member it is of is sent fast
	Routing  string  `json:"routing"`         // its provider's: "", order, rotate, usage
	Fallback bool    `json:"fallback,omitempty"`
	Shared   bool    `json:"shared,omitempty"` // its provider has more than one on
	Known    bool    `json:"known,omitempty"`  // the vendor said what the account has left
	Learns   bool    `json:"learns,omitempty"` // not known, but its answer will tell
	Used     float64 `json:"used"`             // share of the allowance counting the model, used
	// Amount of Limit in Unit: the count that share is of, when the
	// vendor counts it so (WorkBuddy's credits, #659)
	Amount   float64           `json:"amount,omitempty"`
	Limit    float64           `json:"limit,omitempty"`
	Unit     string            `json:"unit,omitempty"`
	Renews   []time.Time       `json:"renews,omitempty"` // when those windows renew, the biggest first
	Pace     float64           `json:"pace,omitempty"`   // remaining allowance per hour until its window resets
	Due      *time.Time        `json:"due,omitempty"`    // when the allowance window used for pace resets
	Tokens   float64           `json:"tokens,omitempty"` // least used: tokens it served lately
	Turn     bool              `json:"turn,omitempty"`   // in turn: it was this one's turn
	Fit      int               `json:"fit,omitempty"`    // keyFit
	Speaks   provider.Protocol `json:"speaks,omitempty"` // a key made for one protocol only
	Rest     *Rest             `json:"rest,omitempty"`   // resting after a failure, when the request came
	Unlisted bool              `json:"unlisted,omitempty"`
	// DueBy: "reset" when Due is when an auto-used Codex reset about to
	// run out starts the windows again, sooner than they renew (#717)
	DueBy string `json:"dueBy,omitempty"`
	// Restarts: when that reset starts them again, set when it is sooner
	// than its biggest window renews — Smart goes by it then (#718)
	Restarts *time.Time `json:"restarts,omitempty"`
	// Barred: left out as the user set it not to serve the model, its
	// own list of models leaving it out (#474)
	Barred bool `json:"barred,omitempty"`
	// Held: left out as the gateway key asking may not use its account or
	// key (#905)
	Held bool `json:"held,omitempty"`
	// Capped: left out as held at the usage cap the user set on the
	// account, this cap in percent; Used is then its fullest window's
	// share, CapBack when the last window at or past it renews
	Capped  int        `json:"capped,omitempty"`
	CapBack *time.Time `json:"capBack,omitempty"`
	// NoCredits: held at 100% (Capped), a Codex account the user set not
	// to spend its credits once its allowance is used up
	NoCredits bool `json:"noCredits,omitempty"`
	// Rank: its place in its provider's own list of accounts or keys, the
	// order the provider's page shows and a drag sets (#217); routing may
	// weigh them in another
	Rank int `json:"rank,omitempty"`
	// Aside: a key made for another protocol than the keys routed over,
	// tried only after them
	Aside bool `json:"aside,omitempty"`
	// Via: the groups in the group it is of, outermost first, when it is
	// of a group in the group asked for
	Via []string `json:"via,omitempty"`
	// Sunk: when it was rate limited with quota left, sending it to the
	// back of an order that sinks (sink.go); nil when it didn't, or the
	// order doesn't sink
	Sunk *time.Time `json:"sunk,omitempty"`
}

// Try is one candidate trying the request.
type Try struct {
	ID     string `json:"id"`
	Model  string `json:"model,omitempty"`  // the model it was asked for: a group's members may share a provider's keys
	Effort string `json:"effort,omitempty"` // the reasoning it was sent at, fitted to its model's levels; "" for none
	Picked bool   `json:"picked,omitempty"` // Effort is the turn's pick, in place of the agent's
	// Fixed: the effort the group's member it went to is fixed at, which
	// Effort is (fitted to the model's levels) whatever was asked
	Fixed  string    `json:"fixed,omitempty"`
	Fast   bool      `json:"fast,omitempty"` // sent in its vendor's fast mode, as the group's member it went to is
	Start  time.Time `json:"start"`
	Done   bool      `json:"done"`
	Status int       `json:"status,omitempty"`
	Millis int64     `json:"ms,omitempty"`
	// TTFT: ms from Start to its reply's first content, FirstText to its
	// first text, when it streamed any (#196)
	TTFT      int64 `json:"ttft,omitempty"`
	FirstText int64 `json:"firstText,omitempty"`
	// Served: the model its reply said answered, when it named one;
	// Swapped: another model than Model, not just its dated name; Routed:
	// Model is another magpie's routing group, and Served the member it
	// routed to (usage.GroupRouted)
	Served  string `json:"served,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
	Routed  bool   `json:"routed,omitempty"`
	// Upstream: the provider an aggregator said answered behind it
	// (OpenRouter's DeepInfra, Novita …)
	Upstream string `json:"upstream,omitempty"`
	Fail     string `json:"fail,omitempty"` // why it failed, as rest tells it
	Error    string `json:"error,omitempty"`
	Rest     *Rest  `json:"rest,omitempty"`  // how long it now sits out; none when it was the last to try
	Again    int64  `json:"again,omitempty"` // ms waited before it was tried again, the last one left
	// Queued: ms it waited for one of its key's or account's slots, the
	// provider's MaxConcurrency out already (concurrency.go)
	Queued int64 `json:"queued,omitempty"`
	// Reset: its week used up and nobody else left, one of the account's
	// Codex resets was spent by itself (the user's setting) — on Who, and
	// what spending it did — and the request asked again
	Reset *AutoReset `json:"reset,omitempty"`
	// Auto: the models Copilot's Auto picked for it, in turn — where each
	// pick came from and Copilot's refusal of it — Model being "auto"
	Auto []provider.AutoPick `json:"auto,omitempty"`
}

// AutoReset is a Codex or Claude reset spent by itself, on Who's account.
type AutoReset struct {
	Who   string `json:"who"`
	Text  string `json:"text"`
	Agent string `json:"agent,omitempty"` // "claude" for a Claude account's; Codex's otherwise
}

type planned struct {
	order, left []Weighed
	// held: the accounts left out (in left) as they won't spend their
	// credits, for one that spends its resets by itself to spend one
	held []candidate
}

func weighed(c candidate, p provider.Provider, wg weighing, fallback bool, from provider.Protocol) Weighed {
	w := Weighed{ID: c.rest, Provider: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Model: c.model, Fixed: c.effort, Fast: c.fast,
		Routing: p.Routing, Fallback: fallback, Shared: c.rest != p.ID, Rank: c.rank}
	switch {
	case c.p.Account != nil:
		w.Kind, w.Who, w.Agent, w.Plan = "account", c.p.Account.User, c.p.Account.Agent, c.p.Account.Plan
		if w.Agent == "plugin" {
			// a plugin's account is told as its provider's: a moved Grok's
			// plan reads as the built-in's did
			w.Agent = c.p.ID
		}
	case c.rest != p.ID:
		w.Kind, w.Who = "key", c.p.KeyName
		if w.Who == "" {
			w.Who = provider.Mask(c.p.Key)
		}
		w.Fit, w.Speaks = keyFit(c.p, c.model, from), c.p.KeyProtocol
	default:
		w.Kind = "provider"
	}
	if l, ok := wg.lefts[c.allowanceKey()]; ok {
		w.Known, w.Used, w.Renews, w.Pace = true, l.used, l.renews, l.pace
		if l.of > 0 {
			w.Amount, w.Limit, w.Unit = l.amount, l.of, l.unit
		}
		if !l.due.IsZero() {
			due := l.due
			w.Due = &due
			if l.dueRestart {
				w.DueBy = "reset"
			}
		}
		if rs := l.restarts; !rs.IsZero() && len(l.soon) > 0 && rs.Equal(l.soon[0]) {
			w.Restarts = &rs
		}
	} else if wg.lefts != nil {
		w.Learns = learns(c, wg.lefts)
	}
	if wg.tokens != nil {
		w.Tokens = wg.tokens[c.rest]
	}
	if sinks(p.Sink, p.Routing) {
		markSunk(&w, c)
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
	if t.ids == 0 {
		// ids go on from one run to the next, the history keeping them
		// all: counted on from the time the gateway began
		t.ids = time.Now().UnixMilli()
	}
	t.ids++
	r.ID = t.ids
	if r.Tries == nil {
		r.Tries = []Try{}
	}
	rp := &r
	t.routes = append(t.routes, rp)
	t.trim()
	t.changed()
	rp.Seq = t.seq
	return rp
}

// trim keeps traceKeep routes, the oldest finished ones going first: a
// request still going isn't dropped for those that came after it (#436),
// as the history has only finished ones, unless more than twice traceKeep
// are going at once.
func (t *trace) trim() {
	for len(t.routes) > traceKeep {
		i := slices.IndexFunc(t.routes, func(r *Route) bool { return r.Done })
		if i < 0 {
			if len(t.routes) <= 2*traceKeep {
				return
			}
			i = 0
		}
		t.routes = slices.Delete(t.routes, i, i+1)
	}
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
		for i, try := range r.Tries {
			if try.Rest != nil && i < len(r.Tries)-1 { // the last rests with nobody after it (failVerify)
				t.totals.Rerouted++
			}
		}
		if r.Status >= 400 || r.Error != "" {
			t.totals.Errors++
		}
		if keepRoutes {
			c := *r
			c.Order = append([]Weighed{}, r.Order...) // [] for none: the GUI reads it as a list
			c.Left = append([]Weighed(nil), r.Left...)
			c.Tries = append([]Try{}, r.Tries...)
			c.Usage = append([]RouteUsage(nil), r.Usage...)
			go saveRoute(c)
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
				c.Order = append([]Weighed{}, r.Order...) // [] for none: the GUI reads it as a list
				c.Left = append([]Weighed(nil), r.Left...)
				c.Tries = append([]Try{}, r.Tries...)
				c.Usage = append([]RouteUsage(nil), r.Usage...)
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

// swapped reports whether served is another model than sent: not the same
// name, however dated, pinned or prefixed (usage.Swapped).
func swapped(sent, served string) bool { return usage.Swapped(sent, served) }

// routeUsage retains only what pricing needs, without another copy of the
// request's metadata. Unknown token counts have no price, including failures.
func routeUsage(id, model string, u Usage) []RouteUsage {
	if u.Input+u.Output == 0 {
		return nil
	}
	return []RouteUsage{{Provider: id, Model: model, Input: u.Input, Output: u.Output,
		CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning}}
}
