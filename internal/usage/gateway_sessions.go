package usage

import (
	"maps"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// GatewaySessionWindow returns gateway sessions and their daily usage for a
// local-date range. It is kept separate from the GUI conversion so callers
// can use the same single usage-log snapshot for list and stats.
func GatewaySessionWindow(since time.Time) ([]GatewaySession, []GatewayDayUsage) {
	return gatewaySessionSnapshot(since, nil)
}

func GatewaySessionWindowExcept(since time.Time, nativeKeys map[string]bool) ([]GatewaySession, []GatewayDayUsage) {
	return gatewaySessionSnapshot(since, nativeKeys)
}

type GatewayDayUsage struct {
	Date, Agent, Model string
	sessions.Tokens
	Cost   float64
	Priced bool
	// first and last bound the day's requests (last includes the request's
	// own duration), so a session seen from a later midnight is derived
	// from the one kept projection instead of a second copy (#2776).
	first, last time.Time
}

// GatewaySession is a read-only projection of requests served by this
// gateway. Its identity is the agent name and the session id supplied by the
// client. Unidentified requests and generated recording identities are left out.
type GatewaySession struct {
	Agent  string            `json:"agent"`
	ID     string            `json:"id"`
	Start  time.Time         `json:"start"`
	Last   time.Time         `json:"last"`
	Models []GatewayModel    `json:"models"`
	Daily  []GatewayDayUsage `json:"-"`
	sessions.Tokens
	Cost     float64 `json:"cost"`
	Unpriced int     `json:"unpriced"`
}

type GatewayModel struct {
	Model string `json:"model"`
	sessions.Tokens
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`
}

// GatewaySessions aggregates one usage snapshot without reading routing
// history or agent files. NativeKeys contains identities already represented
// by local session files and is used to suppress the duplicate projection.
//
// A session's Models and Daily are the kept projection's own, shared by
// every caller, which reads them and never changes them: each ask cloned
// every session's days, and the start's first asks together held ~450 MB
// of copies over ~720k calls (#2776). They are clipped, so an append
// makes a slice of the caller's own.
func GatewaySessions(since time.Time, nativeKeys map[string]bool) []GatewaySession {
	if since.IsZero() {
		e := gatewayProjection()
		out := make([]GatewaySession, 0, len(e.sessions))
		for _, s := range e.sessions {
			if !nativeKeys[s.Agent+"|"+s.ID] {
				out = append(out, s)
			}
		}
		return out
	}
	if date, ok := localMidnight(since); ok {
		// a range of whole days is the all-time projection's days from
		// its first on: no copy of it is kept per range (#2776)
		e := gatewayProjection()
		out := make([]GatewaySession, 0, len(e.sessions))
		for _, s := range e.sessions {
			if nativeKeys[s.Agent+"|"+s.ID] || !s.Last.After(since) {
				continue
			}
			if w, ok := gatewaySessionFrom(s, date); ok {
				out = append(out, w)
			}
		}
		return out
	}
	// any other start is read from the log, and not kept
	snapshot := readLogSnapshot()
	out, _ := buildGatewaySessions(snapshot, since, nativeKeys, provider.UsageAlias())
	return out
}

// EachGatewaySession calls each with every session
// GatewaySessions(time.Time{}, nativeKeys) gives, in its order (newest
// first, then by agent and id), until each returns false, without a copy
// of them all: the Sessions list shows sessions.All rows at most, and each
// of its polls while a session grew copied every one of ~10k more. A
// session's Models and Daily are the projection's own, as there.
func EachGatewaySession(nativeKeys map[string]bool, each func(GatewaySession) bool) {
	for _, s := range gatewayProjection().sessions {
		if !nativeKeys[s.Agent+"|"+s.ID] && !each(s) {
			return
		}
	}
}

// localMidnight is since's local date when since is that date's start.
func localMidnight(since time.Time) (string, bool) {
	date := since.In(time.Local).Format(time.DateOnly)
	start, err := time.ParseInLocation(time.DateOnly, date, time.Local)
	return date, err == nil && start.Equal(since)
}

// gatewaySessionFrom is s as it was read from the start of date on: its
// days from then, and its models, tokens, cost and times summed from them.
func gatewaySessionFrom(s GatewaySession, date string) (GatewaySession, bool) {
	i := sort.Search(len(s.Daily), func(i int) bool { return s.Daily[i].Date >= date })
	if i == len(s.Daily) {
		return GatewaySession{}, false
	}
	if i == 0 {
		return s, true
	}
	w := GatewaySession{Agent: s.Agent, ID: s.ID, Models: []GatewayModel{}, Daily: s.Daily[i:]}
	models := map[string]int{} // index in w.Models, which grows
	for _, d := range w.Daily {
		if w.Start.IsZero() || d.first.Before(w.Start) {
			w.Start = d.first
		}
		if d.last.After(w.Last) {
			w.Last = d.last
		}
		w.Input += d.Input
		w.Output += d.Output
		w.CacheRead += d.CacheRead
		w.CacheWrite += d.CacheWrite
		w.CacheWrite1h += d.CacheWrite1h
		at, ok := models[d.Model]
		if !ok {
			at = len(w.Models)
			w.Models = append(w.Models, GatewayModel{Model: d.Model, Priced: true})
			models[d.Model] = at
		}
		m := &w.Models[at]
		m.Input += d.Input
		m.Output += d.Output
		m.CacheRead += d.CacheRead
		m.CacheWrite += d.CacheWrite
		m.CacheWrite1h += d.CacheWrite1h
		m.Cost += d.Cost
		m.Priced = m.Priced && d.Priced
	}
	finishGatewayModels(&w)
	return w, true
}

// finishGatewayModels sums s's models' cost into it, counts those not priced
// and puts the models in order, the most tokens first.
func finishGatewayModels(s *GatewaySession) {
	// sorted first, so the cost is summed in one order however the models
	// were gathered
	sort.Slice(s.Models, func(i, j int) bool {
		a, b := s.Models[i], s.Models[j]
		if a.Input+a.Output != b.Input+b.Output {
			return a.Input+a.Output > b.Input+b.Output
		}
		return a.Model < b.Model
	})
	for _, m := range s.Models {
		s.Cost += m.Cost
		if !m.Priced && m.Input+m.Output > 0 {
			s.Unpriced++
		}
	}
}

func GatewaySessionByID(agent, id string, nativeKeys map[string]bool) (GatewaySession, bool) {
	if id == "" {
		return GatewaySession{}, false
	}
	agent = ReportedAs(agent)
	if nativeKeys[agent+"|"+id] {
		return GatewaySession{}, false
	}
	entry := gatewayProjection()
	i, ok := entry.byID[agent+"|"+id]
	if !ok {
		return GatewaySession{}, false
	}
	return cloneGatewaySession(entry.sessions[i]), true
}

type gatewayProjectionEntry struct {
	version  uint64
	meta     string
	zone     *time.Location // the zone its days are dates in
	sessions []GatewaySession
	byID     map[string]int
}

// gatewayCache keeps one projection, of all time, not one copy for every
// native exclusion set or range: a range of days is read out of it. With
// ~600k calls each range kept was another ~37 MB, four of them at once
// (#2776). The log snapshot version also detects rewrites and changes of
// config directory.
var gatewayCache struct {
	sync.Mutex
	entry  *gatewayProjectionEntry
	prefix *gatewayPrefix
}

func gatewayProjection() gatewayProjectionEntry {
	snapshot := readLogSnapshot()
	meta := usagePriceCacheKey(time.Now())
	// read before the lock: it reads settings.json
	alias := provider.UsageAlias()
	gatewayCache.Lock()
	defer gatewayCache.Unlock()
	if e := gatewayCache.entry; e != nil && e.version == snapshot.version && e.meta == meta && e.zone == time.Local {
		return *e
	}
	ss, prefix := foldGateway(snapshot, meta, alias, gatewayCache.prefix)
	e := gatewayProjectionEntry{version: snapshot.version, meta: meta, zone: time.Local, sessions: ss, byID: make(map[string]int, len(ss))}
	for i, s := range ss {
		e.byID[s.Agent+"|"+s.ID] = i
	}
	// A failed or partial read must be retried, not published as current.
	if snapshot.complete() {
		gatewayCache.entry, gatewayCache.prefix = &e, prefix
	}
	return e
}

// gatewayPrefix is the projection's sessions as counted over the log's
// full blocks, which never change: a new call is counted on from them
// instead of the whole log again, ~600k calls on every poll (#2278).
type gatewayPrefix struct {
	n     int
	last  *rowChunk // blocks[n-1], which proves blocks[:n] the same
	meta  string
	zone  *time.Location
	built map[gatewayKey]GatewaySession
}

// gatewayFolds counts projections counted on from a prefix, for the tests.
var gatewayFolds int

// foldGateway is the projection of every call in snapshot, sorted, and
// the prefix to count the next one on from. Rows are counted in the log's
// order either way, so its numbers are buildGatewaySessions' to the bit.
func foldGateway(snapshot *logSnapshot, meta string, alias func(string) string, pre *gatewayPrefix) ([]GatewaySession, *gatewayPrefix) {
	gatewayBuilds.Add(1)
	blocks := snapshot.blocks
	cut := max(len(blocks)-1, 0)
	if pre == nil || pre.meta != meta || pre.zone != time.Local || pre.n > cut || pre.n > 0 && blocks[pre.n-1] != pre.last {
		pre = &gatewayPrefix{meta: meta, zone: time.Local, built: map[gatewayKey]GatewaySession{}}
	} else if pre.n > 0 {
		gatewayFolds++
	}
	priceOf, keep := Pricer(), keptStrings{}
	listed := func(base map[gatewayKey]GatewaySession, own map[gatewayKey]*gatewayAcc) map[gatewayKey]GatewaySession {
		if len(own) == 0 {
			return base
		}
		out := maps.Clone(base)
		if out == nil {
			out = map[gatewayKey]GatewaySession{}
		}
		for k, a := range own {
			s := a.session()
			// handed out as they are: an append is the caller's own
			s.Models, s.Daily = slices.Clip(s.Models), slices.Clip(s.Daily)
			out[k] = s
		}
		return out
	}
	next := pre
	if pre.n < cut {
		own := map[gatewayKey]*gatewayAcc{}
		countGateway(blocks[pre.n:cut], time.Time{}, nil, alias, priceOf, pre.built, own, nil, keep)
		next = &gatewayPrefix{n: cut, last: blocks[cut-1], meta: meta, zone: time.Local, built: listed(pre.built, own)}
	}
	tail := map[gatewayKey]*gatewayAcc{}
	countGateway(blocks[cut:], time.Time{}, nil, alias, priceOf, next.built, tail, nil, keep)
	all := listed(next.built, tail)
	out := make([]GatewaySession, 0, len(all))
	for _, s := range all {
		out = append(out, s)
	}
	sortGatewaySessions(out)
	return out, next
}

func cloneGatewaySession(s GatewaySession) GatewaySession {
	s.Models, s.Daily = slices.Clone(s.Models), slices.Clone(s.Daily)
	return s
}

func gatewaySessionSnapshot(since time.Time, nativeKeys map[string]bool) ([]GatewaySession, []GatewayDayUsage) {
	out := GatewaySessions(since, nativeKeys)
	return out, gatewayDays(out)
}

func gatewayDays(ss []GatewaySession) []GatewayDayUsage {
	type key struct{ date, agent, model string }
	groups := map[key]GatewayDayUsage{}
	for _, s := range ss {
		for _, d := range s.Daily {
			k := key{d.Date, d.Agent, d.Model}
			v, ok := groups[k]
			if !ok {
				v.Date, v.Agent, v.Model, v.Priced = d.Date, d.Agent, d.Model, true
			}
			v.Input += d.Input
			v.Output += d.Output
			v.CacheRead += d.CacheRead
			v.CacheWrite += d.CacheWrite
			v.CacheWrite1h += d.CacheWrite1h
			v.Cost += d.Cost
			v.Priced = v.Priced && d.Priced
			groups[k] = v
		}
	}
	out := make([]GatewayDayUsage, 0, len(groups))
	for _, d := range groups {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		return a.Model < b.Model
	})
	return out
}

// gatewayBuilds counts the projections built from the log, for tests.
var gatewayBuilds atomic.Int64

// alias is the name each model's usage is counted under (provider.UsageAlias),
// nil for each its own id: a call is priced by the id it was made under
// first, then counted under the alias.
func buildGatewaySessions(snapshot *logSnapshot, since time.Time, nativeKeys map[string]bool, alias func(string) string) ([]GatewaySession, []GatewayDayUsage) {
	gatewayBuilds.Add(1)
	counted := map[gatewayKey]*gatewayAcc{}
	daily := map[gatewayDayKey]*GatewayDayUsage{}
	countGateway(snapshot.blocks, since, nativeKeys, alias, Pricer(), nil, counted, daily, keptStrings{})
	out := make([]GatewaySession, 0, len(counted))
	for _, a := range counted {
		out = append(out, a.session())
	}
	sortGatewaySessions(out)
	days := make([]GatewayDayUsage, 0, len(daily))
	for _, d := range daily {
		days = append(days, *d)
	}
	sort.Slice(days, func(i, j int) bool {
		if days[i].Date != days[j].Date {
			return days[i].Date < days[j].Date
		}
		if days[i].Agent != days[j].Agent {
			return days[i].Agent < days[j].Agent
		}
		return days[i].Model < days[j].Model
	})
	return out, days
}

type gatewayKey struct{ agent, id string }

type gatewayDayKey struct{ date, agent, model string }

// gatewayAcc is one session as counted: its times and tokens, and its
// models and days, which session lists.
type gatewayAcc struct {
	s      GatewaySession
	models map[string]*GatewayModel
	days   map[gatewayDayKey]*GatewayDayUsage
}

// countedOn is listed session s as counted, to count on: it shares
// nothing with s.
func countedOn(s GatewaySession) *gatewayAcc {
	c := &gatewayAcc{s: s, models: make(map[string]*GatewayModel, len(s.Models)), days: make(map[gatewayDayKey]*GatewayDayUsage, len(s.Daily))}
	c.s.Models, c.s.Daily, c.s.Cost, c.s.Unpriced = []GatewayModel{}, nil, 0, 0
	for _, m := range s.Models {
		c.models[m.Model] = &m
	}
	for _, d := range s.Daily {
		c.days[gatewayDayKey{d.Date, d.Agent, d.Model}] = &d
	}
	return c
}

// session is a's session, with its days and models listed.
func (a *gatewayAcc) session() GatewaySession {
	s := a.s
	// of their own size: the projection keeps them, and grown an item
	// at a time they kept append's spare room, ~40% of them (#2776)
	s.Daily = make([]GatewayDayUsage, 0, len(a.days))
	s.Models = make([]GatewayModel, 0, len(a.models))
	for _, d := range a.days {
		s.Daily = append(s.Daily, *d)
	}
	sort.Slice(s.Daily, func(i, j int) bool {
		if s.Daily[i].Date != s.Daily[j].Date {
			return s.Daily[i].Date < s.Daily[j].Date
		}
		return s.Daily[i].Model < s.Daily[j].Model
	})
	for _, m := range a.models {
		s.Models = append(s.Models, *m)
	}
	finishGatewayModels(&s)
	return s
}

func sortGatewaySessions(out []GatewaySession) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Agent+"|"+out[i].ID < out[j].Agent+"|"+out[j].ID
	})
}

// countGateway counts blocks' calls into own, a session's counts begun
// from its session in base (never written), and into daily when it is
// not nil.
func countGateway(blocks []*rowChunk, since time.Time, nativeKeys map[string]bool, alias func(string) string, priceOf func(Record) *catalog.Price, base map[gatewayKey]GatewaySession, own map[gatewayKey]*gatewayAcc, daily map[gatewayDayKey]*GatewayDayUsage, keep keptStrings) {
	visitBlocks(blocks, since, func(r Record) {
		if r.IsRejected() || r.IsReported() || r.Session == "" {
			return
		}
		a := keep.of(ReportedAs(r.Agent))
		r.Session, r.Model = keep.of(r.Session), keep.of(r.Model)
		k := gatewayKey{a, r.Session}
		if nativeKeys != nil && nativeKeys[a+"|"+r.Session] {
			return
		}
		acc := own[k]
		if acc == nil {
			if b, ok := base[k]; ok {
				acc = countedOn(b)
			} else {
				acc = &gatewayAcc{s: GatewaySession{Agent: a, ID: r.Session, Models: []GatewayModel{}}, models: map[string]*GatewayModel{}, days: map[gatewayDayKey]*GatewayDayUsage{}}
			}
			own[k] = acc
		}
		s := &acc.s
		if s.Start.IsZero() || r.Time.Before(s.Start) {
			s.Start = r.Time
		}
		end := r.Time.Add(time.Duration(max(0, r.Millis)) * time.Millisecond)
		if end.After(s.Last) {
			s.Last = end
		}
		p := priceOf(r)
		cost, priced := 0.0, true
		if r.Input+r.Output > 0 {
			if p == nil {
				priced = false
			} else {
				cost = r.CostAt(*p)
			}
		}
		tokens := sessions.Tokens{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, CacheWrite1h: r.CacheWrite1h}
		model := r.Model // priced above by the id it was made under
		if alias != nil {
			model = keep.of(alias(model))
		}
		dk := gatewayDayKey{r.Time.In(time.Local).Format(time.DateOnly), a, model}
		for _, kept := range []map[gatewayDayKey]*GatewayDayUsage{daily, acc.days} {
			if kept == nil {
				continue
			}
			d := kept[dk]
			if d == nil {
				d = &GatewayDayUsage{Date: dk.date, Agent: a, Model: model, Priced: true}
				kept[dk] = d
			}
			d.Input += tokens.Input
			d.Output += tokens.Output
			d.CacheRead += tokens.CacheRead
			d.CacheWrite += tokens.CacheWrite
			d.CacheWrite1h += tokens.CacheWrite1h
			d.Cost += cost
			d.Priced = d.Priced && priced
			if d.first.IsZero() || r.Time.Before(d.first) {
				d.first = r.Time
			}
			if end.After(d.last) {
				d.last = end
			}
		}
		s.Input += r.Input
		s.Output += r.Output
		s.CacheRead += r.CacheRead
		s.CacheWrite += r.CacheWrite
		s.CacheWrite1h += r.CacheWrite1h
		m := acc.models[model]
		if m == nil {
			m = &GatewayModel{Model: model, Priced: true}
			acc.models[model] = m
		}
		m.addRecord(r, p)
	})
}

func (m *GatewayModel) addRecord(r Record, p *catalog.Price) {
	m.Input += r.Input
	m.Output += r.Output
	m.CacheRead += r.CacheRead
	m.CacheWrite += r.CacheWrite
	m.CacheWrite1h += r.CacheWrite1h
	if r.Input+r.Output == 0 {
		return
	}
	if p == nil {
		m.Priced = false
		return
	}
	m.Cost += r.CostAt(*p)
}
