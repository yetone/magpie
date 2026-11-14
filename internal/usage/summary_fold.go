package usage

import (
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A summary is two passes over the log: the first finds what tells a
// provider's calls apart (the hosts they went to, the accounts that
// answered), the second counts each call into its groups by that. Each new
// call to the log made the Usage page's periods count every call of the
// month again, ~10% of magpie's CPU while a window was open and calls came
// in (#2278). The log's full blocks never change, so a period keeps its
// counts up to the last block but one (summaryPrefix) and counts only the
// blocks after it, as long as the calls are told apart as they were.

// summaryRun is what one summary reads its calls by.
type summaryRun struct {
	now            time.Time
	renamed        provider.Renames
	alias          func(string) string
	cleared        *clearSet
	from, until    time.Time
	since          time.Time
	bucket         string
	series         []Point // the empty timeline
	period         Period
	historicalKeys map[string]bool
	priceOf        func(Record) *catalog.Price
}

func newSummaryRun(p Period, now, first time.Time, historicalKeys map[string]bool) *summaryRun {
	u := &summaryRun{now: now, renamed: provider.UsageRenames()}
	// the names models' usage is counted under (SlouchyQuill507): a model's
	// row sums its ids, each call priced by its own id first
	u.alias = provider.UsageAlias()
	u.historicalKeys = map[string]bool{}
	for id := range historicalKeys {
		for _, next := range u.renamed.Each(id) {
			u.historicalKeys[next] = true
		}
	}
	// the user's clears (cleared.go): a call cleared is in no figure, and a
	// clear of everything starts the timeline again
	u.cleared = readCleared()
	first = u.cleared.clearedSince(first)
	// a picked range's own days alone: its chart can start on the Monday
	// before its first, and it ends before now (#1492)
	u.from, u.until = p.Since(now), p.Until(now)
	u.since, u.bucket, u.series = timeline(p, now, first)
	u.period = p.shown()
	u.priceOf = Pricer()
	return u
}

// read hands fn the calls of read that the summary counts, as it counts
// them.
func (u *summaryRun) read(read func(func(Record)), fn func(Record)) {
	read(func(r Record) {
		if r.Time.Before(u.from) || after(u.until, r.Time) {
			return
		}
		r.renameBy(u.renamed)
		if u.cleared.hides(&r) {
			return
		}
		fn(r)
	})
}

// summaryHosts is the first pass: the places each provider id went in the
// period, and the providers an account answered for in it.
type summaryHosts struct {
	hosts    map[string]map[string]bool
	accounts map[string]bool
}

func newSummaryHosts() summaryHosts {
	return summaryHosts{hosts: map[string]map[string]bool{}, accounts: map[string]bool{}}
}

func (h summaryHosts) note(r Record, since time.Time) {
	if r.IsRejected() || r.Time.Before(since) {
		return
	}
	if r.Account() != "" {
		h.accounts[r.Provider] = true
	}
	if r.Host != "" {
		if h.hosts[r.Provider] == nil {
			h.hosts[r.Provider] = map[string]bool{}
		}
		h.hosts[r.Provider][who(r.Host)] = true
	}
}

func (h summaryHosts) clone() summaryHosts {
	c := summaryHosts{hosts: make(map[string]map[string]bool, len(h.hosts)), accounts: maps.Clone(h.accounts)}
	for p, ws := range h.hosts {
		c.hosts[p] = maps.Clone(ws)
	}
	return c
}

// summaryKeying is how the second pass tells calls apart: by host where a
// provider went to more than one or goes elsewhere now, by key, by account.
type summaryKeying struct {
	goesNow   map[string]string
	manyHosts map[string]bool
	// the providers whose calls are told apart by key, and by account:
	// those an account answered for in the period, and those signed in now
	keyProviders     map[string]bool
	accountProviders map[string]bool
}

func (u *summaryRun) keying(h summaryHosts) summaryKeying {
	k := summaryKeying{goesNow: map[string]string{}, manyHosts: map[string]bool{}, keyProviders: map[string]bool{}, accountProviders: maps.Clone(h.accounts)}
	for p, ws := range h.hosts {
		if len(ws) > 1 {
			k.manyHosts[p] = true
		}
	}
	for _, p := range provider.All() {
		k.goesNow[p.ID] = who(p.Where())
		if p.Account == nil && p.Key != "" {
			k.keyProviders[p.ID] = true
		}
		if p.Account != nil {
			k.accountProviders[p.ID] = true
		}
	}
	for id := range u.historicalKeys {
		k.keyProviders[id] = true
	}
	return k
}

func (k summaryKeying) equal(o summaryKeying) bool {
	return maps.Equal(k.goesNow, o.goesNow) && maps.Equal(k.manyHosts, o.manyHosts) && maps.Equal(k.keyProviders, o.keyProviders) && maps.Equal(k.accountProviders, o.accountProviders)
}

// groupSet is a summary's groups by key: those of base, which other
// summaries share and nothing writes, under those counted since (own).
type groupSet struct {
	base map[string]*Group
	own  map[string]*Group
}

// at is the group of k to count into, made by mk when there is none.
func (g *groupSet) at(k string, mk func() Group) *Group {
	if x := g.own[k]; x != nil {
		return x
	}
	var x Group
	if b := g.base[k]; b != nil {
		x = *b
	} else {
		x = mk()
	}
	if g.own == nil {
		g.own = map[string]*Group{}
	}
	g.own[k] = &x
	return &x
}

func (g *groupSet) list() []Group {
	out := make([]Group, 0, len(g.base)+len(g.own))
	for k, x := range g.base {
		if g.own[k] == nil {
			out = append(out, *x)
		}
	}
	for _, x := range g.own {
		out = append(out, *x)
	}
	return out
}

// freeze makes g's groups one base, which g counts on over from here.
func (g *groupSet) freeze() {
	if len(g.own) == 0 {
		return
	}
	m := make(map[string]*Group, len(g.base)+len(g.own))
	maps.Copy(m, g.base)
	maps.Copy(m, g.own)
	g.base, g.own = m, nil
}

// summaryCounts is the second pass's sums.
type summaryCounts struct {
	totals                                               Totals
	series                                               []Point
	agents, models, keys, accounts, callerKeys, sessions groupSet
}

func (u *summaryRun) newCounts() summaryCounts {
	return summaryCounts{series: slices.Clone(u.series)}
}

func (c *summaryCounts) sets() []*groupSet {
	return []*groupSet{&c.agents, &c.models, &c.keys, &c.accounts, &c.callerKeys, &c.sessions}
}

// carried is a copy of c to count on in, which leaves c as it is: c's
// groups must have been frozen.
func (c summaryCounts) carried() summaryCounts {
	c.series = slices.Clone(c.series)
	return c
}

// freeze makes c's groups their base, and gives a copy of c as it is now.
func (c *summaryCounts) freeze() summaryCounts {
	for _, g := range c.sets() {
		g.freeze()
	}
	return c.carried()
}

func (c *summaryCounts) add(u *summaryRun, k *summaryKeying, r Record) {
	if r.IsRejected() {
		return
	}
	t := r.Time.In(u.now.Location())
	if t.Before(u.since) {
		return
	}
	pr := u.priceOf(r)
	c.totals.add(r, pr)
	i := bucketIndex(u.bucket, u.since, t)
	if i >= 0 && i < len(c.series) {
		c.series[i].add(r, pr)
	}
	id := ReportedAs(r.Agent) // one kept before magpie knew the agent by name
	a := c.agents.at(id, func() Group { return Group{ID: id} })
	a.add(r, pr)
	if r.Time.After(a.Last) {
		a.Last = r.Time
	}
	model := r.Model
	if u.alias != nil {
		model = u.alias(model)
	}
	key, host := r.Provider+"/"+model, ""
	if w := who(r.Host); w != "" && (k.manyHosts[r.Provider] || w != k.goesNow[r.Provider]) {
		key, host = key+" @ "+w, w
	}
	m := c.models.at(key, func() Group { return Group{ID: key, Provider: r.Provider, Model: model, Host: host} })
	m.add(r, pr)
	m.addOwn(r, pr)
	if r.CallerKeyID != "" {
		g := c.callerKeys.at(r.CallerKeyID, func() Group { return Group{ID: r.CallerKeyID, CallerKeyID: r.CallerKeyID} })
		g.CallerKeyName = r.CallerKeyName
		g.add(r, pr)
	}
	if k.keyProviders[r.Provider] {
		g := c.keys.at(r.Provider+"#"+r.ProviderKeyID, func() Group {
			return Group{ID: r.Provider + "#" + r.ProviderKeyID, Provider: r.Provider, ProviderKeyID: r.ProviderKeyID}
		})
		g.ProviderKeyName = r.ProviderKeyName
		g.add(r, pr)
		g.addOwn(r, pr)
	}
	if k.accountProviders[r.Provider] {
		who := r.Account()
		g := c.accounts.at(r.Provider+"@"+who, func() Group { return Group{ID: r.Provider + "@" + who, Provider: r.Provider, Account: who} })
		g.add(r, pr)
		g.addOwn(r, pr)
	}
	if r.Session != "" {
		c.sessions.at(id+"|"+r.Session, func() Group { return Group{ID: r.Session, Agent: id} }).add(r, pr)
	}
}

func (c *summaryCounts) summary(u *summaryRun) Summary {
	s := Summary{Period: u.period, Since: u.since, Totals: c.totals, Bucket: u.bucket, Series: c.series}
	byTokens := func(gs []Group) []Group {
		sort.SliceStable(gs, func(i, j int) bool {
			if a, b := gs[i].Tokens(), gs[j].Tokens(); a != b {
				return a > b
			}
			if gs[i].Calls != gs[j].Calls {
				return gs[i].Calls > gs[j].Calls
			}
			return gs[i].ID < gs[j].ID
		})
		return gs
	}
	s.Agents = byTokens(c.agents.list())
	s.Models = byTokens(c.models.list())
	s.ProviderKeys = byTokens(c.keys.list())
	s.Accounts = byTokens(c.accounts.list())
	s.CallerKeys = byTokens(c.callerKeys.list())
	s.Sessions = byTokens(c.sessions.list())
	s.SessionCount = len(s.Sessions)
	return s
}

// summaryPrefix is a period's counts of the log's blocks up to the last
// but one, for the next summary of a log grown since to count on from.
type summaryPrefix struct {
	version uint64
	// the blocks counted: how many, and the last of them. Only a parse
	// that continues a snapshot carries its blocks on, all of them in their
	// places, so the last one being there says the rest are too; holding
	// it alone keeps a rewritten log's old blocks from staying in memory.
	n    int
	last *rowChunk
	// what the counts were made by
	meta, cleared string
	from, until   time.Time
	since         time.Time
	bucket        string
	series        []time.Time
	hosts         summaryHosts // the first pass over the blocks counted
	keyed         summaryKeying
	counts        summaryCounts
}

// fits is whether p's counts are of blocks' first ones, counted as u
// counts.
func (p *summaryPrefix) fits(u *summaryRun, meta string, blocks []*rowChunk) bool {
	if p == nil || p.n > len(blocks) || p.n > 0 && blocks[p.n-1] != p.last || p.meta != meta || p.cleared != u.cleared.key {
		return false
	}
	if !p.from.Equal(u.from) || !p.until.Equal(u.until) || !p.since.Equal(u.since) || p.bucket != u.bucket || len(p.series) != len(u.series) {
		return false
	}
	for i, t := range p.series {
		if !t.Equal(u.series[i].Time) || u.series[i].Label != p.counts.series[i].Label {
			return false
		}
	}
	return true
}

// summaryPrefixes are held under summaries' lock.
var summaryPrefixes struct {
	m       map[summaryKey]*summaryPrefix
	carried int // how many summaries counted on from a prefix, for the tests
}

// foldSummary is summarizeFrom over snapshot, counting on from the
// period's prefix where it fits.
func foldSummary(k summaryKey, p Period, now time.Time, snapshot *logSnapshot, meta string) Summary {
	u := newSummaryRun(p, now, snapshot.first, snapshot.keyProviders)
	blocks := snapshot.blocks
	cut := max(len(blocks)-1, 0)
	summaries.Lock()
	pre := summaryPrefixes.m[k]
	summaries.Unlock()
	if !pre.fits(u, meta, blocks) || pre.n > cut {
		pre = nil
	}
	from, hosts := 0, newSummaryHosts()
	if pre != nil {
		from, hosts = pre.n, pre.hosts.clone()
	}
	note := func(r Record) { hosts.note(r, u.since) }
	u.read(blockReader(blocks[from:cut], u.from), note)
	cutHosts := hosts.clone()
	u.read(blockReader(blocks[cut:], u.from), note)
	keyed := u.keying(hosts)
	var counts summaryCounts
	if pre != nil && keyed.equal(pre.keyed) {
		counts = pre.counts.carried()
		summaries.Lock()
		summaryPrefixes.carried++
		summaries.Unlock()
	} else {
		from, counts = 0, u.newCounts()
	}
	count := func(r Record) { counts.add(u, &keyed, r) }
	u.read(blockReader(blocks[from:cut], u.from), count)
	next := &summaryPrefix{version: snapshot.version, n: cut, meta: meta, cleared: u.cleared.key, from: u.from, until: u.until, since: u.since, bucket: u.bucket, hosts: cutHosts, keyed: keyed, counts: counts.freeze()}
	if cut > 0 {
		next.last = blocks[cut-1]
	}
	for _, pt := range u.series {
		next.series = append(next.series, pt.Time)
	}
	summaries.Lock()
	if summaryPrefixes.m == nil {
		summaryPrefixes.m = map[summaryKey]*summaryPrefix{}
	}
	if old := summaryPrefixes.m[k]; old == nil || old.version <= snapshot.version {
		summaryPrefixes.m[k] = next
	}
	summaries.Unlock()
	u.read(blockReader(blocks[cut:], u.from), count)
	return counts.summary(u)
}
