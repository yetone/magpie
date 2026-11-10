package middleware

// A gateway module may serve two more of the gateway's points besides its
// hooks (P4 of docs/subsystems/plugin-runtime.md):
//
//   - strategies: routings a provider or group can be routed by, each a
//     function (pool, seats, options) → the order to try the seats in, or
//     an object whose order is that function. Each is registered on the
//     router strategy point (internal/routes) as "<plugin>/<name>" while
//     the plugin is on and loaded. It runs on the request's path, so it has
//     strategyLimit and fails open: a throw, a timeout or an order that
//     isn't a permutation of the seats leaves them in their listed order,
//     counted as a failure.
//   - onUsage(row, options): each ledger row, as an allow-listed copy
//     (usageRow: no bodies, prompts, errors' text or sessions). It runs off
//     the request's path, from a queue that drops what doesn't fit,
//     counted, and never holds a request up.
//
// Both run in one runtime of the module's own, used by one call at a time,
// so a strategy can read what onUsage kept (the latency it saw, say).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moejs"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/internal/usage"
)

var (
	// strategyLimit is how long a strategy may take, waiting for the
	// module's runtime included, before its pool is tried in listed order
	strategyLimit = 20 * time.Millisecond
	// usageLimit is how long onUsage may take with one row
	usageLimit = 250 * time.Millisecond
	// usageQueue is how many rows wait for onUsage before more are dropped
	usageQueue = 1024
)

var strategyName = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// strat is one strategy a module exports.
type strat struct {
	name string
	h    moejs.Hook
}

// points is the runtime a module's strategies and onUsage share, one call
// at a time: sem holds it.
type points struct {
	in  *inst
	sem chan struct{}
}

// discover finds the strategies and onUsage the module in exports, and
// gives the module a runtime for them when it has either.
func (m *mw) discover(in *inst) error {
	m.onUse, m.hasUse = in.hook("onUsage")
	names, err := in.keys("strategies")
	if err != nil {
		return err
	}
	for _, n := range names {
		if !strategyName.MatchString(n) {
			return fmt.Errorf("strategy %q: a name is letters, digits, '.', '_' and '-'", n)
		}
		h, ok := in.member(n)
		if !ok {
			return fmt.Errorf("strategy %q is neither a function nor an object with an order function", n)
		}
		m.strats = append(m.strats, strat{n, h})
	}
	if len(m.strats) == 0 && !m.hasUse {
		return nil
	}
	pt, err := m.newInst()
	if err != nil {
		return err
	}
	m.pt = &points{in: pt, sem: make(chan struct{}, 1)}
	return nil
}

// keys are the names of the export name's object, or default's member
// name's; none when there is no such export.
func (in *inst) keys(name string) ([]string, error) {
	v, ok := in.rt.Export(name)
	if !ok || v.IsUndefined() {
		d, ok := in.rt.Export("default")
		if !ok || !d.IsObject() {
			return nil, nil
		}
		if v, _ = in.rt.Get(d, name); v.IsUndefined() {
			return nil, nil
		}
	}
	if !v.IsObject() {
		return nil, fmt.Errorf("%s isn't an object of strategies by name", name)
	}
	g, err := in.rt.ToGo(v)
	if err != nil {
		return nil, err
	}
	obj, ok := g.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s isn't an object of strategies by name", name)
	}
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// member is strategy n's function: strategies.n, or strategies.n.order, as
// an export or default's member.
func (in *inst) member(n string) (moejs.Hook, bool) {
	for _, path := range [][]string{{"strategies", n}, {"strategies", n, "order"}} {
		for _, export := range []string{"", "default"} {
			p := path
			if export != "" {
				p = append([]string{export}, path...)
			}
			h, err := in.m.mod.Hook(p[0], p[1:]...)
			if err != nil {
				continue
			}
			if ok, _ := in.rt.Has(h); ok {
				return h, true
			}
		}
	}
	return moejs.Hook{}, false
}

// take holds the module's points runtime, waiting at most wait (forever
// when 0); false when it couldn't.
func (p *points) take(wait time.Duration) bool {
	if wait <= 0 {
		p.sem <- struct{}{}
		return true
	}
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (p *points) give() {
	p.in.rt.ReleaseCallData()
	<-p.sem
}

// routing is what a provider or group names strategy n of m by:
// "<plugin>/<n>", the plugin as plugin.ShortName shortens it; a file of
// its own is named without its extensions.
func (m *mw) routing(n string) string {
	short := plugin.ShortName(m.name)
	if plugin.IsPath(m.spec) {
		short = filepath.Base(m.spec)
		for _, ext := range []string{".js", ".mjs", ".cjs", ".middleware", ".gateway"} {
			short = strings.TrimSuffix(short, ext)
		}
	}
	// what a routing may hold (routes.FromPlugin): "my plugin" is "my-plugin"
	short = strings.Trim(badPrefix.ReplaceAllString(short, "-"), "-")
	if short == "" {
		short = "plugin"
	}
	return short + "/" + n
}

var badPrefix = lazyre.New(`[^A-Za-z0-9._-]+`)

// seatJSON is a routes.Seat as a strategy is given it.
type seatJSON struct {
	Key     string  `json:"key"`
	Account bool    `json:"account"`
	Known   bool    `json:"known"`
	Learns  bool    `json:"learns"`
	Used    float64 `json:"used"`
	Soon    []int64 `json:"soon"` // unix ms
	Pace    float64 `json:"pace"`
	Weight  int     `json:"weight"`
	Resting bool    `json:"resting"`
	Tokens  float64 `json:"tokens"`
}

var errBusy = errors.New("its runtime was busy past the time a strategy has")

// strategy is s of m as the router's strategy point takes it.
func (m *mw) strategy(s strat) routes.Strategy {
	return routes.Strategy{Order: func(pool string, seats []routes.Seat) []int {
		order, err := m.order(s.h, pool, seats)
		if err != nil {
			m.fail("strategies."+s.name, err)
			order = make([]int, len(seats))
			for i := range order {
				order[i] = i
			}
		}
		return order
	}}
}

// order asks h for seats' order, checked to be a permutation of them.
func (m *mw) order(h moejs.Hook, pool string, seats []routes.Seat) ([]int, error) {
	start := time.Now()
	list := make([]seatJSON, len(seats))
	for i, s := range seats {
		soon := make([]int64, len(s.Soon))
		for j, t := range s.Soon {
			soon[j] = t.UnixMilli()
		}
		list[i] = seatJSON{s.Key, s.Account, s.Known, s.Learns, s.Used, soon, s.Pace, s.Weight, s.Resting, s.Tokens}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	p := m.pt
	if !p.take(strategyLimit) {
		return nil, errBusy
	}
	defer p.give()
	rt := p.in.rt
	poolV, err := rt.FromGo(pool)
	if err != nil {
		return nil, err
	}
	seatsV, err := rt.ParseJSON(b)
	if err != nil {
		return nil, err
	}
	optsV, err := rt.ParseJSON(m.options)
	if err != nil {
		return nil, err
	}
	v, err := p.in.call(h, strategyLimit-time.Since(start), poolV, seatsV, optsV)
	m.orders.Add(1)
	if err != nil {
		return nil, err
	}
	var list2 []*float64
	out, err := rt.AppendJSON(nil, v)
	if err != nil || json.Unmarshal(out, &list2) != nil || len(list2) != len(seats) {
		return nil, fmt.Errorf("returned %s, not an order of its %d seats", p.in.text(v), len(seats))
	}
	order, seen := make([]int, len(seats)), make([]bool, len(seats))
	for i, x := range list2 {
		if x == nil || *x != math.Trunc(*x) || *x < 0 || int(*x) >= len(seats) || seen[int(*x)] {
			return nil, fmt.Errorf("returned %s, not an order of its %d seats", p.in.text(v), len(seats))
		}
		order[i], seen[int(*x)] = int(*x), true
	}
	return order, nil
}

// usageRow is what onUsage is given of a ledger row: who asked, whom, for
// which model, how much and how it went. Never a body, a prompt, an
// error's text, a session or a request id.
type usageRow struct {
	Time            int64    `json:"time"` // unix ms
	Operation       string   `json:"operation,omitempty"`
	Agent           string   `json:"agent"`
	Provider        string   `json:"provider"`
	ProviderAccount string   `json:"providerAccount,omitempty"`
	ProviderKeyID   string   `json:"providerKeyId,omitempty"`
	ProviderKeyName string   `json:"providerKeyName,omitempty"`
	CallerKeyID     string   `json:"callerKeyId,omitempty"`
	CallerKeyName   string   `json:"callerKeyName,omitempty"`
	Model           string   `json:"model"`
	Requested       string   `json:"requested,omitempty"`
	Served          string   `json:"served,omitempty"`
	Input           int      `json:"input"`
	Output          int      `json:"output"`
	CacheRead       int      `json:"cacheRead"`
	CacheWrite      int      `json:"cacheWrite"`
	Reasoning       int      `json:"reasoning"`
	Ms              int64    `json:"ms"`
	TTFTMs          int64    `json:"ttftMs"`
	Status          int      `json:"status"`
	ErrType         string   `json:"errType,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	Rejected        bool     `json:"rejected"`
	Tries           int      `json:"tries,omitempty"`
	Cost            *float64 `json:"cost"` // USD; null when no price is known
}

func rowOf(r usage.Record, cost func(usage.Record) (float64, bool)) usageRow {
	row := usageRow{
		Time: r.Time.UnixMilli(), Operation: r.Operation, Agent: r.Agent, Provider: r.Provider,
		ProviderAccount: r.ProviderAccount, ProviderKeyID: r.ProviderKeyID, ProviderKeyName: r.ProviderKeyName,
		CallerKeyID: r.CallerKeyID, CallerKeyName: r.CallerKeyName,
		Model: r.Model, Requested: r.Requested, Served: r.Served,
		Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, Reasoning: r.Reasoning,
		Ms: r.Millis, TTFTMs: r.TTFT, Status: r.Status, ErrType: r.ErrType, Kind: r.Kind,
		Rejected: r.Rejected, Tries: r.Tries,
	}
	switch {
	case r.OwnCost != nil:
		c := *r.OwnCost
		row.Cost = &c
	case cost != nil:
		if c, ok := cost(r); ok {
			row.Cost = &c
		}
	}
	return row
}

// use gives onUsage one row.
func (m *mw) use(row []byte) {
	p := m.pt
	p.take(0)
	defer p.give()
	rt := p.in.rt
	rowV, err := rt.ParseJSON(row)
	if err == nil {
		var optsV moejs.Value
		if optsV, err = rt.ParseJSON(m.options); err == nil {
			_, err = p.in.call(m.onUse, usageLimit, rowV, optsV)
		}
	}
	m.rows.Add(1)
	if err != nil {
		m.fail("onUsage", err)
	}
}

// Points is the module's strategies and onUsage on their points, as one
// usage.Observer: plugins/middleware registers it while it is on. Only the
// process serving the gateway serves them (Serve); a strategy is
// registered while its plugin is on and loaded, and onUsage is given each
// row from then.
func Points() usage.Observer { return &observer{} }

type observer struct {
	mu      sync.Mutex
	queue   chan usage.Record
	using   atomic.Bool // the set served has an onUsage
	serving *set
	removes []func()
}

func (o *observer) Bodies() usage.Bodies            { return usage.NoBodies }
func (o *observer) Begin(usage.Request) usage.Watch { return nil }

// Ledger queues r for onUsage, and drops it when the queue is full: it
// runs inside the ledger's write, so it never waits.
func (o *observer) Ledger(r usage.Record) {
	if !o.using.Load() {
		return
	}
	o.mu.Lock()
	q, s := o.queue, o.serving
	o.mu.Unlock()
	if q == nil || s == nil {
		return
	}
	select {
	case q <- r:
	default:
		for _, m := range s.list {
			if m.hasUse {
				m.dropped.Add(1)
			}
		}
		if s.droppedSaid.Add(1)%1000 == 1 {
			log.Printf("middleware: onUsage is behind; %d rows dropped", s.droppedSaid.Load())
		}
	}
}

// Serve registers the strategies of the modules loaded and keeps them as
// the modules load again, and gives onUsage its rows, until stop.
func (o *observer) Serve() (stop func()) {
	q := make(chan usage.Record, usageQueue)
	done := make(chan struct{})
	var wg sync.WaitGroup
	o.mu.Lock()
	o.queue = q
	o.mu.Unlock()
	written := filememo.Written() // before the look, so a write made during it isn't missed
	o.refresh()
	wg.Add(2)
	go func() {
		defer wg.Done()
		// a plugin turned on here is seen once saved (Written); every
		// OthersEvery only catches one the CLI or a second magpie turned on
		// (#3185: a 1s tick woke an idle magpie every second)
		for {
			select {
			case <-done:
				return
			case <-written:
			case <-beat.Next(filememo.OthersEvery()):
			}
			written = filememo.Written()
			o.refresh()
		}
	}()
	go func() {
		defer wg.Done()
		var cost func(usage.Record) (float64, bool)
		var priced time.Time
		for {
			select {
			case <-done:
				return
			case r := <-q:
				o.mu.Lock()
				s := o.serving
				o.mu.Unlock()
				if s == nil {
					continue
				}
				if time.Since(priced) > 5*time.Minute {
					cost, priced = usage.NewCoster(), time.Now()
				}
				b, err := json.Marshal(rowOf(r, cost))
				if err != nil {
					continue
				}
				for _, m := range s.list {
					if m.hasUse {
						m.use(b)
					}
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
			o.mu.Lock()
			o.queue = nil
			o.swap(nil)
			o.mu.Unlock()
		})
	}
}

// refresh serves the set in force now, when it isn't served yet.
func (o *observer) refresh() {
	s := current()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.queue == nil || s == o.serving {
		return
	}
	o.swap(s)
}

// swap takes the strategies served away and registers s's; o.mu is held.
func (o *observer) swap(s *set) {
	for _, r := range o.removes {
		r()
	}
	o.removes, o.serving = nil, s
	using := false
	if s != nil {
		for _, m := range s.list {
			for _, st := range m.strats {
				o.removes = append(o.removes, routes.Register(m.routing(st.name), m.strategy(st)))
			}
			using = using || m.hasUse
		}
	}
	o.using.Store(using)
}

// Routing is a strategy a gateway module serves, as the routing pickers
// list it.
type Routing struct {
	ID     string `json:"id"`     // what a provider or group is routed by
	Name   string `json:"name"`   // the strategy's own name
	Plugin string `json:"plugin"` // the plugin it is of
}

// Routings are the strategies of the modules loaded that this process's
// router serves now: none while plugins/middleware is off or the gateway
// isn't served here.
func Routings() []Routing {
	out := []Routing{}
	for _, m := range current().list {
		for _, st := range m.strats {
			id := m.routing(st.name)
			if routes.Served(id) {
				out = append(out, Routing{ID: id, Name: st.name, Plugin: m.name})
			}
		}
	}
	return out
}
