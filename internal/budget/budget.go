// Package budget holds each gateway key to its own limit (#585): what a
// key has used in its window is summed from usage.jsonl, the records the
// Usage page shows, so it survives a restart; a request is let in only
// while the key has something left after what its requests in flight are
// expected to use, and those are settled with what the call recorded.
//
// The rules:
//   - A call counts in the window it started in (its record's time).
//     Windows are calendar ones in local time, or cycles of N days from
//     when the limit was set (access.Limit.Window). A reset (#1509)
//     starts the window again from then: what came before it doesn't
//     count, tokens and cost alike.
//   - Tokens are a call's uncached input, output and cache writes, and
//     its cache reads when the limit says so; cost is an estimate at the
//     Usage page's prices, and a call with no known price adds none.
//   - A request refused before any upstream (no model, a refusal like
//     this one) uses nothing; a failed call counts the tokens it recorded.
//   - A streamed reply is settled when it ends, with the usage its vendor
//     reported; one cut off before that counts what was recorded.
//   - While a request is in flight it holds a reservation: its body's
//     size in tokens (4 bytes a token) plus the key's mean output per
//     call this window. A request is let in while used + reserved is
//     under the cap, so parallel requests overshoot by about one call.
//   - A key can have several limits, each in its own window (#2227: 5
//     hours and a week, as a subscription's): a request is let in only
//     while every one has something left, and its reservation is held
//     in each window.
//   - A limit can count only some models' calls (access.Limit.Models,
//     zola_xynb on X): "<provider>/<model>" or "<provider>/*", matched
//     against the provider and model that answered each call. A request
//     that names such a model is refused here once that limit is spent,
//     and holds its reservation there; a routing group's, or a fallback's,
//     goes on to the models outside it, as the gateway leaves a spent
//     limit's models out of every route's candidates (Spent). A group's
//     request holds no reservation in a model's limit, so parallel
//     requests through groups can go over it by about one call each.
//   - Only requests that came with a gateway key are limited: from
//     another computer one is required; on this one a request without one
//     (or with a token that is no key) isn't limited.
package budget

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwbudget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// sums is what a key's calls came to in a window.
type sums struct {
	start time.Time
	// scope is the limit's access.Limit.Scope, "" for the whole key, and
	// models its patterns with each model's other ids (scopeModels)
	scope                                string
	models                               []string
	calls                                int
	input, output, cacheRead, cacheWrite int64
	cost                                 float64
	unpriced                             int
}

func (s *sums) add(r usage.Record, cost float64, priced bool) {
	s.calls++
	s.input += int64(r.Input)
	s.output += int64(r.Output)
	s.cacheRead += int64(r.CacheRead)
	s.cacheWrite += int64(r.CacheWrite)
	s.cost += cost
	if !priced {
		s.unpriced++
	}
}

// counts says r is a call of the models s counts: every call, for the
// whole key.
func (s *sums) counts(r usage.Record) bool {
	return s.scope == "" || access.ModelAllowed(s.models, r.Provider+"/"+r.Model)
}

// scopeModels is the patterns l counts by: its own, and for a model the
// user lists by an id of their own (provider.SetModelID) the provider's
// id of it and back, as calls are recorded by the provider's.
func scopeModels(l *access.Limit) []string {
	out := slices.Clone(l.Models)
	for _, m := range l.Models {
		if strings.Contains(m, "*") {
			continue
		}
		pid, model, ok := strings.Cut(m, "/")
		if !ok {
			continue
		}
		p, err := provider.Find(pid)
		if err != nil {
			continue
		}
		for _, o := range []string{p.OwnModelID(model), p.ListedModelID(model)} {
			if o != model {
				out = append(out, pid+"/"+o)
			}
		}
	}
	return out
}

func (s *sums) tokens(l *access.Limit) int64 {
	n := s.input + s.output + s.cacheWrite
	if l != nil && l.CacheReads {
		n += s.cacheRead
	}
	return n
}

type held struct {
	n      int
	tokens int64
	cost   float64
}

// A reservation stays in the window where the request began, even if it
// finishes after the next one has started.
type window struct {
	key   string
	start int64  // the window's start as Unix seconds
	scope string // the limit's models (access.Limit.Scope), "" for the whole key
}

var state = struct {
	sync.Mutex
	sums map[string][]*sums // by usage.Path() and key, one a window its limits count in
	held map[window]*held
}{sums: map[string][]*sums{}, held: map[window]*held{}}

func slot(keyID string) string { return usage.Path() + "\x00" + keyID }

// counts reports whether r is a call a key's budget counts.
// A call an app reported itself (#1874) never counts: it didn't go through
// magpie, and may be a backfill of calls long past. Nor does an MCP tool
// call (usage.SourceMCP): the model requests it made are calls of their own.
func counts(r usage.Record) bool {
	return !r.IsRejected() && !r.IsReported() && !r.IsProbe() && r.Computer == ""
}

// sumsFor is keyID's sums for each of its limits ls, in the window each
// starts at starts, read from usage.jsonl the first time; those of
// windows no limit counts in now are let go. A window two limits of the
// same models share (a day and a week on a Monday, a day and a month on
// the 1st) is kept once, as Append adds a call to each one kept (#2942).
// Called with state held.
func sumsFor(keyID string, ls []*access.Limit, starts []time.Time) []*sums {
	k := slot(keyID)
	kept := state.sums[k]
	out := make([]*sums, len(starts))
	var next []*sums
	at := func(ss []*sums, start time.Time, scope string) int {
		return slices.IndexFunc(ss, func(s *sums) bool { return s.start.Equal(start) && s.scope == scope })
	}
	for i, start := range starts {
		scope := ls[i].Scope()
		if j := at(next, start, scope); j >= 0 {
			out[i] = next[j]
			continue
		}
		if j := at(kept, start, scope); j >= 0 {
			out[i] = kept[j]
		} else {
			out[i] = read(keyID, start, ls[i])
		}
		next = append(next, out[i])
	}
	state.sums[k] = next
	return out
}

// read is what keyID's calls of l's models came to from start.
func read(keyID string, start time.Time, l *access.Limit) *sums {
	s := &sums{start: start, scope: l.Scope()}
	if l.Scoped() {
		s.models = scopeModels(l)
	}
	cost := usage.NewCoster()
	usage.Visit(start, func(r usage.Record) {
		// a key issued under keyID spends from its limits too
		if (r.CallerKeyID == keyID || r.CallerIssuerID == keyID) && counts(r) && !r.Time.Before(start) && s.counts(r) {
			c, ok := cost(r)
			s.add(r, c, ok)
		}
	})
	return s
}

// Forget drops what is kept in memory, as a restart would.
func Forget() {
	state.Lock()
	defer state.Unlock()
	state.sums = map[string][]*sums{}
	state.held = map[window]*held{}
	kept.Lock()
	clear(kept.of)
	kept.Unlock()
}

// Append writes rec to usage.jsonl and, for a key's call, counts it
// against the key's window. The two happen together so a window read from
// the file meanwhile counts the call once.
func Append(rec usage.Record) {
	if rec.CallerKeyID == "" {
		usage.Append(rec)
		return
	}
	state.Lock()
	defer state.Unlock()
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	usage.Append(rec)
	if !counts(rec) {
		return
	}
	var cost func(usage.Record) (float64, bool)
	// the call counts for its key, and for the key that one was issued under
	for _, id := range []string{rec.CallerKeyID, rec.CallerIssuerID} {
		kept := state.sums[slot(id)]
		if id == "" || len(kept) == 0 {
			continue
		}
		if cost == nil {
			cost = usage.NewCoster()
		}
		c, ok := cost(rec)
		for _, s := range kept {
			if !rec.Time.Before(s.start) && s.counts(rec) {
				s.add(rec, c, ok)
			}
		}
	}
}

// Status is a key's limit and what it has used of it.
type Status struct {
	Period string `json:"period"`
	// Days is a "days" period's cycle, Hours an "hours" one's
	Days     int       `json:"days,omitempty"`
	Hours    int       `json:"hours,omitempty"`
	Start    time.Time `json:"start"`
	Reset    time.Time `json:"reset"`
	Calls    int       `json:"calls"`
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
	Unpriced int       `json:"unpriced,omitempty"`
	// InFlight requests, and the tokens and cost they are reserved at
	InFlight     int     `json:"inFlight,omitempty"`
	Reserved     int64   `json:"reserved,omitempty"`
	ReservedCost float64 `json:"reservedCost,omitempty"`
	TokenLimit   int64   `json:"tokenLimit,omitempty"`
	CostLimit    float64 `json:"costLimit,omitempty"`
	CacheReads   bool    `json:"cacheReads,omitempty"`
	// Models are the models the limit counts, none for the whole key
	Models []string `json:"models,omitempty"`
	// TokensLeft and CostLeft are what is left of each cap, 0 at most
	TokensLeft int64   `json:"tokensLeft"`
	CostLeft   float64 `json:"costLeft"`
	// Spent: the key is refused until Reset
	Spent bool `json:"spent"`
}

func status(keyID string, l *access.Limit, s *sums, h *held, reset time.Time) Status {
	st := Status{Period: l.Period, Days: l.Days, Hours: l.Hours, Start: s.start, Reset: reset, Calls: s.calls, Tokens: s.tokens(l), Cost: s.cost, Unpriced: s.unpriced,
		TokenLimit: l.Tokens, CostLimit: l.Cost, CacheReads: l.CacheReads, Models: l.Models}
	if h != nil {
		st.InFlight, st.Reserved, st.ReservedCost = h.n, h.tokens, h.cost
	}
	if l.Tokens > 0 {
		st.TokensLeft = max(l.Tokens-st.Tokens, 0)
		st.Spent = st.Tokens >= l.Tokens
	}
	if l.Cost > 0 {
		st.CostLeft = max(l.Cost-st.Cost, 0)
		st.Spent = st.Spent || st.Cost >= l.Cost
	}
	return st
}

// Of is key k's status now of its first limit, nil when it has no limit.
// It reads the file afresh rather than the gateway's sums, so it is right
// for a gateway served by another process too.
func Of(k access.Key, now time.Time) *Status {
	all := All(k, now)
	if len(all) == 0 {
		return nil
	}
	return &all[0]
}

// All is key k's status now of each of its limits (#2227), in the
// order k keeps them; none for a key with no limit. A window two
// limits share is read once.
func All(k access.Key, now time.Time) []Status {
	return AllOf([]access.Key{k}, now)[0]
}

// AllOf is All for each of keys, in their order, from one pass over the
// ledger, and from what the last pass counted: the Gateway page asked for
// every key's limits and read a month of the log again for each window of
// each key, on every poll, which timed out behind a proxy for a user with
// 12,000 calls a day (aiandcloud on X). A window's full blocks of the log
// are counted once and kept (usage.VisitFrom); each call counts the rows
// after them. The gateway's own count (sumsFor) is the one a limit is
// held by, and still reads the file on its own.
func AllOf(keys []access.Key, now time.Time) [][]Status {
	type want struct {
		w      window
		prefix sums // the full blocks' rows
		cur    usage.Cursor
		total  *sums // and the rows after them
	}
	out := make([][]Status, len(keys))
	wants := make([][]*want, len(keys))
	byKey := map[string][]*want{} // by the key a call is recorded under, or issued under
	var all []*want
	for i, k := range keys {
		seen := map[window]*want{}
		for _, l := range k.Limits() {
			start, _ := l.Window(now)
			w := window{slot(k.ID), start.Unix(), l.Scope()}
			x := seen[w]
			if x == nil {
				x = &want{w: w}
				x.prefix = sums{start: start, scope: l.Scope()}
				if l.Scoped() {
					x.prefix.models = scopeModels(l)
				}
				kept.Lock()
				// window keys a start by its second: what was kept is
				// another limit's when that one started in the same
				// second (a 10-day limit made a 5-hour one at once)
				if e, ok := kept.of[w]; ok && e.prefix.start.Equal(start) {
					x.prefix, x.cur = e.prefix, e.cur
				}
				kept.Unlock()
				seen[w] = x
				byKey[k.ID] = append(byKey[k.ID], x)
				all = append(all, x)
			}
			wants[i] = append(wants[i], x)
		}
	}
	if len(all) > 0 {
		cost := usage.NewCoster()
		// the windows that stopped at the same place read together
		for len(all) > 0 {
			cur := all[0].cur
			var group, rest []*want
			for _, x := range all {
				if x.cur == cur {
					group = append(group, x)
				} else {
					rest = append(rest, x)
				}
			}
			all = rest
			since := group[0].prefix.start
			for _, x := range group {
				if x.prefix.start.Before(since) {
					since = x.prefix.start
				}
			}
			ofGroup := map[string][]*want{}
			for _, x := range group {
				for id, ws := range byKey {
					if slices.Contains(ws, x) {
						ofGroup[id] = append(ofGroup[id], x)
					}
				}
			}
			count := func(into func(*want) *sums) func(usage.Record) {
				return func(r usage.Record) {
					ws := ofGroup[r.CallerKeyID]
					if r.CallerIssuerID != "" && r.CallerIssuerID != r.CallerKeyID {
						ws = append(slices.Clone(ws), ofGroup[r.CallerIssuerID]...)
					}
					if len(ws) == 0 || !counts(r) {
						return
					}
					var c float64
					var ok, priced bool
					for _, x := range ws {
						// a key issued under keyID spends from its limits too
						if s := into(x); !r.Time.Before(s.start) && s.counts(r) {
							if !priced {
								c, ok = cost(r)
								priced = true
							}
							s.add(r, c, ok)
						}
					}
				}
			}
			next := usage.VisitFrom(since, cur,
				func() {
					for _, x := range group {
						x.prefix = sums{start: x.prefix.start, scope: x.prefix.scope, models: x.prefix.models}
					}
				},
				count(func(x *want) *sums { return &x.prefix }),
				count(func(x *want) *sums {
					if x.total == nil {
						c := x.prefix
						x.total = &c
					}
					return x.total
				}))
			for _, x := range group {
				x.cur = next
				if x.total == nil {
					c := x.prefix
					x.total = &c
				}
				kept.Lock()
				if len(kept.of) > 1024 {
					clear(kept.of)
				}
				kept.of[x.w] = keptSums{prefix: x.prefix, cur: next}
				kept.Unlock()
			}
		}
	}
	state.Lock()
	defer state.Unlock()
	for i, k := range keys {
		for j, l := range k.Limits() {
			x := wants[i][j]
			_, reset := l.Window(now)
			out[i] = append(out[i], status(k.ID, l, x.total, state.held[x.w], reset))
		}
	}
	return out
}

// kept is what AllOf counted of each window's full blocks of the log.
var kept = struct {
	sync.Mutex
	of map[window]keptSums
}{of: map[window]keptSums{}}

type keptSums struct {
	prefix sums
	cur    usage.Cursor
}

// Binding is the one of a key's statuses that holds it most: of those
// spent, the one that resets last, as the key is refused until then;
// else the one with the most of a cap used. Nil for none. A limit of
// some models holds the key only when it has no limit of the whole key:
// one spent leaves the key's other models served, and a magpie using this
// one as a remote reads this one as the key's.
func Binding(all []Status) *Status {
	whole := slices.ContainsFunc(all, func(st Status) bool { return len(st.Models) == 0 })
	var b *Status
	for i := range all {
		st := &all[i]
		switch {
		case whole && len(st.Models) > 0:
		case b == nil:
			b = st
		case st.Spent != b.Spent:
			if st.Spent {
				b = st
			}
		case st.Spent:
			if st.Reset.After(b.Reset) {
				b = st
			}
		case st.share() > b.share():
			b = st
		}
	}
	return b
}

// share is how much of its fuller cap st has used, 0 to 1 and over.
func (st *Status) share() float64 {
	var f float64
	if st.TokenLimit > 0 {
		f = float64(st.Tokens) / float64(st.TokenLimit)
	}
	if st.CostLimit > 0 {
		f = max(f, st.Cost/st.CostLimit)
	}
	return f
}

// Refusal is why a key's request was turned away.
type Refusal struct {
	Status
	Key string
}

// Error says it as a client is told: the key, what it used of which
// cap, and when it resets.
func (r *Refusal) Error() string {
	what := ""
	if r.TokenLimit > 0 && r.Tokens+r.Reserved >= r.TokenLimit {
		counted := "input, output and cache-write tokens"
		if r.CacheReads {
			counted = "input, output and cache tokens"
		}
		what = fmt.Sprintf("%d of its %d %s", r.Tokens, r.TokenLimit, counted)
	} else {
		what = fmt.Sprintf("$%.2f of its $%.2f estimated cost", r.Cost, r.CostLimit)
	}
	if !r.Spent {
		what += fmt.Sprintf(", with %d requests in flight holding the rest", r.InFlight)
	}
	on := ""
	if len(r.Models) > 0 {
		on = " on " + strings.Join(r.Models, ", ")
	}
	if r.Period == "total" {
		return fmt.Sprintf("magpie gateway key %q has used %s%s of its total limit; it stays refused until the limit is reset or raised", r.Key, what, on)
	}
	return fmt.Sprintf("magpie gateway key %q has used %s%s for this %s; it resets at %s",
		r.Key, what, on, (&access.Limit{Period: r.Period, Days: r.Days, Hours: r.Hours}).Per(), r.Reset.Format("2006-01-02 15:04 MST"))
}

// RetryAfter is how long until the window resets, in whole seconds.
func (r *Refusal) RetryAfter(now time.Time) int {
	return max(int(r.Reset.Sub(now).Seconds()+0.999), 1)
}

// ResetAt is when the window resets.
func (r *Refusal) ResetAt() time.Time { return r.Reset }

// Limiter is Reserve as the gateway's budget point asks it
// (gwbudget.Limiter); plugins/budget serves it.
type Limiter struct{}

func (Limiter) Reserve(who access.Identity, body int64, model string, now time.Time) (func(), gwbudget.Refusal) {
	release, refused := Reserve(who, body, model, now)
	if refused != nil {
		return nil, refused
	}
	return release, nil
}

func (Limiter) Spent(who access.Identity, now time.Time) []access.SpentScope { return Spent(who, now) }

// Reserve lets a request of who's in, holding a reservation for it until
// the release it returns is called, or refuses it. body is the request's
// size in bytes (-1 unknown) and model the model it named, for a cost cap
// before the key has a price of its own this window.
func Reserve(who access.Identity, body int64, model string, now time.Time) (release func(), refused *Refusal) {
	if who.Issuer == nil {
		return reserve(who, body, model, now)
	}
	// an issued key's request is held to its own limits and to its
	// issuer's, which count what every key issued under it spends
	own, refused := reserve(who, body, model, now)
	if refused != nil {
		return nil, refused
	}
	up, refused := reserve(*who.Issuer, body, model, now)
	if refused != nil {
		own()
		return nil, refused
	}
	return func() { own(); up() }, nil
}

func reserve(who access.Identity, body int64, model string, now time.Time) (release func(), refused *Refusal) {
	// a limit of some models holds a request only when it names one of
	// them; one through a routing group is held by the gateway leaving
	// the spent ones' models out of its candidates (Spent)
	served := servedAs(model)
	if who.KeyID == "" || len(who.Limits()) == 0 {
		return func() {}, nil
	}
	state.Lock()
	defer state.Unlock()
	ls, starts, resets, ss := windowsOf(who, now, func(l *access.Limit) bool { return !l.Scoped() || scopeHas(l, served) })
	if len(ls) == 0 {
		return func() {}, nil
	}
	heldAt := func(i int) *held { return state.held[window{slot(who.KeyID), starts[i].Unix(), ls[i].Scope()}] }
	// every limit is asked: of those with nothing left, the one that
	// resets last is the one the client is told, as it is refused until then
	for i, l := range ls {
		s, h := ss[i], heldAt(i)
		var ht int64
		var hc float64
		if h != nil {
			ht, hc = h.tokens, h.cost
		}
		if (l.Tokens > 0 && s.tokens(l)+ht >= l.Tokens) || (l.Cost > 0 && s.cost+hc >= l.Cost) {
			if refused == nil || resets[i].After(refused.Reset) {
				refused = &Refusal{Status: status(who.KeyID, l, s, h, resets[i]), Key: who.KeyName}
			}
		}
	}
	if refused != nil {
		return nil, refused
	}
	// one reservation a window, however many of the key's limits count
	// in it, at that window's pace
	type hold struct {
		k    window
		h    *held
		est  int64
		cost float64
	}
	var holds []hold
	for i, l := range ls {
		k := window{slot(who.KeyID), starts[i].Unix(), l.Scope()}
		j := slices.IndexFunc(holds, func(x hold) bool { return x.k == k })
		if j < 0 {
			s := ss[i]
			est := max(body, 0) / 4
			if s.calls > 0 {
				est += s.output / int64(s.calls)
			}
			holds = append(holds, hold{k: k, est: est})
			j = len(holds) - 1
		}
		x := &holds[j]
		if l.Cost > 0 && x.cost == 0 {
			s, used := ss[i], ss[i].tokens(l)
			if used > 0 && s.cost > 0 {
				x.cost = float64(x.est) * s.cost / float64(used)
			} else if p, ok := priceOf(model); ok {
				x.cost = float64(x.est) * p
			}
		}
	}
	for i := range holds {
		x := &holds[i]
		x.h = state.held[x.k]
		if x.h == nil {
			x.h = &held{}
			state.held[x.k] = x.h
		}
		x.h.n++
		x.h.tokens += x.est
		x.h.cost += x.cost
	}
	once := sync.Once{}
	return func() {
		once.Do(func() {
			state.Lock()
			defer state.Unlock()
			for _, x := range holds {
				x.h.n--
				x.h.tokens -= x.est
				x.h.cost -= x.cost
				if x.h.n == 0 && state.held[x.k] == x.h {
					delete(state.held, x.k)
				}
			}
		})
	}, nil
}

// servedAs is the ids a request for model would be answered by, as
// "<provider>/<model>" with the provider's id of it and the user's own:
// none for a routing group, an auto/ model or a name magpie doesn't serve,
// whose models routing decides.
func servedAs(model string) []string {
	if model == "" || provider.IsGroupRef(model) {
		return nil
	}
	if _, ok := provider.GroupFor(model); ok {
		return nil
	}
	if _, ok := provider.AutoStandIn(model); ok {
		return nil
	}
	p, m, ok := provider.Resolve(model)
	if !ok {
		return nil
	}
	out := []string{p.ID + "/" + m}
	for _, o := range []string{p.OwnModelID(m), p.ListedModelID(m)} {
		if o != m {
			out = append(out, p.ID+"/"+o)
		}
	}
	return out
}

// scopeHas says one of ids is among l's models.
func scopeHas(l *access.Limit, ids []string) bool {
	for _, id := range ids {
		if access.ModelAllowed(l.Models, id) {
			return true
		}
	}
	return false
}

// Spent is who's limits of some models that have nothing left now, its
// own key's and the one it was issued under's: the gateway leaves their
// models out of every route's candidates for the request (access.Identity
// .Spent), each said with the refusal a request naming one is told.
func Spent(who access.Identity, now time.Time) []access.SpentScope {
	out := spentOf(who, now)
	if who.Issuer != nil {
		out = append(out, spentOf(*who.Issuer, now)...)
	}
	return out
}

func spentOf(who access.Identity, now time.Time) []access.SpentScope {
	if who.KeyID == "" || !slices.ContainsFunc(who.Limits(), (*access.Limit).Scoped) {
		return nil
	}
	state.Lock()
	defer state.Unlock()
	ls, starts, resets, ss := windowsOf(who, now, (*access.Limit).Scoped)
	var out []access.SpentScope
	for i, l := range ls {
		s, h := ss[i], state.held[window{slot(who.KeyID), starts[i].Unix(), l.Scope()}]
		var ht int64
		var hc float64
		if h != nil {
			ht, hc = h.tokens, h.cost
		}
		if (l.Tokens > 0 && s.tokens(l)+ht >= l.Tokens) || (l.Cost > 0 && s.cost+hc >= l.Cost) {
			r := &Refusal{Status: status(who.KeyID, l, s, h, resets[i]), Key: who.KeyName}
			out = append(out, access.SpentScope{Models: s.models, Reset: resets[i], Why: r.Error()})
		}
	}
	return out
}

// windowsOf is who's limits that keep holds, each with its window's start
// and reset and its sums. The sums of every limit are asked for, the ones
// left out too: sumsFor lets go of a window no limit asked for counts in,
// and a request of one model would read another's limit's again from the
// ledger on the next. Called with state held.
func windowsOf(who access.Identity, now time.Time, keep func(*access.Limit) bool) (ls []*access.Limit, starts, resets []time.Time, ss []*sums) {
	all := who.Limits()
	allStarts := make([]time.Time, len(all))
	allResets := make([]time.Time, len(all))
	for i, l := range all {
		allStarts[i], allResets[i] = l.Window(now)
	}
	allSums := sumsFor(who.KeyID, all, allStarts)
	for i, l := range all {
		if keep(l) {
			ls = append(ls, l)
			starts = append(starts, allStarts[i])
			resets = append(resets, allResets[i])
			ss = append(ss, allSums[i])
		}
	}
	return ls, starts, resets, ss
}

// priceOf is the input price per token of model when it names its
// provider ("provider/model").
func priceOf(model string) (float64, bool) {
	p, m, ok := strings.Cut(model, "/")
	if !ok || p == "" || m == "" {
		return 0, false
	}
	pr, ok := provider.EffectivePriceIn(settings.Load(), p, m)
	if !ok {
		return 0, false
	}
	// the call is made now, at this hour's price (Price.In)
	return pr.In(time.Now()).Input / 1e6, true
}

// ModelOf is the model a JSON request body names, "" for none.
func ModelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return v.Model
}
