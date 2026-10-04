package usage

// The request page keeps compact per-source records, not a second full ledger.
// Unchanged sources are never decoded again. Aggregation streams those records;
// only the requested page is materialized/sorted. Export can still ask for the
// complete ledger explicitly. This cache is disposable and bounded.

import (
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
	// Accounts are the subscription accounts that answered calls in the
	// period, by provider and account, for the Account filter (#557)
	Accounts []Group
	Bucket   string
	Series   []SeriesPoint
	By       map[string][]Share
	ChartBy  map[string][]Share // the whole period's legend while a day is selected
	// Computers are the rows told apart by the computer they were made on,
	// ThisComputer's and each other's by id, of the rows without the
	// filter's computer, and Names what the others are called: none when no
	// other computer's calls were brought here by sync (#542)
	Computers []Share
	Names     map[string]string
}

type packedRow struct {
	Time                           time.Time
	Text                           [27]uint32
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
	Count     int
	Latest    time.Time
	// Computer is the other computer whose day this is (#542), by id
	Computer string
}

// rowMsg is the Text of a row's Claude message id, after rowText's
const rowMsg = 26

func rowText(r *Row) [26]*string {
	return [26]*string{&r.Agent, &r.Provider, &r.Host, &r.SessionProvider, &r.SessionAccount, &r.Model, &r.Requested, &r.Served, &r.Effort, &r.Error, &r.ErrType, &r.RequestID, &r.Endpoint, &r.Session, &r.NativeSession, &r.Kind, &r.Source, &r.Via, &r.ProviderKeyID, &r.ProviderKeyName, &r.CallerKeyID, &r.CallerKeyName, &r.Archive, &r.Operation, &r.ProviderAccount, &r.ResponseID}
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
	c.Count = len(c.Rows)
	if r.Time.After(c.Latest) {
		c.Latest = r.Time
	}
	c.Bytes += int64(unsafe.Sizeof(packedRow{}))
}
func (c *rowChunk) row(i int) Row {
	p := &c.Rows[i]
	r := Row{Record: Record{RouteID: p.RouteID, Time: p.Time, Input: int(p.Tokens[0]), Output: int(p.Tokens[1]), CacheRead: int(p.Tokens[2]), CacheWrite: int(p.Tokens[3]), Reasoning: int(p.Tokens[4]), Millis: p.Millis, TTFT: p.TTFT, FirstText: p.FirstText, Status: int(p.Status), Rejected: p.Flags&4 != 0, SessionOfficialLogin: p.Flags&8 != 0}, Cost: p.Cost, Priced: p.Flags&1 != 0, Swapped: p.Flags&2 != 0, Routed: p.Flags&32 != 0}
	for i, s := range rowText(&r) {
		*s = c.Strings[p.Text[i]]
	}
	r.Computer = c.Computer
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
	root, meta, key string
	chunks          map[string]*rowChunk
	gateways        map[*rowChunk]*rowChunk // immutable raw block -> priced block
	pages           map[pageKey]RequestPage
	tick            uint64
}

var requestCache requestIndex

var requestCacheBytes int64 = 24 << 20

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
	for _, current := range provider.All() {
		fmt.Fprintf(h, "%q:%q:%v:%t:%t:%t;", current.ID, current.Where(), current.Catalogs(), current.IsPlugin(), current.Account == nil, current.Key != "")
		fmt.Fprint(h, statKey(catalog.LivePath(current.ID)))
	}
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
	snapshot := logSnapshotFor(true)
	version := snapshot.version
	fmt.Fprint(h, meta, version, time.Now().Format("2006-01-02 MST"))
	for _, s := range sources {
		fmt.Fprintf(h, "%s:%d:%d;", s.Path, s.Size, s.Modified.UnixNano())
	}
	days, someShared := sharedSources(time.Time{})
	for _, s := range days {
		fmt.Fprintf(h, "%s:%d:%d;", s.path, s.size, s.mod.UnixNano())
	}
	key := fmt.Sprintf("%x", h.Sum(nil))
	root := Path() + "|" + catalog.CachePath()
	idx := &requestCache
	idx.Lock()

	if idx.root != root || idx.meta != meta {
		idx.root, idx.meta = root, meta
		idx.chunks = map[string]*rowChunk{}
		idx.gateways = map[*rowChunk]*rowChunk{}
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
		chunks: maps.Clone(idx.chunks), gateways: maps.Clone(idx.gateways)}
	shared.Unlock()
	if snapshot.uncached && len(snapshot.blocks) == 0 {
		snapshot = readLogSnapshot()
	}
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
	since := p.Since(time.Now())
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	var gateways []*rowChunk
	aliases := provider.Renamed()
	live := make(map[*rowChunk]bool, len(snapshot.blocks))
	for _, packed := range snapshot.blocks {
		live[packed] = true
		if !gatewaySince.IsZero() && packed.Latest.Before(gatewaySince) {
			continue
		}
		cached := idx.gateways[packed]
		c := cached.unpack()
		if c == nil {
			raw := packed.unpack()
			if raw == nil {
				continue
			}
			c = &rowChunk{}
			// Cache the entire block: another period can reuse the same prices.
			// Only the append tail changes; a tariff change clears all blocks.
			for i, pr := range raw.Rows {
				r := raw.row(i).Record
				if id, ok := aliases[r.Provider]; ok {
					r.Provider = id
				}
				c.add(priceRow(r, ""), "", pr.Order, false)
			}
			cached = c.freeze()
		}
		kept := *cached
		kept.Used = idx.tick
		idx.gateways[packed] = &kept
		gateways = append(gateways, c)
	}
	for raw := range idx.gateways {
		if !live[raw] {
			delete(idx.gateways, raw)
		}
	}
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
	// the other computers' days (#542), a chunk each, read again only when
	// sync brought a new copy
	var others []*rowChunk
	var renames map[string]string
	from := ""
	if !since.IsZero() {
		from = since.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	for _, s := range days {
		on[s.path] = true
		if s.day < from {
			continue
		}
		cached := idx.chunks[s.path]
		c := cached.unpack()
		reused := c != nil && c.Source.Size == s.size && c.Source.Modified.Equal(s.mod)
		if !reused {
			d, _ := readShared(s.path)
			if renames == nil {
				renames = provider.Renamed()
			}
			c = &rowChunk{Source: sessions.CallSource{Path: s.path, Size: s.size, Modified: s.mod}, Computer: s.computer, Rows: make([]packedRow, 0, len(d.Calls))}
			for i, call := range d.Calls {
				r := call.Record
				r.Computer = ""
				if id, ok := renames[r.Provider]; ok {
					r.Provider = id
				}
				c.add(priceRow(r, call.Source), "", int64(i), false)
			}
			c.Bytes -= int64(32 * len(c.Strings))
			c.dict = nil
		}
		next := *c
		c = &next
		c.Used = idx.tick
		if reused {
			kept := *cached
			kept.Used = idx.tick
			idx.chunks[s.path] = &kept
		} else {
			idx.chunks[s.path] = c.pack()
		}
		others = append(others, c)
	}
	for path := range idx.chunks {
		if !on[path] {
			delete(idx.chunks, path)
		}
	}
	var names map[string]string
	if someShared {
		names = sharedNames()
	}
	page := buildRequestBlocks(p, f, offset, limit, gateways, chunks, others, names)
	shared.Lock()
	defer shared.Unlock()
	// A query for an older filesystem snapshot may finish after a newer one.
	if shared.root != idx.root || shared.meta != idx.meta || shared.key != idx.key || snapshot.version != version {
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
	for raw, c := range idx.gateways {
		if prev := shared.gateways[raw]; prev == nil || prev.Used <= c.Used {
			shared.gateways[raw] = c
		}
	}
	for raw := range shared.gateways {
		if !live[raw] {
			delete(shared.gateways, raw)
		}
	}
	if len(shared.pages) >= 16 {
		shared.pages = map[pageKey]RequestPage{}
	}
	if !snapshot.uncached {
		shared.pages[q] = page
	}
	// Raw blocks are also retained by gateway-map keys: charge their bytes once.
	bytes := snapshot.bytes
	budget := int64(requestCacheBytes)
	if snapshot.uncached {
		budget = 0
		shared.pages = map[pageKey]RequestPage{}
	}

	type cachedChunk struct {
		path string
		raw  *rowChunk
		row  *rowChunk
	}
	var kept []cachedChunk
	for path, c := range shared.chunks {
		bytes += c.Bytes
		kept = append(kept, cachedChunk{path: path, row: c})
	}
	for raw, c := range shared.gateways {
		bytes += c.Bytes
		kept = append(kept, cachedChunk{raw: raw, row: c})
	}
	slices.SortFunc(kept, func(a, b cachedChunk) int {
		if a.row.Used < b.row.Used {
			return -1
		}
		if a.row.Used > b.row.Used {
			return 1
		}
		return strings.Compare(a.path, b.path)
	})
	for _, c := range kept {
		if bytes <= budget {
			break
		}
		if c.raw != nil {
			delete(shared.gateways, c.raw)
		} else {
			delete(shared.chunks, c.path)
		}
		bytes -= c.row.Bytes
	}
	return page
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
	tokens         [3]int64
	failed, hasID  bool
}

// matchTokens is what a call's tokens are matched by: its input with what it
// wrote to the cache in it, its output and what it read from the cache. The
// two logs needn't split a cache write out of the input alike (#589): a
// Codex that names cache_write_input_tokens beside a gateway record that
// didn't, an older Codex that doesn't beside one that does; the sum is the
// same either way.
func matchTokens(t [5]int64) [3]int64 { return [3]int64{t[0] + t[3], t[1], t[2]} }

type matchEnd struct {
	at    time.Time
	index rowRef
}
type matchGroup struct {
	ends   []matchEnd
	counts []int
}

func matchedLocal(gateway *rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time) map[rowRef]bool {
	return matchedBlocks([]*rowChunk{gateway}, chunks, skip, since, true)
}

func matchedBlocks(gateways []*rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time, newestIDs bool) map[rowRef]bool {
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	byID := map[string][]rowRef{}
	for _, gateway := range gateways {
		for i, p := range gateway.Rows {
			if (p.Flags&4 != 0 || p.Text[1] == 0 && p.Status >= 400) || p.Time.Before(gatewaySince) {
				continue
			}
			if id := gateway.Strings[p.Text[11]]; id != "" {
				byID[id] = append(byID[id], rowRef{gateway, i})
			}
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
	matched, used := map[rowRef]bool{}, map[rowRef]bool{}
	for id, refs := range byRequest {
		if newestIDs {
			slices.SortFunc(refs, func(a, b rowRef) int {
				if refNewer(a, b) {
					return -1
				}
				if refNewer(b, a) {
					return 1
				}
				return 0
			})
		}
		for j, ref := range refs {
			if j >= len(byID[id]) {
				break
			}
			matched[ref], used[byID[id][j]] = true, true
		}
	}
	groups := map[matchKey]*matchGroup{}
	for _, gateway := range gateways {
		for i, p := range gateway.Rows {
			if used[rowRef{gateway, i}] || (p.Flags&4 != 0 || p.Text[1] == 0 && p.Status >= 400) || p.Time.Before(gatewaySince) {
				continue
			}
			session := gateway.Strings[p.Text[14]]
			if session == "" {
				session = gateway.Strings[p.Text[13]]
			}
			if session == "" {
				continue
			}
			key := matchKey{session, gateway.Strings[p.Text[0]], matchTokens(p.Tokens), p.Status >= 400 || p.Text[9] != 0, p.Text[11] != 0}
			g := groups[key]
			if g == nil {
				g = &matchGroup{}
				groups[key] = g
			}
			g.ends = append(g.ends, matchEnd{p.Time.Add(time.Duration(p.Millis) * time.Millisecond), rowRef{gateway, i}})
		}
	}
	for _, g := range groups {
		slices.SortFunc(g.ends, func(a, b matchEnd) int { return a.at.Compare(b.at) })
		g.counts = make([]int, len(g.ends)+1)
	}
	candidates := map[rowRef]rowRef{}
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
			key := matchKey{session: c.Strings[p.Text[13]], agent: c.Strings[p.Text[0]], tokens: matchTokens(p.Tokens), failed: failed}
			count, candidate := 0, (rowRef{})
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
	unique := map[rowRef]bool{}
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
	return buildRequestBlocks(p, f, offset, limit, []*rowChunk{gateway}, chunks, nil, nil)
}

// Shared days are never matched to this computer's gateway or session calls.
func buildRequestBlocks(p Period, f Filter, offset, limit int, gateways, chunks, others []*rowChunk, names map[string]string) RequestPage {
	skip := visibleLocal(chunks)
	since := p.Since(time.Now())
	matched := matchedBlocks(gateways, chunks, skip, since, true)
	all := append(append(slices.Clone(gateways), chunks...), others...)
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
	accounts := map[string]*Group{}
	computers := map[string]*Share{}
	groups := map[string]map[string]*Share{}
	chartGroups := map[string]map[string]*Share{}
	seriesGroups := map[string]map[string]*Share{}
	for _, d := range Dimensions {
		groups[d] = map[string]*Share{}
		chartGroups[d] = map[string]*Share{}
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
	chartFilter := f
	chartFilter.Day = ""
	var first time.Time
	visit(func(ref rowRef, r Row) {
		agents[r.Agent] = true
		if r.Provider != "" {
			providers[r.Provider] = true
		}
		addCallerRow(callers, r)
		addAccountRow(accounts, r)
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
			}
		}
		if r.IsRejected() {
			return
		}
		if keep || f.Day != "" && chartFilter.keeps(r.Record) {
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
		if g := f; names != nil {
			g.Computer = ""
			g.Day = ""
			if g.keeps(r.Record) {
				k := r.key("computer")
				if computers[k] == nil {
					computers[k] = &Share{ID: k}
				}
				computers[k].addRow(r)
			}
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
			if f.Day != "" {
				g.Day = ""
				if g.keeps(r.Record) {
					k := r.key(d)
					if chartGroups[d][k] == nil {
						chartGroups[d][k] = &Share{ID: k}
					}
					chartGroups[d][k].addRow(r)
				}
				g.Day = f.Day
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
	out.Accounts = callerGroups(accounts)
	for _, d := range Dimensions {
		out.By[d] = sharesOf(groups[d])
	}
	if f.Day != "" {
		out.ChartBy = map[string][]Share{}
		for _, d := range Dimensions {
			out.ChartBy[d] = sharesOf(chartGroups[d])
		}
	}
	out.Computers, out.Names = computerShares(computers, names)
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
		if !chartFilter.keeps(r.Record) || r.IsRejected() {
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
	callers, accounts := map[string]*Group{}, map[string]*Group{}
	for i := len(all.Rows) - 1; i >= 0; i-- {
		addCallerRow(callers, all.Rows[i])
		addAccountRow(accounts, all.Rows[i])
	}
	out.CallerKeys = callerGroups(callers)
	out.Accounts = callerGroups(accounts)
	offset = min(offset, len(l.Rows))
	out.Rows = l.Rows[offset:min(len(l.Rows), offset+limit)]
	chartRows := l.Rows
	if f.Day != "" {
		chartFilter := f
		chartFilter.Day = ""
		chartRows = all.Filtered(chartFilter).Rows
		out.ChartBy = map[string][]Share{}
	}
	out.Bucket, out.Series = LedgerSeries(p, chartRows)
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
		if f.Day != "" {
			g.Day = ""
			out.ChartBy[d] = Breakdown(all.Filtered(g).Rows, d)
		}
	}
	if names := sharedNames(); len(names) > 0 {
		g := f
		g.Computer = ""
		g.Day = ""
		computers := map[string]*Share{}
		for _, s := range Breakdown(all.Filtered(g).Rows, "computer") {
			computers[s.ID] = &s
		}
		out.Computers, out.Names = computerShares(computers, names)
	}
	return out
}

// computerShares are the computers' shares, this one and every other kept
// here among them though it made no call in the period; none without names.
func computerShares(m map[string]*Share, names map[string]string) ([]Share, map[string]string) {
	if names == nil {
		return nil, nil
	}
	for _, id := range append([]string{ThisComputer}, slices.Collect(maps.Keys(names))...) {
		if m[id] == nil {
			m[id] = &Share{ID: id}
		}
	}
	return sharesOf(m), names
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

// Account choices cover the period too: each account that answered a call,
// by provider, for the Account filter, whatever else is picked.
func addAccountRow(groups map[string]*Group, r Row) {
	who := r.Account()
	if who == "" || r.IsRejected() {
		return
	}
	id := r.Provider + "@" + who
	g := groups[id]
	if g == nil {
		g = &Group{ID: id, Provider: r.Provider, Account: who}
		groups[id] = g
	}
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
