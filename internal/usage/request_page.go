package usage

// The request page keeps compact per-source records, not a second full ledger.
// Unchanged sources are never decoded again. Aggregation streams those records;
// only the requested page is materialized/sorted. Export can still ask for the
// complete ledger explicitly. This cache is disposable and bounded.

import (
	"bufio"
	"container/heap"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

type RequestPage struct {
	Rows              []Row
	Sum               Totals
	Total             int
	Agents, Providers []string
	CallerKeys        []Group
	Bucket            string
	Series            []SeriesPoint
	By                map[string][]Share
}

type packedRow struct {
	Time                           time.Time
	Text                           [24]uint32
	Tokens                         [5]int64
	Millis, TTFT, FirstText, Order int64
	RouteID                        int64
	Cost                           float64
	Status                         int32
	Flags                          uint8
}
type rowChunk struct {
	Archive   []byte
	Locations []*time.Location
	Rows      []packedRow
	Strings   []string
	dict      map[string]uint32
	Source    sessions.CallSource
	Bytes     int64
	Used      uint64
}

// rowMsg is the Text of a row's Claude message id, after rowText's
const rowMsg = 23

func rowText(r *Row) [23]*string {
	return [23]*string{&r.Agent, &r.Provider, &r.Host, &r.SessionProvider, &r.SessionAccount, &r.Model, &r.Requested, &r.Served, &r.Effort, &r.Error, &r.ErrType, &r.RequestID, &r.Endpoint, &r.Session, &r.NativeSession, &r.Kind, &r.Source, &r.Via, &r.ProviderKeyID, &r.ProviderKeyName, &r.CallerKeyID, &r.CallerKeyName, &r.Archive}
}
func (c *rowChunk) add(r Row, msg string, order int64, failed bool) {
	if c.dict == nil {
		c.dict = map[string]uint32{"": 0}
		c.Strings = []string{""}
	}
	intern := func(s string) uint32 {
		if id, ok := c.dict[s]; ok {
			return id
		}
		id := uint32(len(c.Strings))
		c.dict[s] = id
		c.Strings = append(c.Strings, s)
		c.Bytes += int64(len(s) + 48)
		return id
	}
	p := packedRow{Time: r.Time, Tokens: [5]int64{int64(r.Input), int64(r.Output), int64(r.CacheRead), int64(r.CacheWrite), int64(r.Reasoning)}, Millis: r.Millis, TTFT: r.TTFT, FirstText: r.FirstText, Order: order, RouteID: r.RouteID, Cost: r.Cost, Status: int32(r.Status)}
	for i, s := range rowText(&r) {
		p.Text[i] = intern(*s)
	}
	p.Text[rowMsg] = intern(msg)
	if r.Priced {
		p.Flags |= 1
	}
	if r.Swapped {
		p.Flags |= 2
	}
	if r.Rejected {
		p.Flags |= 4
	}
	if r.SessionOfficialLogin {
		p.Flags |= 8
	}
	if r.Routed {
		p.Flags |= 32
	}
	if failed {
		p.Flags |= 16
	}
	c.Rows = append(c.Rows, p)
	c.Bytes += int64(unsafe.Sizeof(packedRow{}))
}
func (c *rowChunk) row(i int) Row {
	p := &c.Rows[i]
	r := Row{Record: Record{RouteID: p.RouteID, Time: p.Time, Input: int(p.Tokens[0]), Output: int(p.Tokens[1]), CacheRead: int(p.Tokens[2]), CacheWrite: int(p.Tokens[3]), Reasoning: int(p.Tokens[4]), Millis: p.Millis, TTFT: p.TTFT, FirstText: p.FirstText, Status: int(p.Status), Rejected: p.Flags&4 != 0, SessionOfficialLogin: p.Flags&8 != 0}, Cost: p.Cost, Priced: p.Flags&1 != 0, Swapped: p.Flags&2 != 0, Routed: p.Flags&32 != 0}
	for i, s := range rowText(&r) {
		*s = c.Strings[p.Text[i]]
	}
	return r
}

type rowRef struct {
	Chunk *rowChunk
	Index int
}

func (r rowRef) row() Row      { return r.Chunk.row(r.Index) }
func (r rowRef) at() time.Time { return r.Chunk.Rows[r.Index].Time }
func refNewer(a, b rowRef) bool {
	if !a.at().Equal(b.at()) {
		return a.at().After(b.at())
	}
	if (a.Chunk.Source.Path == "") != (b.Chunk.Source.Path == "") {
		return a.Chunk.Source.Path == ""
	}
	if a.Chunk.Source.Path != b.Chunk.Source.Path {
		return a.Chunk.Source.Path > b.Chunk.Source.Path
	}
	return a.Chunk.Rows[a.Index].Order > b.Chunk.Rows[b.Index].Order
}

type newestHeap []rowRef

func (h newestHeap) Len() int           { return len(h) }
func (h newestHeap) Less(i, j int) bool { return refNewer(h[j], h[i]) }
func (h newestHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *newestHeap) Push(v any)        { *h = append(*h, v.(rowRef)) }
func (h *newestHeap) Pop() any          { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }

type pageKey struct {
	Period        Period
	Filter        Filter
	Offset, Limit int
}
type requestIndex struct {
	sync.Mutex
	root, meta, key                     string
	chunks                              map[string]*rowChunk
	gateway                             *rowChunk
	gatewaySize, gatewayMod, gatewayOff int64
	gatewayHash                         string
	pages                               map[pageKey]RequestPage
	tick                                uint64
}

var requestCache requestIndex

const requestCacheBytes = 24 << 20

func statKey(path string) string {
	s, e := os.Stat(path)
	if e != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%d:%d", path, s.Size(), s.ModTime().UnixNano())
}
func recordHash(path string, n int64) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.CopyN(h, f, n); e != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func QueryPage(p Period, f Filter, offset, limit int) RequestPage {
	return queryPage(p, f, offset, limit, sessions.ReadCallSource)
}
func queryPage(p Period, f Filter, offset, limit int, readSource func(sessions.CallSource) []sessions.Call) RequestPage {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	offset = max(0, offset)
	// A replacement reader remains useful to callers supplying synthetic logs.
	if LogCalls != nil {
		return pageFromLedger(p, f, offset, limit, LedgerOf(p, Filter{}))
	}
	sources := sessions.CallSources()
	slices.SortFunc(sources, func(a, b sessions.CallSource) int {
		if n := a.Modified.Compare(b.Modified); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	})
	ids, _ := json.Marshal(provider.SessionIdentities(sessions.CodexDir()))
	renamed, _ := json.Marshal(provider.Renamed())
	cfg := settings.Load()
	prices, _ := json.Marshal(cfg.ModelPrices)
	wires, _ := json.Marshal(cfg.ModelWires)
	meta := fmt.Sprintf("%s|%s|%s|%s|%s|%s", ids, renamed, prices, wires, statKey(provider.Path()), statKey(catalog.CachePath()))
	h := sha256.New()
	for _, root := range sessions.DesktopDataDirs() {
		for _, kind := range []string{"local-agent-mode-sessions", "claude-code-sessions"} {
			files, _ := sessions.SessionGlob(filepath.Join(root, kind, "*", "*", "local_*.json"))
			for _, path := range files {
				fmt.Fprint(h, statKey(path))
			}
		}
	}
	meta += fmt.Sprintf("|%x|%s", h.Sum(nil), statKey(catalog.LivePath("antigravity")))
	h.Reset()
	fmt.Fprint(h, meta, statKey(Path()), time.Now().Format("2006-01-02 MST"))
	for _, s := range sources {
		fmt.Fprintf(h, "%s:%d:%d;", s.Path, s.Size, s.Modified.UnixNano())
	}
	key := fmt.Sprintf("%x", h.Sum(nil))
	root := Path() + "|" + catalog.CachePath()
	idx := &requestCache
	idx.Lock()

	if idx.root != root || idx.meta != meta {
		idx.root, idx.meta = root, meta
		idx.chunks = map[string]*rowChunk{}
		idx.gateway = nil
		idx.pages = nil
		idx.key = ""
	}
	if idx.key != key {
		idx.pages = map[pageKey]RequestPage{}
		idx.key = key
	}
	q := pageKey{p, f, offset, limit}
	if page, ok := idx.pages[q]; ok {
		idx.Unlock()
		return page
	}
	idx.tick++
	// Copy only metadata while holding the lock. Published chunks are immutable;
	// reading, decoding, pricing and aggregation use a private snapshot.
	shared := idx
	idx = &requestIndex{root: idx.root, meta: idx.meta, key: idx.key, tick: idx.tick,
		chunks: maps.Clone(idx.chunks), gateway: idx.gateway,
		gatewaySize: idx.gatewaySize, gatewayMod: idx.gatewayMod, gatewayOff: idx.gatewayOff, gatewayHash: idx.gatewayHash}
	shared.Unlock()
	price := pricer()
	priceRow := func(r Record, source string) Row {
		sent := r.Model
		if source == "" {
			sent = provider.SentNameIn(cfg.ModelWires, r.Provider, r.Model, r.Effort)
		}
		row := Row{Record: r, Source: source, Swapped: r.Served != "" && Swapped(sent, r.Served), Routed: GroupRouted(sent, r.Served)}
		if pr := price(r); pr != nil && r.Input+r.Output > 0 {
			row.Priced = true
			row.Cost = pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
		}
		row.Agent = AgentOf(r.Agent)
		return row
	}
	cachedGateway, cachedSize, cachedMod := idx.gateway, idx.gatewaySize, idx.gatewayMod
	idx.readGateway(priceRow)
	since := p.Since(time.Now())
	on := map[string]bool{}
	var chunks []*rowChunk
	var resolver *sessionResolver
	for _, s := range sources {
		on[s.Path] = true
		if !since.IsZero() && s.Modified.Before(since) {
			continue
		}
		cached := idx.chunks[s.Path]
		c := cached.unpack()
		reused := c != nil && c.Source.Size == s.Size && c.Source.Modified.Equal(s.Modified)
		if !reused {
			if resolver == nil {
				resolver = newSessionResolver([]sessions.Call{{}})
			}
			cs := readSource(s)
			c = &rowChunk{Source: s, Rows: make([]packedRow, 0, len(cs))}
			for _, call := range cs {
				r := logRecord(call)
				r.SessionAccount = resolver.resolve(call)
				r.SessionOfficialLogin = resolver.officialLogin(call, r.SessionAccount)
				r.SessionProvider = call.Upstream
				c.add(priceRow(r, "log"), call.Msg, call.To, call.Error != "")
			}
			c.Bytes -= int64(32 * len(c.Strings))
			c.dict = nil // local chunks are immutable until the source changes
			idx.chunks[s.Path] = c
		}
		next := *c
		c = &next
		c.Used = idx.tick
		if reused {
			kept := *cached
			kept.Used = idx.tick
			idx.chunks[s.Path] = &kept
		} else {
			idx.chunks[s.Path] = c.pack()
		}
		chunks = append(chunks, c)
	}
	for path := range idx.chunks {
		if !on[path] {
			delete(idx.chunks, path)
		}
	}
	page := buildRequestPage(p, f, offset, limit, idx.gateway, chunks)
	// Compression and decoding also stay outside the cache lock.
	gatewaySnapshot := cachedGateway
	if gatewaySnapshot == nil || cachedSize != idx.gatewaySize || cachedMod != idx.gatewayMod {
		gatewaySnapshot = idx.gateway.pack()
	}
	if gatewaySnapshot.dict != nil {
		next := *gatewaySnapshot
		next.dict = nil
		next.Bytes -= int64(32 * len(next.Strings))
		gatewaySnapshot = &next
	}
	shared.Lock()
	defer shared.Unlock()
	// A query for an older filesystem snapshot may finish after a newer one.
	if shared.root != idx.root || shared.meta != idx.meta || shared.key != idx.key {
		return page
	}
	for path, c := range idx.chunks {
		if prev := shared.chunks[path]; prev == nil || prev.Used <= c.Used {
			shared.chunks[path] = c
		}
	}
	for path := range shared.chunks {
		if !on[path] {
			delete(shared.chunks, path)
		}
	}
	shared.gateway, shared.gatewaySize, shared.gatewayMod, shared.gatewayOff, shared.gatewayHash = gatewaySnapshot, idx.gatewaySize, idx.gatewayMod, idx.gatewayOff, idx.gatewayHash
	if len(shared.pages) >= 16 {
		shared.pages = map[pageKey]RequestPage{}
	}
	shared.pages[q] = page
	var bytes int64
	var kept []*rowChunk
	for _, c := range shared.chunks {
		bytes += c.Bytes
		kept = append(kept, c)
	}
	slices.SortFunc(kept, func(a, b *rowChunk) int {
		if a.Used < b.Used {
			return -1
		}
		if a.Used > b.Used {
			return 1
		}
		return strings.Compare(a.Source.Path, b.Source.Path)
	})
	for _, c := range kept {
		if bytes <= requestCacheBytes {
			break
		}
		delete(shared.chunks, c.Source.Path)
		bytes -= c.Bytes
	}
	return page
}

func (idx *requestIndex) readGateway(price func(Record, string) Row) {
	idx.gateway = idx.gateway.unpack()
	info, err := os.Stat(Path())
	if err != nil {
		idx.gateway = &rowChunk{}
		idx.gatewaySize, idx.gatewayMod, idx.gatewayOff = 0, 0, 0
		idx.gatewayHash = ""
		return
	}
	if idx.gateway != nil && idx.gatewaySize == info.Size() && idx.gatewayMod == info.ModTime().UnixNano() {
		return
	}
	continued := idx.gateway != nil && info.Size() > idx.gatewaySize && idx.gatewayHash != "" && recordHash(Path(), idx.gatewaySize) == idx.gatewayHash
	if continued {
		// Append to a copy; another period can be aggregating the published rows.
		c := *idx.gateway
		c.Rows, c.Strings, c.dict = slices.Clone(c.Rows), slices.Clone(c.Strings), maps.Clone(c.dict)
		if c.dict == nil {
			c.dict = make(map[string]uint32, len(c.Strings))
			for i, s := range c.Strings {
				c.dict[s] = uint32(i)
				c.Bytes += 32
			}
		}
		idx.gateway = &c
	}
	if !continued {
		idx.gateway = &rowChunk{}
		idx.gatewayOff = 0
	}
	file, err := os.Open(Path())
	if err != nil {
		return
	}
	defer file.Close()
	if _, err = file.Seek(idx.gatewayOff, io.SeekStart); err != nil {
		return
	}
	rd := bufio.NewReader(io.LimitReader(file, info.Size()-idx.gatewayOff))
	renamed := provider.Renamed()
	for {
		b, e := rd.ReadBytes('\n')
		if e != nil {
			break
		}
		idx.gatewayOff += int64(len(b))
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if id, ok := renamed[r.Provider]; ok {
			r.Provider = id
		}
		idx.gateway.add(price(r, ""), "", int64(len(idx.gateway.Rows)), false)
	}
	idx.gatewaySize, idx.gatewayMod = info.Size(), info.ModTime().UnixNano()
	idx.gatewayHash = recordHash(Path(), info.Size())
}

// localRefs visits first occurrences of Claude message IDs. It stores only
// references for matches, never a full hydrated row for every call.
func visibleLocal(chunks []*rowChunk) map[rowRef]bool {
	duplicates := map[rowRef]bool{}
	seen := map[string]bool{}
	for _, c := range chunks {
		for i, p := range c.Rows {
			msg := c.Strings[p.Text[rowMsg]]
			if msg == "" {
				continue
			}
			if seen[msg] {
				duplicates[rowRef{c, i}] = true
			} else {
				seen[msg] = true
			}
		}
	}
	return duplicates
}

// matchKey narrows candidates before comparing their end times. Request IDs
// are consumed first; fallback matching still requires uniqueness both ways.
type matchKey struct {
	session, agent string
	tokens         [4]int64
	failed, hasID  bool
}
type matchEnd struct {
	at    time.Time
	index int
}
type matchGroup struct {
	ends   []matchEnd
	counts []int
}

func matchedLocal(gateway *rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time) map[rowRef]bool {
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	byID := map[string][]int{}
	for i, p := range gateway.Rows {
		if (p.Flags&4 != 0 || p.Text[1] == 0 && p.Status >= 400) || p.Time.Before(gatewaySince) {
			continue
		}
		if id := gateway.Strings[p.Text[11]]; id != "" {
			byID[id] = append(byID[id], i)
		}
	}
	byRequest := map[string][]rowRef{}
	for _, c := range chunks {
		for i, p := range c.Rows {
			ref := rowRef{c, i}
			if skip[ref] || p.Time.Before(since) {
				continue
			}
			if id := c.Strings[p.Text[11]]; len(byID[id]) > 0 {
				byRequest[id] = append(byRequest[id], ref)
			}
		}
	}
	matched, used := map[rowRef]bool{}, map[int]bool{}
	for id, refs := range byRequest {
		slices.SortFunc(refs, func(a, b rowRef) int {
			if refNewer(a, b) {
				return -1
			}
			if refNewer(b, a) {
				return 1
			}
			return 0
		})
		for j, ref := range refs {
			if j >= len(byID[id]) {
				break
			}
			matched[ref], used[byID[id][j]] = true, true
		}
	}
	groups := map[matchKey]*matchGroup{}
	for i, p := range gateway.Rows {
		if used[i] || (p.Flags&4 != 0 || p.Text[1] == 0 && p.Status >= 400) || p.Time.Before(gatewaySince) {
			continue
		}
		session := gateway.Strings[p.Text[14]]
		if session == "" {
			session = gateway.Strings[p.Text[13]]
		}
		if session == "" {
			continue
		}
		key := matchKey{session, gateway.Strings[p.Text[0]], [4]int64(p.Tokens[:4]), p.Status >= 400 || p.Text[9] != 0, p.Text[11] != 0}
		g := groups[key]
		if g == nil {
			g = &matchGroup{}
			groups[key] = g
		}
		g.ends = append(g.ends, matchEnd{p.Time.Add(time.Duration(p.Millis) * time.Millisecond), i})
	}
	for _, g := range groups {
		slices.SortFunc(g.ends, func(a, b matchEnd) int { return a.at.Compare(b.at) })
		g.counts = make([]int, len(g.ends)+1)
	}
	candidates := map[rowRef]int{}
	for _, c := range chunks {
		for j, p := range c.Rows {
			ref := rowRef{c, j}
			if skip[ref] || matched[ref] || p.Time.Before(since) {
				continue
			}
			failed := p.Flags&16 != 0
			if p.Tokens[0]+p.Tokens[1]+p.Tokens[2]+p.Tokens[3] == 0 && !failed {
				continue
			}
			key := matchKey{session: c.Strings[p.Text[13]], agent: c.Strings[p.Text[0]], tokens: [4]int64(p.Tokens[:4]), failed: failed}
			count, candidate := 0, 0
			// Without a local ID either gateway partition can match. With an ID,
			// only an unnamed gateway call can match (different IDs stay distinct).
			for _, hasID := range []bool{false, true} {
				if hasID && p.Text[11] != 0 {
					continue
				}
				key.hasID = hasID
				g := groups[key]
				if g == nil {
					continue
				}
				lo := sort.Search(len(g.ends), func(i int) bool { return !g.ends[i].at.Before(p.Time.Add(-2 * time.Second)) })
				hi := sort.Search(len(g.ends), func(i int) bool { return g.ends[i].at.After(p.Time.Add(2 * time.Second)) })
				if hi > lo {
					count += hi - lo
					candidate = g.ends[lo].index
					// Range counts preserve ambiguity even for thousands of identical
					// retries, without enumerating their Cartesian product.
					g.counts[lo]++
					g.counts[hi]--
				}
			}
			if count == 1 {
				candidates[ref] = candidate
			}
		}
	}
	unique := map[int]bool{}
	for _, g := range groups {
		count := 0
		for i, end := range g.ends {
			count += g.counts[i]
			if count == 1 {
				unique[end.index] = true
			}
		}
	}
	for ref, i := range candidates {
		if unique[i] {
			matched[ref] = true
		}
	}
	return matched
}
func sharesOf(m map[string]*Share) []Share {
	out := make([]Share, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Share) int {
		if a.AllTokens() != b.AllTokens() {
			return b.AllTokens() - a.AllTokens()
		}
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
func buildRequestPage(p Period, f Filter, offset, limit int, gateway *rowChunk, chunks []*rowChunk) RequestPage {
	skip := visibleLocal(chunks)
	since := p.Since(time.Now())
	matched := matchedLocal(gateway, chunks, skip, since)
	all := append([]*rowChunk{gateway}, chunks...)
	visit := func(fn func(rowRef, Row)) {
		for _, c := range all {
			for i, pr := range c.Rows {
				ref := rowRef{c, i}
				if pr.Time.Before(since) || skip[ref] || matched[ref] {
					continue
				}
				fn(ref, c.row(i))
			}
		}
	}
	out := RequestPage{Rows: []Row{}, Agents: []string{}, Providers: []string{}, By: map[string][]Share{}}
	agents, providers := map[string]bool{}, map[string]bool{}
	callers := map[string]*Group{}
	groups := map[string]map[string]*Share{}
	seriesGroups := map[string]map[string]*Share{}
	for _, d := range Dimensions {
		groups[d] = map[string]*Share{}
		seriesGroups[d] = map[string]*Share{}
	}
	// Bound the requested prefix before adding limit: external offsets can
	// reach MaxInt, and a page beyond all sources needs no heap at all.
	maxRows := 0
	for _, c := range all {
		maxRows += len(c.Rows)
	}
	take := 0
	if offset < maxRows {
		take = offset + min(limit, maxRows-offset)
	}
	selected := newestHeap{}
	var first time.Time
	visit(func(ref rowRef, r Row) {
		agents[r.Agent] = true
		if r.Provider != "" {
			providers[r.Provider] = true
		}
		addCallerRow(callers, r)
		keep := f.keeps(r.Record)
		if keep {
			out.Total++
			if take > 0 && len(selected) < take {
				heap.Push(&selected, ref)
			} else if take > 0 && refNewer(ref, selected[0]) {
				selected[0] = ref
				heap.Fix(&selected, 0)
			}
			if !r.IsRejected() {
				out.Sum.addRow(r)
				for _, d := range Dimensions {
					k := r.key(d)
					s := seriesGroups[d][k]
					if s == nil {
						s = &Share{ID: k}
						seriesGroups[d][k] = s
					}
					s.addRow(r)
				}
				if first.IsZero() || r.Time.Before(first) {
					first = r.Time
				}
			}
		}
		if r.IsRejected() {
			return
		}
		for _, d := range Dimensions {
			g := f
			if d == "provider" {
				g.Provider = ""
			}
			if d == "agent" {
				g.Agent = ""
			}
			if d == "model" {
				g.Model = ""
			}
			if !g.keeps(r.Record) {
				continue
			}
			k := r.key(d)
			s := groups[d][k]
			if s == nil {
				s = &Share{ID: k}
				groups[d][k] = s
			}
			s.addRow(r)
		}
	})
	slices.SortFunc(selected, func(a, b rowRef) int {
		if refNewer(a, b) {
			return -1
		}
		if refNewer(b, a) {
			return 1
		}
		return 0
	})
	for _, ref := range selected[min(offset, len(selected)):] {
		out.Rows = append(out.Rows, ref.row())
	}
	for a := range agents {
		out.Agents = append(out.Agents, a)
	}
	slices.Sort(out.Agents)
	for a := range providers {
		out.Providers = append(out.Providers, a)
	}
	slices.Sort(out.Providers)
	out.CallerKeys = callerGroups(callers)
	for _, d := range Dimensions {
		out.By[d] = sharesOf(groups[d])
	}
	var base []Point
	chartSince := since
	chartSince, out.Bucket, base = timeline(p, time.Now(), first)
	out.Series = make([]SeriesPoint, len(base))
	for i := range base {
		out.Series[i] = SeriesPoint{Point: base[i], By: map[string]map[string]Part{}}
		for _, d := range Dimensions {
			out.Series[i].By[d] = map[string]Part{}
		}
	}
	visit(func(_ rowRef, r Row) {
		if !f.keeps(r.Record) || r.IsRejected() {
			return
		}
		t := r.Time.In(time.Local)
		if t.Before(chartSince) {
			return
		}
		i := bucketIndex(out.Bucket, chartSince, t)
		if i < 0 || i >= len(out.Series) {
			return
		}
		pt := &out.Series[i]
		pt.addRow(r)
		for _, d := range Dimensions {
			k := r.key(d)
			part := pt.By[d][k]
			part.Calls++
			part.Tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
			if r.Priced {
				part.Cost += r.Cost
			}
			pt.By[d][k] = part
		}
	})
	// Match LedgerSeries's top-24 selection on the filtered data, not facets.
	for _, d := range Dimensions {
		kept := map[string]bool{}
		for i, s := range sharesOf(seriesGroups[d]) {
			if i < seriesKeep {
				kept[s.ID] = true
			}
		}
		for i := range out.Series {
			for k := range out.Series[i].By[d] {
				if !kept[k] {
					delete(out.Series[i].By[d], k)
				}
			}
		}
	}
	return out
}
func pageFromLedger(p Period, f Filter, offset, limit int, all Ledgered) RequestPage {
	l := all.Filtered(f)
	out := RequestPage{Sum: l.Sum, Total: len(l.Rows), Agents: l.Agents, Providers: l.Providers, By: map[string][]Share{}}
	callers := map[string]*Group{}
	for i := len(all.Rows) - 1; i >= 0; i-- {
		addCallerRow(callers, all.Rows[i])
	}
	out.CallerKeys = callerGroups(callers)
	offset = min(offset, len(l.Rows))
	out.Rows = l.Rows[offset:min(len(l.Rows), offset+limit)]
	out.Bucket, out.Series = LedgerSeries(p, l.Rows)
	for _, d := range Dimensions {
		g := f
		if d == "provider" {
			g.Provider = ""
		}
		if d == "agent" {
			g.Agent = ""
		}
		if d == "model" {
			g.Model = ""
		}
		out.By[d] = Breakdown(all.Filtered(g).Rows, d)
	}
	return out
}

// Caller choices cover the period, not just the current page or selected key.
func addCallerRow(groups map[string]*Group, r Row) {
	if r.CallerKeyID == "" || r.IsRejected() {
		return
	}
	g := groups[r.CallerKeyID]
	if g == nil {
		g = &Group{ID: r.CallerKeyID, CallerKeyID: r.CallerKeyID}
		groups[r.CallerKeyID] = g
	}
	g.CallerKeyName = r.CallerKeyName
	g.addRow(r)
}

func callerGroups(groups map[string]*Group) []Group {
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b Group) int {
		if a.Tokens() != b.Tokens() {
			return b.Tokens() - a.Tokens()
		}
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
