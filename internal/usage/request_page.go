package usage

// The request page keeps compact per-source records, not a second full ledger.
// Unchanged sources are never decoded again. Aggregation streams those records;
// only the requested page is materialized/sorted. Export can still ask for the
// complete ledger explicitly. This cache is disposable and bounded.

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"hash/maphash"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	Purposes          []string // all purposes in the period, independent of the selected filter
	CallerKeys        []Group
	// Through and Direct are the rows the gateway served and the rows read
	// from the agents' own session files, which the gateway never saw. Sum
	// is the two together; the page shows the three beside each other so a
	// reader can tell what magpie carried from what it only read about.
	Through, Direct Totals
	// Reported are the calls a client told magpie of (#1874), which went
	// straight to their vendor: counted in Sum, apart from both of those.
	Reported Totals
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
	// Heat is the rows by day, as the chart's filter keeps them: only for
	// the heatmap's own period (HeatmapOf)
	Heat *Heatmap
}

type packedRow struct {
	// at is the row's time in unix ns, and zone its zone in rowZones
	// (Time): a time.Time was 24 of a row's bytes (#2278)
	at int64
	// Varied are the strings of rowVaried, and Shape the chunk's Shapes
	// entry that holds the row's other strings
	Varied [len(rowVaried)]uint32
	Shape  uint32
	// in 32 bits: no call reads or writes 2^31 tokens, and they were
	// 48 of a row's bytes as int64s (#2278); one past it is kept as the
	// most an int32 holds (tokensOf)
	Tokens         [6]int32
	Order, RouteID int64
	Cost, Credits  float64
	// the times of the call, in ms as in Record, kept in 32 bits (msOf):
	// a page holds every row of its period, and they were 36 of a row's
	// 216 bytes as int64s (#2278)
	Millis, TTFT, FirstText    int32
	Sent, Flow, FirstReasoning int32
	Own, Retried, Queued       int32
	Masking                    int32 // ms (Record.Masking)
	Status, Tries              int32
	Flags                      uint16
	zone                       uint16
}

// rowZones are the zones rows' times are in, by a row's zone; 0 is the
// zero time. A row held its *time.Location, and that one pointer made
// every []packedRow a slice the collector scans: the pages' and the kept
// buffers' hundreds of thousands of rows, on every cycle, while calls
// came in (#4737). A zone is known by its name and offset, not its
// pointer: a time parsed with an offset, or a chunk read from disk, has
// a new FixedZone each.
var rowZones struct {
	sync.Mutex
	list atomic.Pointer[[]*time.Location]
	ids  map[rowZoneKey]uint16
	seen map[*time.Location]uint16
}

type rowZoneKey struct {
	name   string
	offset int
}

// zoneUTC is UTC's id. time.Local is a zone as any other: a test sets
// it, and a row keeps the zone it was written in.
const zoneUTC = 1

func init() {
	list := []*time.Location{nil, time.UTC}
	rowZones.list.Store(&list)
	rowZones.ids = map[rowZoneKey]uint16{}
	rowZones.seen = map[*time.Location]uint16{}
}

// zoneOf is loc's id in rowZones, added when it is new. Past the most a
// row's zone holds, a new zone's times are kept in UTC: the same
// instants, read in another zone.
func zoneOf(loc *time.Location) uint16 {
	if loc == time.UTC || loc == nil {
		return zoneUTC
	}
	rowZones.Lock()
	defer rowZones.Unlock()
	if id, ok := rowZones.seen[loc]; ok {
		return id
	}
	_, offset := time.Unix(0, 0).In(loc).Zone()
	key := rowZoneKey{loc.String(), offset}
	id, ok := rowZones.ids[key]
	if !ok {
		list := *rowZones.list.Load()
		if len(list) > math.MaxUint16 {
			return zoneUTC
		}
		id = uint16(len(list))
		next := append(list[:len(list):len(list)], loc)
		rowZones.list.Store(&next)
		rowZones.ids[key] = id
	}
	if len(rowZones.seen) >= 1024 { // a pointer each parse: kept bounded
		clear(rowZones.seen)
	}
	rowZones.seen[loc] = id
	return id
}

// msOf is a time in ms as a packedRow keeps it: one past 24 days, which
// no call takes, is kept as the longest it can be.
func msOf(ms int64) int32 { return int32(max(min(ms, math.MaxInt32), math.MinInt32)) }

// tokensOf is a token count as a packedRow keeps it.
func tokensOf(n int64) int32 { return int32(max(min(n, math.MaxInt32), math.MinInt32)) }

// Time is the row's time, in the zone it was written in.
func (p *packedRow) Time() time.Time {
	if p.zone == 0 {
		return time.Time{}
	}
	return time.Unix(0, p.at).In((*rowZones.list.Load())[p.zone])
}

// setTime keeps t as the row's time. The zero time stays zero.
func (p *packedRow) setTime(t time.Time) {
	if t.IsZero() {
		p.at, p.zone = 0, 0
		return
	}
	p.setTimeIn(t, zoneOf(t.Location()))
}

// setTimeIn is setTime of a time in zone, which zoneOf gave.
func (p *packedRow) setTimeIn(t time.Time, zone uint16) {
	if t.IsZero() {
		p.at, p.zone = 0, 0
		return
	}
	p.at, p.zone = t.UnixNano(), zone
}

// rowHandedOn is the Flags bit of Record.HandedOn, rowReasoningHidden
// Record.ReasoningHidden's: 64 is block_price.go's swapKnown, 256
// rowOwnCost
const (
	rowHandedOn        = 128
	rowReasoningHidden = 512
	rowCompactionKnown = 1024
	rowCompaction      = 2048
)

// rowOwnCost marks a reported call that said what it cost (#1874): Cost is
// that, which the block pricer keeps, and reading the row back gives
// Record.OwnCost again.
const rowOwnCost = 256

// rowCredits marks a call whose reply said what it cost in its plan's
// credits (#2512): Credits is that, and reading the row back gives
// Record.Credits again.
const rowCredits = 4096

// rowCacheUnknown is the Flags bit of Record.CacheUnknown: its vendor
// counted no cache (#3464).
const rowCacheUnknown = 8192

type rowChunk struct {
	Archive   []byte
	Locations []*time.Location
	Rows      []packedRow
	Strings   []string
	// Shapes are the rows' strings but rowVaried's, each set kept once:
	// in every row they were 140 of its 328 bytes, and the page holds every
	// row of the period at once. 364k calls had 7300 shapes.
	Shapes           [][rowTexts]uint32
	shapeIDs         map[[rowTexts]uint32]uint32
	codexActors      codexActors
	codexCompactions map[int]*bool
	codexIdentities  map[string]sessions.CodexIdentity
	dict             map[string]uint32
	Source           sessions.CallSource
	Bytes            int64
	// used is the tick of the latest read that used the chunk, for trim:
	// a counter of its own, so a read marks a published chunk used without
	// writing the chunk under another read's page (use)
	used   *atomic.Uint64
	Count  int
	Latest time.Time
	// Computer is the other computer whose day this is (#542), by id
	Computer string
	// serial names the chunk as made, for the cells a page keeps of it
	// (chunkSerial)
	serial uint64
	// mapped is the file Archive was read from, when it is of its pages
	mapped *diskMapping
	// raw is the gateway block a chunk is priced of. lazy is the pricer of
	// one not priced yet (priceStubs), which only its serial and raw stand
	// for: a page whose settled days are kept never reads them (#2278).
	raw  *rowChunk
	lazy *blockPricer
}

// rowMsg is the Text of a row's Claude message id, after rowText's, and
// rowError its error's
const (
	rowMsg   = 35
	rowError = 9
	// rowClient is the Text of the client's own name, when the user counts
	// it as another agent (Row.Client, #1958)
	rowClient = 33
	// rowTexts is how many strings a row has: rowText's and the message id
	rowTexts = 36
)

// rowVaried are the strings that differ call by call: the error, the
// request and response ids, the archive and the message id. A row keeps
// them itself; every other string is the same for most of a chunk's rows.
var rowVaried = [...]int{rowError, 11, 22, 25, rowMsg}

// rowVariedAt is each Text slot's place in Varied, or -1
var rowVariedAt = func() (at [rowTexts]int8) {
	for i := range at {
		at[i] = -1
	}
	for k, slot := range rowVaried {
		at[slot] = int8(k)
	}
	return at
}()

// text is the string id of p's Text slot
func (c *rowChunk) text(p *packedRow, slot int) uint32 {
	if k := rowVariedAt[slot]; k >= 0 {
		return p.Varied[k]
	}
	return c.Shapes[p.Shape][slot]
}

// texts is every string id of p, by Text slot
func (c *rowChunk) texts(p *packedRow) [rowTexts]uint32 {
	t := c.Shapes[p.Shape]
	for k, slot := range rowVaried {
		t[slot] = p.Varied[k]
	}
	return t
}

// setTexts keeps t, every string id of p by Text slot, in p and Shapes
func (c *rowChunk) setTexts(p *packedRow, t [rowTexts]uint32) {
	for k, slot := range rowVaried {
		p.Varied[k], t[slot] = t[slot], 0
	}
	p.Shape = c.shape(t)
}

// shape is the Shapes entry of t, which is added when there is none
func (c *rowChunk) shape(t [rowTexts]uint32) uint32 {
	if c.shapeIDs == nil {
		c.shapeIDs = make(map[[rowTexts]uint32]uint32, len(c.Shapes))
		for i, s := range c.Shapes {
			c.shapeIDs[s] = uint32(i)
		}
	}
	id, ok := c.shapeIDs[t]
	if !ok {
		id = uint32(len(c.Shapes))
		c.Shapes = append(c.Shapes, t)
		c.shapeIDs[t] = id
		c.Bytes += int64(unsafe.Sizeof(t))
	}
	return id
}

// rowText is every string of a row's record, the upstream's own stop
// reason ("Upstream stop reason", #1927) and the service tiers (#1784)
// among them: a string left out comes back empty from the cache, in the
// page and in /v1/magpie/usage/requests (TestRowTextHoldsEveryString)
func rowText(r *Row) [35]*string {
	return [35]*string{&r.Agent, &r.Provider, &r.Host, &r.SessionProvider, &r.SessionAccount, &r.Model, &r.Requested, &r.Served, &r.Effort, &r.Error, &r.ErrType, &r.RequestID, &r.Endpoint, &r.Session, &r.NativeSession, &r.Kind, &r.Source, &r.Via, &r.ProviderKeyID, &r.ProviderKeyName, &r.CallerKeyID, &r.CallerKeyName, &r.Archive, &r.Operation, &r.ProviderAccount, &r.ResponseID, &r.Upstream, &r.Subagent, &r.ParentAgent, &r.Stop, &r.TierAsked, &r.TierSent, &r.TierServed, &r.Client, &r.CallerIssuerID}
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
	p := packedRow{Tokens: [6]int32{tokensOf(int64(r.Input)), tokensOf(int64(r.Output)), tokensOf(int64(r.CacheRead)), tokensOf(int64(r.CacheWrite)), tokensOf(int64(r.Reasoning)), tokensOf(int64(r.CacheWrite1h))}, Millis: msOf(r.Millis), TTFT: msOf(r.TTFT), FirstText: msOf(r.FirstText), FirstReasoning: msOf(r.FirstReasoning), Sent: msOf(r.Sent), Flow: msOf(r.Flow), Own: msOf(r.Own), Masking: int32(min(r.Masking, math.MaxInt32)), Retried: msOf(r.Retried), Queued: msOf(r.Queued), Order: order, RouteID: r.RouteID, Cost: r.Cost, Status: int32(r.Status), Tries: int32(r.Tries)}
	p.setTime(r.Time)
	var t [rowTexts]uint32
	for i, s := range rowText(&r) {
		t[i] = intern(*s)
	}
	t[rowMsg] = intern(msg)
	c.setTexts(&p, t)
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
	if r.ReasoningHidden {
		p.Flags |= rowReasoningHidden
	}
	if r.Routed {
		p.Flags |= 32
	}
	if r.CacheUnknown {
		p.Flags |= rowCacheUnknown
	}
	if failed {
		p.Flags |= 16
	}
	if r.HandedOn {
		p.Flags |= rowHandedOn
	}
	if r.OwnCost != nil {
		p.Flags |= rowOwnCost
		p.Cost = *r.OwnCost
	}
	if r.Credits != nil {
		p.Flags |= rowCredits
		p.Credits = *r.Credits
	}
	if r.Compaction != nil {
		p.Flags |= rowCompactionKnown
		if *r.Compaction {
			p.Flags |= rowCompaction
		}
	}
	c.Rows = append(c.Rows, p)
	if c.codexActors.ids != nil || r.codexActor != "" {
		before := c.codexActors.bytes()
		if c.codexActors.ids == nil {
			c.codexActors.ids = make([]uint32, len(c.Rows)-1)
		}
		c.codexActors.add(r.codexActor)
		c.Bytes += c.codexActors.bytes() - before
	}
	c.Count = len(c.Rows)
	if r.Time.After(c.Latest) {
		c.Latest = r.Time
	}
	c.Bytes += int64(unsafe.Sizeof(packedRow{}))
}
func (c *rowChunk) row(i int) Row {
	p := &c.Rows[i]
	r := Row{Record: Record{RouteID: p.RouteID, Time: p.Time(), Input: int(p.Tokens[0]), Output: int(p.Tokens[1]), CacheRead: int(p.Tokens[2]), CacheWrite: int(p.Tokens[3]), CacheWrite1h: int(p.Tokens[5]), Reasoning: int(p.Tokens[4]), Millis: int64(p.Millis), TTFT: int64(p.TTFT), FirstText: int64(p.FirstText), FirstReasoning: int64(p.FirstReasoning), ReasoningHidden: p.Flags&rowReasoningHidden != 0, CacheUnknown: p.Flags&rowCacheUnknown != 0, Sent: int64(p.Sent), Flow: int64(p.Flow), Own: int64(p.Own), Masking: int64(p.Masking), Retried: int64(p.Retried), Queued: int64(p.Queued), Status: int(p.Status), Tries: int(p.Tries), HandedOn: p.Flags&rowHandedOn != 0, Rejected: p.Flags&4 != 0, SessionOfficialLogin: p.Flags&8 != 0}, Cost: p.Cost, Priced: p.Flags&1 != 0, Swapped: p.Flags&2 != 0, Routed: p.Flags&32 != 0}
	if p.Flags&rowCredits != 0 {
		credits := p.Credits
		r.Credits = &credits
	}
	if p.Flags&rowCompactionKnown != 0 {
		compacted := p.Flags&rowCompaction != 0
		r.Compaction = &compacted
	}
	if compacted, ok := c.codexCompactions[i]; ok {
		if compacted == nil {
			r.Compaction = nil
		} else {
			value := *compacted
			r.Compaction = &value
		}
	}
	t := c.texts(p)
	for i, s := range rowText(&r) {
		*s = c.Strings[t[i]]
	}
	if r.Swapped && p.Flags&swapKnown == 0 && (SameSpelled(r.Model, r.Served) || ServingName(r.Model, r.Served) || RelayRenamed(r.Model, r.Served) || VendorAlias(r.Model, r.Served)) {
		r.Swapped = false // kept before a name spelled otherwise, a vendor's serving name, a relay's rename or the vendor's own alias was the same
	} else if r.Swapped && (RegionTail(r.Model, r.Served) || ListedAs(r.Provider, r.Model, r.Served)) {
		r.Swapped = false // a region's deployment of the model (kept before plugins#628), or the name the provider's list gives the model asked for
	}
	if r.cacheNotCounted() {
		r.CacheUnknown = true // kept before its plugin said its vendor counts no cache
	}
	if r.Source == SourceReported {
		r.Record.Source = SourceReported
		if p.Flags&rowOwnCost != 0 {
			own := p.Cost
			r.OwnCost = &own
		}
	}
	r.Computer = c.Computer
	if c.codexIdentities != nil {
		r = codexIdentityRow(r, c.codexActors.at(i), c.codexIdentities)
	}
	return r
}

func (c *rowChunk) setCodexCompaction(index int, compacted *bool) {
	if c.codexCompactions == nil {
		c.codexCompactions = make(map[int]*bool)
	}
	if compacted == nil {
		c.codexCompactions[index] = nil
		return
	}
	value := *compacted
	c.codexCompactions[index] = &value
}

type rowRef struct {
	Chunk *rowChunk
	Index int
}

func (r rowRef) row() Row { return r.Chunk.row(r.Index) }
func refNewer(a, b rowRef) bool {
	return timedOf(a, &a.Chunk.Rows[a.Index]).newer(timedOf(b, &b.Chunk.Rows[b.Index]))
}

// timedRef is a ref with what puts it in order, newest first: its row's
// time and order. A packed chunk's refs are put in order without its rows.
type timedRef struct {
	rowRef
	at    time.Time
	order int64
}

func timedOf(ref rowRef, p *packedRow) timedRef { return timedRef{ref, p.Time(), p.Order} }

func (a timedRef) newer(b timedRef) bool {
	if !a.at.Equal(b.at) {
		return a.at.After(b.at)
	}
	if (a.Chunk.Source.Path == "") != (b.Chunk.Source.Path == "") {
		return a.Chunk.Source.Path == ""
	}
	if a.Chunk.Source.Path != b.Chunk.Source.Path {
		return a.Chunk.Source.Path > b.Chunk.Source.Path
	}
	return a.order > b.order
}

type newestHeap []timedRef

func (h newestHeap) Len() int           { return len(h) }
func (h newestHeap) Less(i, j int) bool { return h[j].newer(h[i]) }
func (h newestHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *newestHeap) Push(v any)        { *h = append(*h, v.(timedRef)) }
func (h *newestHeap) Pop() any          { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }

// rowsIn is how many rows c has, packed or not.
func rowsIn(c *rowChunk) int {
	if c.lazy != nil {
		return rowsIn(c.raw)
	}
	if len(c.Archive) != 0 {
		return c.Count
	}
	return len(c.Rows)
}

// settledBefore is when the gateway's blocks a split page, heatmap or
// effort keeps the days of (pageSplit, heatSplit, effortSplit) end before.
func settledBefore(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()-heatRecent, 0, 0, 0, 0, now.Location()).Add(-heatReach)
}

// priceStubs prices the blocks of gateways left unpriced (rowChunk.lazy),
// in place, before their rows are read.
func priceStubs(gateways []*rowChunk) {
	for _, c := range gateways {
		if b := c.lazy; b != nil {
			if priced := b.block(c.raw); priced != nil {
				*c = *priced
			} else {
				*c = rowChunk{serial: c.serial, raw: c.raw}
			}
		}
	}
}

// chunkRows reads chunks' rows one chunk at a time. A page of the whole
// history held every session file's rows decoded at once, beside the
// packed copies it keeps: ~100 MB of a start that peaked at 508 MB over
// 420k calls. A packed chunk is decoded when a pass reaches it, into a
// buffer the next one's decode reuses, and refs still name the packed
// chunk. Its strings' and shapes' slices are reused the same way.
type chunkRows struct {
	c, open *rowChunk
	// lent: the strings are of the decoded block, not a copy of them, and
	// the next chunk is decoded over them. For a pass that keeps nothing of
	// a row past the next chunk, and clones a string it keeps: the passes
	// that pair a page's calls copied every chunk's strings five times,
	// ~250 MB of a warm start's page (#3493).
	lent bool
	b    *rowBuffer
}

// rowBuffer is what a chunkRows decodes into, kept between passes
// (keptRows): each of a page's passes over every chunk grew its own from
// nothing, and a warm start's page and Codex's effort after it allocated
// ~30 MB a pass to read the same rows (#3493).
type rowBuffer struct {
	rows    []packedRow
	strs    []string
	shapes  [][rowTexts]uint32
	decoded []byte
	open    rowChunk
}

// keptRows is the buffers passes put back, kept as matchScratch is. A
// sync.Pool kept them until the next collection, which a poll while an
// agent works runs several times a second, and the next pass grew one to
// the largest session chunk again: ~550 MB of the ~2.7 GB 90 s of polls
// allocated, in matchedBlocks alone (#2278). They go with the matcher's
// scratch, on ReleaseScratch or scratchLinger after the last pass.
var keptRows [4]atomic.Pointer[rowBuffer]

// rowBufferFor is a kept buffer, or a new one when every kept one is out
func rowBufferFor() *rowBuffer {
	for i := range keptRows {
		if b := keptRows[i].Swap(nil); b != nil {
			return b
		}
	}
	return new(rowBuffer)
}

// keepRows puts b back, unless as many are kept as keptRows holds
func keepRows(b *rowBuffer) {
	for i := range keptRows {
		if keptRows[i].CompareAndSwap(nil, b) {
			lingerScratch()
			return
		}
	}
}

// releaseRows lets go of every kept buffer
func releaseRows() {
	for i := range keptRows {
		keptRows[i].Store(nil)
	}
}

// lentRows is a chunkRows whose strings are lent (chunkRows.lent).
func lentRows() chunkRows { return chunkRows{lent: true} }

// of is c's rows to read. What the page set on c since (a Codex actor or
// compaction, the identities) is read through c.
func (r *chunkRows) of(c *rowChunk) *rowChunk {
	if len(c.Archive) == 0 {
		return c
	}
	if r.c != c {
		if r.b == nil {
			r.b = rowBufferFor()
		}
		b := r.b
		// the block is decoded into b's buffer either way: a pass that
		// copies its strings took one from a sync.Pool, which each
		// collection empties, and grew it again (#4372)
		open := c.unpackReusing(&b.open, b.rows, b.strs, b.shapes, &b.decoded, r.lent)
		if open == nil { // never written so: pack wrote it in this process
			b.open = rowChunk{Source: c.Source, Computer: c.Computer}
			open = &b.open
		}
		if cap(open.Rows) > cap(b.rows) {
			b.rows = open.Rows[:0]
		}
		if cap(open.Strings) > cap(b.strs) {
			b.strs = open.Strings[:0]
		}
		if cap(open.Shapes) > cap(b.shapes) {
			b.shapes = open.Shapes[:0]
		}
		r.c, r.open = c, open
	}
	r.open.codexActors, r.open.codexCompactions, r.open.codexIdentities = c.codexActors, c.codexCompactions, c.codexIdentities
	return r.open
}

// release hands r's buffers on to the next pass. Nothing r opened is read
// after: a lent string kept from it is a clone, and a row a copy.
func (r *chunkRows) release() {
	if b := r.b; b != nil {
		b.open = rowChunk{}
		clear(b.strs[:cap(b.strs)]) // the strings of a block not lent are its own
		r.b, r.c, r.open = nil, nil, nil
		keepRows(b)
	}
}

func (r *chunkRows) row(ref rowRef) Row { return r.of(ref.Chunk).row(ref.Index) }

// rowsOf reads the rows refs name, each chunk once, in the order of chunks.
func rowsOf(chunks []*rowChunk, refs []rowRef) map[rowRef]Row {
	out := make(map[rowRef]Row, len(refs))
	byChunk := map[*rowChunk][]int{}
	for _, ref := range refs {
		byChunk[ref.Chunk] = append(byChunk[ref.Chunk], ref.Index)
	}
	var rows chunkRows
	defer rows.release()
	for _, c := range chunks {
		indexes, ok := byChunk[c]
		if !ok {
			continue
		}
		delete(byChunk, c)
		open := rows.of(c)
		for _, i := range indexes {
			out[rowRef{c, i}] = open.row(i)
		}
	}
	for c, indexes := range byChunk { // a ref to a chunk not in chunks
		open := rows.of(c)
		for _, i := range indexes {
			out[rowRef{c, i}] = open.row(i)
		}
	}
	return out
}

type pageKey struct {
	Period        Period
	Filter        Filter
	Offset, Limit int
	// Date is today in the reader's zone (Filter.Zone): the request
	// state's key turns with this computer's day, and a reader in another
	// zone has a today of their own
	Date string
}
type requestIndex struct {
	sync.Mutex
	root, meta, key string
	chunks          map[string]*rowChunk
	pages           map[pageKey]RequestPage
	tick            uint64
	// flights are the pages being worked out: a second ask for one waits
	// for it rather than reading the whole history beside it (a refresh
	// pressed while a 750k-call All was still being read, Vantiboo1ean)
	flights map[flightKey]*pageFlight
	// heats are the heatmap's last pages, kept while the agents' session
	// files are all that changed (heatFresh)
	heats map[pageKey]heatPage
	// idle trims the chunks once the page goes unasked (keepWhileAsked)
	idle *time.Timer
	// parts are the cells of each chunk the last pages of a few partKeys
	// were added up from, by chunk serial: kept until what prices or
	// names a call changes (meta), and read without the lock, since a
	// part is never written once made
	parts map[partKey]map[uint64]*chunkPart
	// heatParts are the heatmap's sums of each chunk (heatOf), kept as
	// parts are but apart, so that the heatmap's never push a page's out
	heatParts map[partKey]map[uint64]*chunkPart
	// heatOld are the heatmap's days before its last few (heatSplit)
	heatOld map[partKey]*heatOld
	// pageOld are a page's cells before its last few days (pageSplit)
	pageOld map[partKey]*pageOld
	// pairs are the last pairing of each of a few periods (pairMemo),
	// kept as parts are
	pairs map[Period]*pairMemo
	// on are the paths the last read listed; unsaved counts the chunks
	// made since the file of them was last written (toDisk), and saving
	// says a write is under way; unsavedRows counts their rows, and
	// savedRows says a write was made for them (once a run)
	on          map[string]bool
	unsaved     int
	unsavedRows int
	saving      bool
	savedRows   bool
}

// heatPage is a heatmap's page, of the gateway log's snapshot version,
// worked out at.
type heatPage struct {
	page    RequestPage
	version uint64
	at      time.Time
}

// heatFresh is how long a heatmap is kept while only the agents' session
// files change. A live agent writes its session file all the time, so the
// page's key changed for every ask, and the Requests tab's refresh worked
// out a year of calls again each time: seconds of CPU over a long history
// (#3493). A year's calendar is the same a few seconds on; a change to
// what prices or names a call, or a call through magpie, is still shown at
// once.
var heatFresh = 30 * time.Second

// building, in tests, is told each time a page is worked out.
var building func()

type flightKey struct {
	key string
	q   pageKey
}
type pageFlight struct {
	done chan struct{}
	page RequestPage
}

var requestCache requestIndex

// requestCacheBytes bounds the session files' chunks kept while the
// Requests page is in use, and requestIdleBytes once it has gone unasked
// for requestIdle. Every new call is a new page key, so the open page
// is worked out again each refresh, and a chunk dropped is a shard read,
// decoded and priced again for each: at 24 MB a history of 416,000 calls
// (63 MB of chunks) read 2,679 shards again on every refresh, a third of
// its CPU (#3493). Idle, it is held to what it was before.
var (
	requestCacheBytes int64 = 128 << 20
	requestIdleBytes  int64 = 24 << 20
	requestIdle             = 2 * time.Minute
)

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

// requestState is what the calls read at now depend on: the gateway log's
// snapshot, the agents' call files, other computers' days, Codex's thread
// identities and what prices and names a call. A change to meta drops the
// request index's chunks; a change to key, its pages. Codex's thread
// identities are in key alone: a chunk's rows don't hold them, the page
// gives them to its rows as it is built, and every new Codex thread, a
// subagent's too, dropped every chunk, so the next page read the whole
// history again (#3493).
type requestState struct {
	sources         []sessions.CallSource
	codexIdentities map[string]sessions.CodexIdentity
	cfg             settings.Settings
	snapshot        *logSnapshot
	days            []sharedSource
	someShared      bool
	root, meta, key string
}

func readRequestState(now time.Time) requestState {
	sources, codexIdentities := sessions.CallSourcesAndCodexIdentities()
	slices.SortFunc(sources, func(a, b sessions.CallSource) int {
		if n := a.Modified.Compare(b.Modified); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	})
	ids, _ := json.Marshal(provider.SessionIdentities(sessions.CodexDir()))
	renamed := provider.UsageRenames().Key()
	cfg := settings.Load()
	prices, _ := json.Marshal(cfg.ModelPrices)
	wires, _ := json.Marshal(cfg.Wires())
	meta := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s", ids, renamed, prices, wires, statKey(provider.Path()), statKey(catalog.CachePath()), clientAgentsKey(), clearedKey())
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
	snapshot := readLogSnapshot()
	fmt.Fprint(h, meta, snapshot.version, now.In(time.Local).Format("2006-01-02 MST"))
	hashCodexIdentities(h, codexIdentities)
	for _, s := range sources {
		fmt.Fprintf(h, "%s:%d:%d;", s.Path, s.Size, s.Modified.UnixNano())
	}
	days, someShared := sharedSources(time.Time{})
	for _, s := range days {
		fmt.Fprintf(h, "%s:%d:%d;", s.path, s.size, s.mod.UnixNano())
	}
	return requestState{sources: sources, codexIdentities: codexIdentities, cfg: cfg, snapshot: snapshot, days: days, someShared: someShared,
		root: requestRoot(), meta: meta, key: fmt.Sprintf("%x", h.Sum(nil))}
}

// hashCodexIdentities writes ids into h in the order of their keys. They
// were json.Marshal()ed first, ~20k threads' identities encoded into a new
// buffer of several MB on every page and effort ask.
func hashCodexIdentities(h hash.Hash, ids map[string]sessions.CodexIdentity) {
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	b := make([]byte, 0, 32<<10)
	for _, k := range keys {
		id := ids[k]
		for _, s := range [...]string{k, id.ID, id.Role, id.ParentSession, id.RootSession} {
			b = binary.AppendUvarint(b, uint64(len(s)))
			b = append(b, s...)
		}
		if len(b) > 30<<10 {
			h.Write(b)
			b = b[:0]
		}
	}
	h.Write(b)
}

// requestRoot is the logs a request state is read from: another home's
// answer is no answer.
func requestRoot() string { return Path() + "|" + catalog.CachePath() }

func QueryPage(p Period, f Filter, offset, limit int) RequestPage {
	return queryPageOf(p, f, offset, limit, sessions.ReadCallSource, true)
}

// queryPage is QueryPage of the session files as readSource reads them,
// its chunks its own.
func queryPage(p Period, f Filter, offset, limit int, readSource func(sessions.CallSource) []sessions.Call) RequestPage {
	return queryPageOf(p, f, offset, limit, readSource, false)
}

// queryPageOf is queryPage; share says readSource is the files' own
// reader, so a chunk another query is making of a file is waited for
// (sharedBuild).
func queryPageOf(p Period, f Filter, offset, limit int, readSource func(sessions.CallSource) []sessions.Call, share bool) (page RequestPage) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	offset = max(0, offset)
	// this computer's zone is one key however it is named: the warm-up and
	// the tray ask with none, the Usage page with time.Local, and each
	// built and kept a page of its own, so the warm-up's warmed nothing the
	// page read and page-v1.json held both (#2278)
	if f.Zone == time.Local {
		f.Zone = nil
	}
	// The time is read once, so that the rows, the chart and the day the page
	// is kept for are of one day when midnight falls while it is worked out,
	// and read in the reader's zone, whose days and hours the page shows.
	now := Clock().In(f.loc())
	// A replacement reader remains useful to callers supplying synthetic logs.
	if LogCalls != nil {
		return pageFromLedgerAt(p, f, offset, limit, LedgerOfAt(p, Filter{}, now), now)
	}
	st := readRequestState(now)
	codexIdentities, snapshot, someShared := st.codexIdentities, st.snapshot, st.someShared
	version, key := snapshot.version, st.key
	idx := &requestCache
	idx.Lock()
	idx.keepWhileAsked()
	idx.adopt(st)
	q := pageKey{p, f, offset, limit, now.Format(time.DateOnly)}
	if page, ok := idx.pages[q]; ok {
		idx.Unlock()
		return page
	}
	if h, ok := idx.heats[q]; ok && h.version == version && time.Since(h.at) < heatFresh {
		idx.Unlock()
		return h.page
	}
	began := time.Now()
	fk := flightKey{key, q}
	if fl := idx.flights[fk]; fl != nil {
		idx.Unlock()
		<-fl.done
		return fl.page
	}
	if idx.flights == nil {
		idx.flights = map[flightKey]*pageFlight{}
	}
	fl := &pageFlight{done: make(chan struct{})}
	idx.flights[fk] = fl
	defer func() {
		requestCache.Lock()
		if requestCache.flights[fk] == fl {
			delete(requestCache.flights, fk)
		}
		requestCache.Unlock()
		fl.page = page
		close(fl.done)
	}()
	// a rolling window's start moves with now, so its cells are never the
	// same twice; the heatmap's sums are kept apart (heatParts)
	var cache *partCache
	pk := partKey{p: p, f: f, since: p.Since(now), until: p.Until(now), loc: now.Location()}
	if p == heatmapPeriod {
		cache = &partCache{old: idx.heatParts[pk], next: map[uint64]*chunkPart{}, pairs: idx.pairs[All], heatOld: idx.heatOld[pk], disk: heatDiskRef{root: idx.root, meta: idx.meta}}
	} else if p.Last() == 0 {
		cache = &partCache{old: idx.parts[pk], next: map[uint64]*chunkPart{}, pairs: idx.pairs[p], pageOld: idx.pageOld[pk], disk: heatDiskRef{root: idx.root, meta: idx.meta}}
	}
	shared := idx
	idx = shared.private()
	shared.Unlock()
	var settled time.Time
	if cache != nil {
		settled = settledBefore(now)
	}
	b := readRequestBlocks(idx, st, p, now, settled, readSource, share)
	defer b.priced.release() // on a panic too: a shared block is held until let go
	var names map[string]string
	if someShared {
		names = sharedNames()
	}
	if building != nil {
		building()
	}
	func() {
		defer holdBigBuild(b.gateways, b.chunks, b.others)() // on a panic too
		memo := p
		if p == heatmapPeriod {
			memo = All
		}
		if cache != nil {
			cache.pairs = shared.lastPairs(idx, memo)
		}
		page = buildRequestBlocksIn(p, f, offset, limit, now, b.gateways, b.chunks, b.others, names, codexIdentities, cache)
		if cache != nil && (p != heatmapPeriod || cache.pairsNext != nil) {
			shared.keepPairsFor(idx, memo, cache.pairsNext)
		}
	}()
	ownStrings(reflect.ValueOf(&page).Elem())
	b.priced.release()
	shared.Lock()
	defer shared.Unlock()
	if !shared.keep(idx, b.on) {
		return page
	}
	shared.noteBuilt(b)
	if p == heatmapPeriod {
		if shared.heatParts == nil || len(shared.heatParts) >= partSets && shared.heatParts[pk] == nil {
			shared.heatParts = map[partKey]map[uint64]*chunkPart{}
		}
		shared.heatParts[pk] = cache.next
		if shared.heatOld == nil || len(shared.heatOld) >= partSets && shared.heatOld[pk] == nil {
			shared.heatOld = map[partKey]*heatOld{}
		}
		shared.heatOld[pk] = cache.heatOldNext
	} else if cache != nil {
		if shared.parts == nil || len(shared.parts) >= partSets && shared.parts[pk] == nil {
			shared.parts = map[partKey]map[uint64]*chunkPart{}
		}
		shared.parts[pk] = cache.next
		if shared.pageOld == nil || len(shared.pageOld) >= partSets && shared.pageOld[pk] == nil {
			shared.pageOld = map[partKey]*pageOld{}
		}
		if cache.pageOldNext != nil {
			shared.pageOld[pk] = cache.pageOldNext
		}
	}
	if len(shared.pages) >= 16 {
		shared.pages = map[pageKey]RequestPage{}
	}
	shared.pages[q] = page
	if p == heatmapPeriod {
		if shared.heats == nil || len(shared.heats) >= 16 {
			shared.heats = map[pageKey]heatPage{}
		}
		shared.heats[q] = heatPage{page: page, version: version, at: began}
	}
	shared.trim(requestCacheBytes)
	return page
}

// codexEffortOfIndex is codexModelEffort of p's Codex rows, read through
// the request index as a page is: a build after a call reads only the
// session files that changed, where reading the ledger again opened every
// Codex shard there is (#3493).
func codexEffortOfIndex(st requestState, p Period, now time.Time) map[string][]sessions.ModelEffort {
	shared := &requestCache
	shared.Lock()
	shared.keepWhileAsked()
	shared.adopt(st)
	idx := shared.private()
	shared.Unlock()
	b := readRequestBlocks(idx, st, p, now, settledBefore(now), sessions.ReadCallSource, true)
	defer b.priced.release()
	sums := effortSums{}
	var next *pairMemo
	paired := false
	func() {
		defer holdBigBuild(b.gateways, b.chunks, b.others)()
		// the page of p just before, the warm-up's of All, paired these
		// rows: a build after it paired every session file's calls again
		// (#3493)
		memo := shared.lastPairs(idx, p)
		full := func() *pairedRows {
			var ps *pairedRows
			ps, next = pairRowsMemo(p, now, b.gateways, b.chunks, b.others, st.codexIdentities, memo)
			paired = true
			return ps
		}
		if !effortSplit(sums, p, now, b.gateways, b.chunks, b.others, st.codexIdentities, heatDiskRef{root: st.root, meta: st.meta}, full) {
			full().visitActors(func(r *Row) { sums.add(r) })
		}
		if paired {
			shared.keepPairsFor(idx, p, next)
		}
	}()
	b.priced.release()
	shared.Lock()
	if shared.keep(idx, b.on) {
		shared.noteBuilt(b)
		shared.trim(requestCacheBytes)
	}
	shared.Unlock()
	return sums.values()
}

// bigBuilds is held while a page pairs and adds up bigBuildRows rows or
// more. On a cold start the Usage page's heatmap and requests each paired
// a year of calls side by side, ~350 MB held at once and most of the
// footprint's peak (#2278). A smaller page, Today's, goes beside them.
var bigBuilds sync.Mutex

var bigBuildRows = 1 << 17

// holdBigBuild holds bigBuilds when the chunks have bigBuildRows rows or
// more, and is what lets it go.
func holdBigBuild(sets ...[]*rowChunk) (let func()) {
	if rowsOfAll(sets...) < bigBuildRows {
		return func() {}
	}
	bigBuilds.Lock()
	return bigBuilds.Unlock
}

// requestBlocks are what a page of a period is worked out from: the
// gateway's blocks of it, priced, the session files' chunks and the other
// computers' days, and the paths the index holds now (on).
type requestBlocks struct {
	gateways, chunks, others []*rowChunk
	on                       map[string]bool
	priced                   *blockPricer
	// built counts the session files' chunks made again, and builtRows
	// their rows
	built, builtRows int
}

// readRequestBlocks reads the blocks of p at now into idx, a private copy
// of the request index (private): a session file or another computer's
// day is read again only when it changed since its chunk was made. share
// says readSource is sessions.ReadCallSource: a file another query is
// making a chunk of now is waited for, not read beside it (sharedBuild).
// The gateway's blocks before settled are left to be priced when a pairing
// reads them (priceStubs).
func readRequestBlocks(idx *requestIndex, st requestState, p Period, now, settled time.Time, readSource func(sessions.CallSource) []sessions.Call, share bool) requestBlocks {
	sources, cfg, snapshot, days := st.sources, st.cfg, st.snapshot, st.days
	price := Pricer()
	// Swapped's names are a few hundred pairs over thousands of files' calls,
	// and its regexps were a third of reading them again (#3493). Each
	// reader of the session files has a pricer of its own, with its memos.
	pricers := func() func(Record, string) Row {
		price := Pricer()
		swapped := map[[4]string]bool{}
		return func(r Record, source string) Row {
			sent := r.Model
			if source == "" {
				sent = provider.SentNameIn(cfg.Wires(), r.priceID(), r.Model, r.Effort)
			}
			row := Row{Record: r, Source: source, Routed: GroupRouted(sent, r.Served)}
			if r.Served != "" {
				k := [4]string{r.priceID(), r.Model, sent, r.Served}
				s, ok := swapped[k]
				if !ok {
					s = Swapped(sent, r.Served) && !ListedAs(r.priceID(), r.Model, r.Served)
					swapped[k] = s
				}
				row.Swapped = s
			}
			if pr := price(r); pr != nil && r.Input+r.Output > 0 {
				row.Priced = true
				row.Cost = r.CostAt(*pr)
			}
			row.Agent, row.Client = reportedAs(r.Agent)
			return row
		}
	}
	priceRow := pricers()
	since := p.Since(now)
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	// The gateway's blocks are priced as they are read, from a memo of each
	// provider, model, served model and effort the block names: a priced
	// copy of each block kept beside it doubled the index, and over the
	// cache budget every query priced the whole history again.
	var gateways []*rowChunk
	renames := provider.UsageRenames()
	priced := newBlockPricer(renames, cfg.Wires(), price)
	meta := sha256.Sum256([]byte(st.root + "|" + st.meta))
	priced.share(meta)
	for _, packed := range snapshot.blocks {
		if !gatewaySince.IsZero() && packed.Latest.Before(gatewaySince) {
			continue
		}
		if len(packed.Archive) != 0 && packed.serial != 0 && packed.Latest.Before(settled) {
			// named as its priced copy is (heatSettledKey)
			gateways = append(gateways, &rowChunk{Source: packed.Source, Count: packed.Count, Latest: packed.Latest, Computer: packed.Computer, serial: packed.serial, raw: packed, lazy: priced})
			continue
		}
		if c := priced.block(packed); c != nil {
			gateways = append(gateways, c)
		}
	}
	if c := probeChunk(snapshot.probes, gatewaySince, renames, snapshot.hosts); c != nil {
		gateways = append(gateways, c) // listed, never counted (SourceProbe)
	}
	// made to size: a map of every session file grown a path at a time was
	// ~96 MB of a minute and a third of polls, and the list beside it (#2278)
	on := make(map[string]bool, len(sources))
	chunks := make([]*rowChunk, 0, len(sources))
	// made once a session file is to be read again, shared by the readers:
	// it is only read after
	resolver := sync.OnceValue(func() *sessionResolver { return newSessionResolver([]sessions.Call{{}}) })
	stale := func(s sessions.CallSource) bool {
		if !since.IsZero() && s.Modified.Before(since) {
			return false
		}
		c := idx.chunks[s.Path]
		return c == nil || c.Source.Size != s.Size || !c.Source.Modified.Equal(s.Modified)
	}
	want := map[string]sessions.CallSource{}
	for _, s := range sources {
		if stale(s) {
			want[s.Path] = s
		}
	}
	var spill *archiveSpill
	if len(want) >= requestDiskLeast {
		idx.fromDisk(want)
		spill = newArchiveSpill()
	}
	built, builtRows := 0, 0
	// A file's rows are made by the reader that read it: made one after
	// another, they were most of a cold build's wait (#3493).
	makeChunk := func() func(sessions.CallSource) *rowChunk {
		priceRow := pricers()
		build := func(s sessions.CallSource) *rowChunk {
			cs := readSource(s)
			c := &rowChunk{Source: s, Rows: make([]packedRow, 0, len(cs))}
			for _, call := range cs {
				r := logRecord(call)
				r.SessionAccount = resolver().resolve(call)
				r.SessionOfficialLogin = resolver().officialLogin(call, r.SessionAccount)
				r.SessionProvider = call.Upstream
				row := priceRow(r, "log")
				row.codexActor = codexActor(call)
				c.add(row, call.Msg, call.To, call.Error != "")
			}
			c.Bytes -= int64(32 * len(c.Strings))
			c.dict, c.shapeIDs = nil, nil // local chunks are immutable until the source changes
			return c.pack()
		}
		if !share {
			return build
		}
		return func(s sessions.CallSource) *rowChunk {
			return sharedBuild(chunkBuildKey{meta, s.Path, s.Size, s.Modified.UnixNano()}, func() *rowChunk { return build(s) })
		}
	}
	read := prefetchEach(sources, stale, makeChunk)
	for i, s := range sources {
		on[s.Path] = true
		if !since.IsZero() && s.Modified.Before(since) {
			continue
		}
		// kept packed: the page reads a chunk's rows when it reaches them
		// (chunkRows), not every file's at once
		c := idx.chunks[s.Path]
		put := false
		if reused := c != nil && c.Source.Size == s.Size && c.Source.Modified.Equal(s.Modified); !reused {
			c = read(i)
			c.serial = sourceSerial(st.root, st.meta, s)
			built++
			builtRows += c.Count
			c.used = useAt(idx.tick)
			put = spill.put(c, i)
		} else {
			c = idx.use(c)
		}
		idx.chunks[s.Path] = c
		chunks = append(chunks, c)
		if put {
			spill.hand(c)
		}
	}
	spill.done(func(i int) *rowChunk { return makeChunk()(sources[i]) })
	// the other computers' days (#542), a chunk each, read again only when
	// sync brought a new copy
	var others []*rowChunk
	from := ""
	if !since.IsZero() { // as sharedSources reads it
		from = since.AddDate(0, 0, -2).Format(time.DateOnly)
	}
	for _, s := range days {
		on[s.path] = true
		if s.day < from {
			continue
		}
		c := idx.chunks[s.path]
		if reused := c != nil && c.Source.Size == s.size && c.Source.Modified.Equal(s.mod); !reused {
			d, _ := readShared(s.path)
			c = &rowChunk{Source: sessions.CallSource{Path: s.path, Size: s.size, Modified: s.mod}, Computer: s.computer, Rows: make([]packedRow, 0, len(d.Calls))}
			for i, call := range d.Calls {
				r := call.Record
				r.Computer = ""
				r.renameBy(renames)
				c.add(priceRow(r, call.Source), "", int64(i), false)
			}
			c.Bytes -= int64(32 * len(c.Strings))
			c.dict, c.shapeIDs = nil, nil
			c = c.pack()
			c.serial = nextChunkSerial()
			c.used = useAt(idx.tick)
		} else {
			c = idx.use(c)
		}
		idx.chunks[s.path] = c
		others = append(others, c)
	}
	for path := range idx.chunks {
		if !on[path] {
			delete(idx.chunks, path)
		}
	}
	return requestBlocks{gateways: gateways, chunks: chunks, others: others, on: on, priced: priced, built: built, builtRows: builtRows}
}

// adopt drops what idx keeps of a state other than st: every chunk when
// what prices or names a call changed (meta), the pages when anything
// did (key). idx is locked.
// sourceSerial names a session file's chunk by what it is made of: the
// file's path, size and mtime under the index's root and meta, as a read reuses
// a chunk it keeps and joins another query's build of it (sharedBuild).
// A serial of each read's own made a cold start's builds, which read the
// files side by side, name every chunk apart: none found the pairing the
// one before it made (pairMemo), and the whole history was paired once
// for each, the footprint's peak (#2278). Its top bit keeps it apart from
// a counted one.
func sourceSerial(root, meta string, s sessions.CallSource) uint64 {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d", root, meta, s.Path, s.Size, s.Modified.UnixNano())
	return binary.BigEndian.Uint64(h.Sum(nil)) | 1<<63
}

// lastPairs is p's last pairing for read, idx's private copy, taken once
// the build of read has its turn (holdBigBuild). Taken when it began, a
// cold start's page of the month, heatmap, page of All and Codex's effort
// each found none, as they began together and paired in turn: the whole
// history was paired three times, each beside what the last kept, the
// footprint's peak (#2278). idx is not locked.
func (idx *requestIndex) lastPairs(read *requestIndex, p Period) *pairMemo {
	idx.Lock()
	defer idx.Unlock()
	if idx.root != read.root || idx.meta != read.meta {
		return nil
	}
	return idx.pairs[p]
}

// keepPairsFor keeps memo as p's last pairing for read, idx's private
// copy, while the build of read still has its turn (holdBigBuild): kept
// once it let go, as the page was, the build that had waited for it took
// its turn first and found none (#2278). The memo is kept though read
// isn't, as it stands only for chunks of its serials. idx is not locked.
func (idx *requestIndex) keepPairsFor(read *requestIndex, p Period, memo *pairMemo) {
	idx.Lock()
	defer idx.Unlock()
	if idx.root == read.root && idx.meta == read.meta {
		idx.keepPairs(p, memo)
	}
}

// keepPairs keeps memo as p's last pairing (nil: there is none to keep).
func (idx *requestIndex) keepPairs(p Period, memo *pairMemo) {
	if idx.pairs == nil || len(idx.pairs) >= partSets && idx.pairs[p] == nil {
		idx.pairs = map[Period]*pairMemo{}
	}
	if memo != nil {
		idx.pairs[p] = memo
	} else {
		delete(idx.pairs, p)
	}
}

func (idx *requestIndex) adopt(st requestState) {
	if idx.root != st.root || idx.meta != st.meta {
		idx.root, idx.meta = st.root, st.meta
		idx.chunks = map[string]*rowChunk{}
		idx.pages = nil
		idx.heats = nil
		idx.parts = nil
		idx.heatParts = nil
		idx.heatOld = nil
		idx.pageOld = nil
		idx.pairs = nil
		idx.key = ""
	}
	if idx.key != st.key {
		idx.pages = map[pageKey]RequestPage{}
		idx.key = st.key
	}
}

// use is c, a chunk of idx's, as used by this read. Both the map and the
// page take the one chunk: a copy for each was 13.5 MB of the live heap
// after 34k sessions' start (#2278). The read is marked on c's own counter,
// never by a copy: a copy of every published chunk on every read was
// 255 MB of 5.4 GB allocated in 80s of polls while a session was written
// (#2278).
func (idx *requestIndex) use(c *rowChunk) *rowChunk {
	if c.used == nil {
		next := *c
		next.used = useAt(idx.tick)
		return &next
	}
	for {
		was := c.used.Load()
		if was >= idx.tick || c.used.CompareAndSwap(was, idx.tick) {
			return c
		}
	}
}

// useAt is a chunk's use counter, made by a read of tick.
func useAt(tick uint64) *atomic.Uint64 {
	u := new(atomic.Uint64)
	u.Store(tick)
	return u
}

// lastUse is the tick of the latest read that used c, 0 for none.
func (c *rowChunk) lastUse() uint64 {
	if c.used == nil {
		return 0
	}
	return c.used.Load()
}

// private is a copy of idx's metadata and chunk map to read into without
// its lock: published chunks are immutable, and reading, decoding, pricing
// and aggregation use the copy. It counts a use of idx (tick). idx is
// locked.
func (idx *requestIndex) private() *requestIndex {
	idx.tick++
	return &requestIndex{root: idx.root, meta: idx.meta, key: idx.key, tick: idx.tick,
		chunks: maps.Clone(idx.chunks)}
}

// keep takes into idx the chunks read into read, its private copy, when
// they are of the state idx is in: a read of an older filesystem snapshot
// may finish after a newer one. on are the paths read holds. idx is
// locked.
func (idx *requestIndex) keep(read *requestIndex, on map[string]bool) bool {
	if idx.root != read.root || idx.meta != read.meta || idx.key != read.key {
		return false
	}
	for path, c := range read.chunks {
		prev := idx.chunks[path]
		if prev != nil && prev.lastUse() > c.lastUse() {
			continue
		}
		if prev != nil && prev.serial == c.serial && prev.pagesRank() > c.pagesRank() {
			// copied before the file of it was written (save): its archive
			// stays of the file's pages
			next := *c
			next.Archive, next.mapped = prev.Archive, prev.mapped
			c = &next
		}
		idx.chunks[path] = c
	}
	for path := range idx.chunks {
		if !on[path] {
			delete(idx.chunks, path)
		}
	}
	return true
}

// noteBuilt counts the chunks b made, and writes the file of them once
// they are requestDiskLeast or more (a first read of the history): fewer
// are a few shard reads at the next start, and not worth writing the
// whole file (~44MB for 15.6k sessions) again. Or once a run, when they
// hold requestDiskLeastRows rows: a few long sessions that ended since
// the last write (9 files, 84.6k calls, ~1s) were made again at every
// start, and the next ones that end joined them, until they were
// requestDiskLeast files. Once, as a session still at work grows with
// every call and is made again for each. idx is locked.
func (idx *requestIndex) noteBuilt(b requestBlocks) {
	idx.on = b.on
	idx.unsaved += b.built
	idx.unsavedRows += b.builtRows
	if idx.unsaved >= requestDiskLeast {
		idx.save()
	} else if !idx.savedRows && idx.unsavedRows >= requestDiskLeastRows {
		idx.savedRows = true
		idx.save()
	}
}

// requestDiskLeastRows is how many rows of chunks made since the last
// write have the file written once a run, whatever their files.
var requestDiskLeastRows = 20000

// requestDiskSaves are the writes under way, for the tests to wait on.
var requestDiskSaves sync.WaitGroup

// savingDisk, in tests, is called by a write before it writes the file.
var savingDisk func()

// save writes the file of idx's chunks behind the lock: chunks are never
// changed once made, so a copy of the map is enough. idx is locked.
func (idx *requestIndex) save() {
	if idx.saving {
		return
	}
	idx.saving = true
	idx.unsaved, idx.unsavedRows = 0, 0 // a write that fails is tried again only for new chunks
	root, meta, chunks, on := idx.root, idx.meta, maps.Clone(idx.chunks), idx.on
	requestDiskSaves.Add(1)
	go func() {
		defer requestDiskSaves.Done()
		if savingDisk != nil {
			savingDisk()
		}
		var kept map[string]*rowChunk
		if toDisk(root, meta, chunks, on) == nil {
			kept = onDisk(root, meta, chunks)
		}
		idx.Lock()
		defer idx.Unlock()
		idx.saving = false
		// a read since has copied the chunk with a use of its own: the same
		// chunk while its serial is
		for path, c := range kept {
			if cur := idx.chunks[path]; cur != nil && cur.serial == c.serial && cur.pagesRank() < 2 {
				next := *cur
				next.Archive, next.mapped = c.Archive, c.mapped
				idx.chunks[path] = &next
			}
		}
	}()
}

// keepWhileAsked holds the chunks to requestCacheBytes while the page is
// asked for, and to requestIdleBytes once it has gone unasked for
// requestIdle. idx is locked.
func (idx *requestIndex) keepWhileAsked() {
	if idx.idle != nil {
		idx.idle.Reset(requestIdle)
		return
	}
	idx.idle = time.AfterFunc(requestIdle, func() {
		idx.Lock()
		defer idx.Unlock()
		idx.trim(requestIdleBytes)
	})
}

// trim drops the chunks used longest ago until the rest are within bound.
// The session files' chunks are bounded; the gateway's index is not
// counted here, as it is kept whatever its size (log_index.go). idx is
// locked.
func (idx *requestIndex) trim(bound int64) {
	var bytes int64
	type cachedChunk struct {
		path string
		row  *rowChunk
	}
	kept := make([]cachedChunk, 0, len(idx.chunks))
	for path, c := range idx.chunks {
		bytes += c.Bytes
		kept = append(kept, cachedChunk{path: path, row: c})
	}
	slices.SortFunc(kept, func(a, b cachedChunk) int {
		if a, b := a.row.lastUse(), b.row.lastUse(); a != b {
			return cmp.Compare(a, b)
		}
		return strings.Compare(a.path, b.path)
	})
	for _, c := range kept {
		if bytes <= bound {
			break
		}
		delete(idx.chunks, c.path)
		bytes -= c.row.Bytes
	}
}

// localRefs visits first occurrences of Claude message IDs. It stores only
// references for matches, never a full hydrated row for every call.
func visibleLocal(chunks []*rowChunk) refSet {
	duplicates := refSet{}
	// a message id seen is kept as two 64-bit hashes of it, not a copy of
	// the decoded chunk's string: a long history's ~400k ids held ~7MB of
	// copies at the start's peak, and a key of these is a string header's
	// size. Two ids alike in both are ~1e-28 likely.
	// made to size, as many as the rows that can have an id (a Codex call
	// has none): grown an id at a time, it was ~250 MB of a minute and a
	// third of polls (#2278)
	keys := msgKeysOf(chunks)
	n := 0
	for _, k := range keys {
		// a settled chunk's are read below: the set grows for them
		if k != nil {
			n += len(k.keys)
		}
	}
	seen := keptSeen.Swap(nil)
	// judged by the chunks' rows, an upper bound n leaves the recent out of
	need := rowsOfAll(chunks)
	if seen == nil || !scratchFits(seen.made, need) {
		seen = &seenScratch{ids: make(map[[2]uint64]struct{}, n)}
	}
	seen.made = max(seen.made, need)
	defer keepSeen(seen)
	rows := lentRows()
	defer rows.release()
	var read chunkMsgKeys
	for i, c := range chunks {
		k := keys[i]
		if k == nil {
			read.keys = appendMsgKeys(read.keys[:0], rows.of(c))
			k = &read
		}
		for _, k := range k.keys {
			if _, ok := seen.ids[k.k]; ok {
				duplicates.add(rowRef{c, int(k.at)})
			} else {
				seen.ids[k.k] = struct{}{}
			}
		}
	}
	return duplicates
}

// keptSeen is visibleLocal's set of ids seen, kept empty for the next
// call as matchScratch is: made to size for every pairing it was ~210 MB
// of the ~2 GB 90 s of polls allocated (#2278). A cleared map keeps its
// buckets.
var keptSeen atomic.Pointer[seenScratch]

// seenScratch is the set and the ids of the largest call it served.
type seenScratch struct {
	ids  map[[2]uint64]struct{}
	made int
}

func keepSeen(seen *seenScratch) {
	clear(seen.ids)
	keptSeen.Store(seen)
	lingerScratch()
}

// msgKeys are a chunk's message ids as visibleLocal hashes them, each
// with its row, kept by the chunk's serial as matchHints are: a pairing
// decoded every session file of the period to read them, ~840 MB of the
// 1.1 GB visibleLocal allocated in 90s while an agent worked (#2278).
var msgKeys struct {
	sync.Mutex
	by   map[uint64]*chunkMsgKeys
	turn uint64
}

type chunkMsgKeys struct {
	keys []msgKey
	turn uint64
}

type msgKey struct {
	k  [2]uint64
	at int32
}

// msgKeysOpening, in tests, is told each chunk msgKeysOf decodes.
var msgKeysOpening func(*rowChunk)

// msgKeysOf are chunks' message keys, read from a chunk only when none
// are kept for its serial. A chunk settled before the days a split page
// pairs again (settledBefore) is left nil, for the caller to read on its
// own: it is paired only when what was counted of it is missed, and a
// cold start's pairing of the whole history kept all of them, ~67 MB of
// its ~460 MB live heap, beside the period's that polls read (#2278).
func msgKeysOf(chunks []*rowChunk) []*chunkMsgKeys {
	settled := settledBefore(time.Now())
	msgKeys.Lock()
	msgKeys.turn++
	turn := msgKeys.turn
	out := make([]*chunkMsgKeys, len(chunks))
	for i, c := range chunks {
		if k := msgKeys.by[c.serial]; c.serial != 0 && k != nil {
			k.turn = turn
			out[i] = k
		}
	}
	msgKeys.Unlock()
	rows := lentRows()
	defer rows.release()
	made := map[uint64]*chunkMsgKeys{}
	for i, c := range chunks {
		if out[i] != nil || !c.Latest.IsZero() && c.Latest.Before(settled) {
			continue
		}
		k := &chunkMsgKeys{turn: turn, keys: slices.Clip(appendMsgKeys(nil, rows.of(c)))}
		out[i] = k
		if c.serial != 0 {
			made[c.serial] = k
		}
	}
	msgKeys.Lock()
	if msgKeys.by == nil {
		msgKeys.by = map[uint64]*chunkMsgKeys{}
	}
	for serial, k := range made {
		msgKeys.by[serial] = k
	}
	for serial, k := range msgKeys.by {
		if k.turn+hintsKept < msgKeys.turn {
			delete(msgKeys.by, serial)
		}
	}
	msgKeys.Unlock()
	return out
}

// appendMsgKeys appends the keys of open's rows that have a message id.
func appendMsgKeys(keys []msgKey, open *rowChunk) []msgKey {
	if msgKeysOpening != nil {
		msgKeysOpening(open)
	}
	for j := range open.Rows {
		p := &open.Rows[j]
		msg := open.Strings[open.text(p, rowMsg)]
		if msg == "" {
			continue
		}
		keys = append(keys, msgKey{[2]uint64{maphash.String(msgSeeds[0], msg), maphash.String(msgSeeds[1], msg)}, int32(j)})
	}
	return keys
}

var msgSeeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}

// idCalls are the gateway's calls of one request id, in the gateway's
// order: first, then more, which most ids have none of. last is the place
// after the last local call found that names the id, 0 for none.
type idCalls struct {
	first rowRef
	more  []rowRef
	last  int32
}

func (c *idCalls) len() int { return 1 + len(c.more) }

func (c *idCalls) at(j int) rowRef {
	if j == 0 {
		return c.first
	}
	return c.more[j-1]
}

// idLocal is a local call that names a request id, and the place after
// the call found before it that names the id, 0 for none
type idLocal struct {
	before int32
	timedRef
}

// matchKey narrows candidates before comparing their end times. Request IDs
// are consumed first; fallback matching still requires uniqueness both ways.
// The agent isn't in it: a session's id names the one conversation, and a
// client built on Codex (miyi-helper, say) is logged under its own name by
// the gateway while its Codex session file names Codex, so its calls were
// counted twice, once through magpie and once as not.
type matchKey struct {
	session       string
	tokens        [3]int64
	failed, hasID bool
}

// sum is k as two hashes of it, ~1e-28 likely alike for two keys. The
// session is followed by bytes of a fixed length, so two keys never read
// as the same bytes.
func (k matchKey) sum() [2]uint64 {
	var tail [26]byte
	for i, t := range k.tokens {
		binary.LittleEndian.PutUint64(tail[8*i:], uint64(t))
	}
	if k.failed {
		tail[24] = 1
	}
	if k.hasID {
		tail[25] = 1
	}
	var out [2]uint64
	for i, seed := range matchKeySeeds {
		var h maphash.Hash
		h.SetSeed(seed)
		h.WriteString(k.session)
		h.Write(tail[:])
		out[i] = h.Sum64()
	}
	return out
}

var matchKeySeeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}

// matchTokens is what a call's tokens are matched by: its input with what it
// wrote to the cache in it, its output and what it read from the cache. The
// two logs needn't split a cache write out of the input alike (#589): a
// Codex that names cache_write_input_tokens beside a gateway record that
// didn't, an older Codex that doesn't beside one that does; the sum is the
// same either way.
func matchTokens(t [6]int32) [3]int64 {
	return [3]int64{int64(t[0]) + int64(t[3]), int64(t[1]), int64(t[2])}
}

type matchEnd struct {
	at    time.Time
	index rowRef
}

// matchGroup is a key's gateway calls, ends[off:off+n] of one slab, by
// their end; its range counts are counts[off+k:off+k+n+1] for the k-th
// group. A key holds a call or two, a slice each was ~1 GB of what 80 s
// of polls allocated while calls came in, on a year's history (#2278).
type matchGroup struct {
	off, n int32
}

// matchCandidate is a local call with one gateway call in its range, and
// that call's place in ends
type matchCandidate struct {
	ref rowRef
	at  int32
}

// matchScratch is what one matchedBlocks call works in, kept for the
// next. Every poll while an agent works matches a year's history again,
// and growing these afresh each time was ~1.9 of the ~2.2 GB the matcher
// allocated in 80 s of polls (#2278). A sync.Pool kept nothing: a poll
// collects several times a second, and each collection empties it. One is
// kept until ReleaseScratch, which the window calls once its reads rest,
// or until no pairing has used it for scratchLinger.
type matchScratch struct {
	made       int // the rows of the largest call it served
	reserved   refSet
	byID       map[string]int32
	calls      []idCalls
	ids        map[uint32]struct{}
	found      []idLocal
	used       refSet
	refs       []timedRef
	groupOf    map[[2]uint64]int32
	groups     []matchGroup
	ks         []int32
	ends       []matchEnd
	counts     []int32
	named      map[uint64]struct{}
	unique     []bool
	candidates []matchCandidate
}

var keptMatch atomic.Pointer[matchScratch]

// matchScratchFor is the kept scratch for a call over need rows, or a new
// one when another call has it, none is kept or the one kept doesn't fit
// (scratchFits)
func matchScratchFor(need int) *matchScratch {
	if s := keptMatch.Swap(nil); s != nil && scratchFits(s.made, need) {
		s.made = max(s.made, need)
		return s
	}
	return &matchScratch{made: need}
}

// scratchFits says a call over need rows takes a scratch kept from one
// over made. A kept scratch stays scratchLinger after the last call put it
// back, and the pairings of a poll's last few days kept the one a pairing
// of the whole history made: at a cold start ~140 MB of maps and slabs
// made for every call stayed while the window polled, the footprint at
// 490–530 MB from 44 s to the end of 75 s of polls, of a 548 MB peak
// (#2278). A call that much smaller makes its own, and the large one goes.
func scratchFits(made, need int) bool { return made <= 4*max(need, scratchFloor) }

// scratchFloor is the rows below which every call takes what is kept.
var scratchFloor = 1 << 12

// rowsOfAll is the rows of every chunk of sets, packed or not.
func rowsOfAll(sets ...[]*rowChunk) int {
	n := 0
	for _, set := range sets {
		for _, c := range set {
			n += rowsIn(c)
		}
	}
	return n
}

// mapFor is m, or a new map made for n when there is none yet
func mapFor[K comparable, V any](m map[K]V, n int) map[K]V {
	if m == nil {
		return make(map[K]V, n)
	}
	return m
}

// ReleaseScratch lets go of what the request matching keeps between calls,
// and the priced blocks no query holds.
func ReleaseScratch() {
	keptMatch.Store(nil)
	keptPicks.Store(nil)
	keptSeen.Store(nil)
	releaseRows()
	releasePriced()
}

// ReleaseMatchScratch is ReleaseScratch keeping the priced blocks.
func ReleaseMatchScratch() {
	keptMatch.Store(nil)
	keptPicks.Store(nil)
	keptSeen.Store(nil)
	releaseRows()
}

// scratchLinger is how long the matcher's scratch is kept once the last
// pairing put it back. While an agent works every poll pairs again and
// finds it kept; with nothing new to pair, a poll is answered from the
// pairings kept (pairMemo) and pairs nothing, and the scratch of one pairing
// of the whole history sat unused while the window polled: ~120 MB of the
// ~270 MB held over 60 s of polls on a 65 GB history (#2278).
var scratchLinger = 30 * time.Second

var scratchSweep struct {
	sync.Mutex
	t *time.Timer
}

// lingerScratch has the kept scratch let go scratchLinger after the last
// pairing put it back.
func lingerScratch() {
	scratchSweep.Lock()
	defer scratchSweep.Unlock()
	if scratchSweep.t == nil {
		scratchSweep.t = time.AfterFunc(scratchLinger, func() {
			keptMatch.Store(nil)
			keptPicks.Store(nil)
			keptSeen.Store(nil)
			releaseRows()
		})
		return
	}
	scratchSweep.t.Reset(scratchLinger)
}

// sized is s[:n], zeroed, in s's array when it holds n
func sized[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	s = s[:n]
	clear(s)
	return s
}

// release empties s, so it holds no chunk of this call, and keeps it
func (s *matchScratch) release() {
	clear(s.reserved)
	clear(s.byID)
	clear(s.ids)
	clear(s.used)
	clear(s.groupOf)
	clear(s.named)
	clear(s.calls)
	clear(s.found)
	clear(s.refs)
	clear(s.ends)
	clear(s.candidates)
	s.calls, s.found, s.refs, s.groups, s.ks = s.calls[:0], s.found[:0], s.refs[:0], s.groups[:0], s.ks[:0]
	s.ends, s.counts, s.unique, s.candidates = s.ends[:0], s.counts[:0], s.unique[:0], s.candidates[:0]
	keptMatch.Store(s)
	lingerScratch()
}

// agyMatchSession stands for the session of an Antigravity CLI (agy) call
// on both sides: agy sends the gateway no session header, so its gateway
// records name none, and its transcript's steps are matched to them by
// their tokens and time alone (#2097).
const agyMatchSession = "\x00agy"

// agyGateway says a gateway record of agent a, naming no session, may be
// an agy step's: agy's own, or the Antigravity app's. An agy not connected
// on its own row follows the app's gateway with the app's key
// (internal/agent/antigravity.go), so the gateway records its calls as the
// app's, and its transcript counted them a second time (Chamberlain_zyz on X).
func agyGateway(a string) bool { return a == "agy" || a == "antigravity" }

// agyStepLongest is how long after an agy step began its gateway call may
// have ended: a transcript's created_at is when the step began, to the
// second, where every other session file's time is when its call ended.
const agyStepLongest = 30 * time.Minute

func matchedLocal(gateway *rowChunk, chunks []*rowChunk, skip refSet, since time.Time) refSet {
	return matchedBlocks([]*rowChunk{gateway}, chunks, skip, nil, since, true)
}

func matchedBlocks(gateways []*rowChunk, chunks []*rowChunk, skip refSet, responsePairs map[rowRef]rowRef, since time.Time, newestIDs bool) refSet {
	scratch := matchScratchFor(rowsOfAll(gateways, chunks))
	defer scratch.release()
	scratch.reserved = mapFor(scratch.reserved, 0)
	reserved := scratch.reserved
	for gateway, local := range responsePairs {
		reserved.add(gateway)
		reserved.add(local)
	}
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	// A request id's gateway calls are its place in calls, and the local
	// calls that name it are found, not a slice an id each: over a year's
	// calls those slices and the maps' growth were ~1.5 GB of what two
	// minutes of polls allocated while an agent worked (#2278).
	matchable := func(gateway *rowChunk, i int) bool {
		p := &gateway.Rows[i]
		return !(p.Flags&4 != 0 || gateway.text(p, 1) == 0 && p.Status >= 400) && !p.Time().Before(gatewaySince)
	}
	withID := 0
	for _, gateway := range gateways {
		for i := range gateway.Rows {
			if gateway.text(&gateway.Rows[i], 11) != 0 && matchable(gateway, i) {
				withID++
			}
		}
	}
	scratch.byID = mapFor(scratch.byID, withID)
	byID := scratch.byID
	calls := slices.Grow(scratch.calls, withID)
	defer func() { scratch.calls = calls }()
	for _, gateway := range gateways {
		for i := range gateway.Rows {
			ref := rowRef{gateway, i}
			if reserved.has(ref) || !matchable(gateway, i) {
				continue
			}
			p := &gateway.Rows[i]
			if id := gateway.Strings[gateway.text(p, 11)]; id != "" {
				if k, ok := byID[id]; ok {
					calls[k].more = append(calls[k].more, ref)
				} else {
					byID[id] = int32(len(calls))
					calls = append(calls, idCalls{first: ref})
				}
			}
		}
	}
	// the local calls that name an id, chained from its calls[k].last in
	// the order they were found
	found := scratch.found
	defer func() { scratch.found = found }()
	rows := lentRows() // a key is looked up
	defer rows.release()
	hints := hintsOf(chunks, &rows)
	scratch.ids = mapFor(scratch.ids, len(byID))
	ids := scratch.ids
	for id := range byID {
		ids[uint32(matchHash(id))] = struct{}{}
	}
	for n, c := range chunks {
		if hints[n].latest.Before(since) || !namesAny(hints[n].ids, ids) {
			continue
		}
		if matchOpening != nil {
			matchOpening(c)
		}
		open := rows.of(c)
		for i := range open.Rows {
			p := &open.Rows[i]
			ref := rowRef{c, i}
			if skip.has(ref) || reserved.has(ref) || p.Time().Before(since) {
				continue
			}
			if open.text(p, 11) == 0 {
				continue
			}
			if k, ok := byID[open.Strings[open.text(p, 11)]]; ok {
				found = append(found, idLocal{calls[k].last, timedOf(ref, p)})
				calls[k].last = int32(len(found))
			}
		}
	}
	matched := refSet{}
	for _, local := range responsePairs {
		matched.add(local)
	}
	scratch.used = mapFor(scratch.used, 0)
	used := scratch.used
	refs := scratch.refs
	defer func() { scratch.refs = refs }()
	for k := range calls {
		refs = refs[:0]
		for j := calls[k].last; j != 0; j = found[j-1].before {
			refs = append(refs, found[j-1].timedRef)
		}
		slices.Reverse(refs)
		if newestIDs {
			slices.SortFunc(refs, func(a, b timedRef) int {
				if a.newer(b) {
					return -1
				}
				if b.newer(a) {
					return 1
				}
				return 0
			})
		}
		for j, ref := range refs {
			if j >= calls[k].len() {
				break
			}
			matched.add(ref.rowRef)
			used.add(calls[k].at(j))
		}
	}
	// keyOf is the key of a gateway call the local calls left without a
	// request id may match, ok false for one they never do
	keyOf := func(gateway *rowChunk, i int) (matchKey, bool) {
		if reserved.has(rowRef{gateway, i}) || used.has(rowRef{gateway, i}) {
			return matchKey{}, false
		}
		p := &gateway.Rows[i]
		if (p.Flags&4 != 0 || gateway.text(p, 1) == 0 && p.Status >= 400) || p.Time().Before(gatewaySince) {
			return matchKey{}, false
		}
		session := gateway.Strings[gateway.text(p, 14)]
		if session == "" {
			session = gateway.Strings[gateway.text(p, 13)]
		}
		if session == "" && agyGateway(gateway.Strings[gateway.text(p, 0)]) {
			session = agyMatchSession
		}
		if session == "" {
			return matchKey{}, false
		}
		return matchKey{session, matchTokens(p.Tokens), p.Status >= 400 || gateway.text(p, 9) != 0, gateway.text(p, 11) != 0}, true
	}
	kept := 0
	for _, gateway := range gateways {
		for i := range gateway.Rows {
			if _, ok := keyOf(gateway, i); ok {
				kept++
			}
		}
	}
	// a key's place in groups, by its sum: keyed by the key, a session
	// header, three counts and two flags, a whole history's calls held
	// ~29 MB of the start's peak, kept with the scratch (#2278)
	scratch.groupOf = mapFor(scratch.groupOf, kept)
	groupOf := scratch.groupOf
	// the sessions the groups name
	scratch.named = mapFor(scratch.named, 0)
	named := scratch.named
	groups := scratch.groups
	defer func() { scratch.groups = groups }()
	ks := slices.Grow(scratch.ks, kept) // each kept call's group, in the order read
	for _, gateway := range gateways {
		for i := range gateway.Rows {
			key, ok := keyOf(gateway, i)
			if !ok {
				continue
			}
			sum := key.sum()
			k, ok := groupOf[sum]
			if !ok {
				k = int32(len(groups))
				groupOf[sum] = k
				groups = append(groups, matchGroup{})
				named[matchHash(key.session)] = struct{}{}
			}
			groups[k].n++
			ks = append(ks, k)
		}
	}
	off := int32(0)
	for k := range groups {
		groups[k].off, off = off, off+groups[k].n
		groups[k].n = 0
	}
	ends := sized(scratch.ends, len(ks))
	scratch.ends, scratch.ks = ends, ks
	next := 0
	for _, gateway := range gateways {
		for i := range gateway.Rows {
			if _, ok := keyOf(gateway, i); !ok {
				continue
			}
			g := &groups[ks[next]]
			next++
			p := &gateway.Rows[i]
			ends[g.off+g.n] = matchEnd{p.Time().Add(time.Duration(p.Millis) * time.Millisecond), rowRef{gateway, i}}
			g.n++
		}
	}
	for k := range groups {
		g := groups[k]
		slices.SortFunc(ends[g.off:g.off+g.n], func(a, b matchEnd) int { return a.at.Compare(b.at) })
	}
	counts := sized(scratch.counts, len(ends)+len(groups))
	scratch.counts = counts
	candidates := scratch.candidates
	defer func() { scratch.candidates = candidates }()
	for n, chunk := range chunks {
		if hints[n].latest.Before(since) || !namesAny(hints[n].sessions, named) {
			continue
		}
		if matchOpening != nil {
			matchOpening(chunk)
		}
		c := rows.of(chunk)
		for j := range c.Rows {
			p := &c.Rows[j]
			ref := rowRef{chunk, j}
			if skip.has(ref) || reserved.has(ref) || matched.has(ref) || p.Time().Before(since) {
				continue
			}
			failed := p.Flags&16 != 0
			if p.Tokens[0]+p.Tokens[1]+p.Tokens[2]+p.Tokens[3] == 0 && !failed {
				continue
			}
			key := matchKey{session: c.Strings[c.text(p, 13)], tokens: matchTokens(p.Tokens), failed: failed}
			from, to := p.Time().Add(-2*time.Second), p.Time().Add(2*time.Second)
			if c.Strings[c.text(p, 0)] == "agy" {
				key.session, to = agyMatchSession, p.Time().Add(agyStepLongest)
			}
			count, at := 0, int32(0)
			// Without a local ID either gateway partition can match. With an ID,
			// only an unnamed gateway call can match (different IDs stay distinct).
			for _, hasID := range []bool{false, true} {
				if hasID && c.text(p, 11) != 0 {
					continue
				}
				key.hasID = hasID
				k, ok := groupOf[key.sum()]
				if !ok {
					continue
				}
				g := groups[k]
				in := ends[g.off : g.off+g.n]
				lo := sort.Search(len(in), func(i int) bool { return !in[i].at.Before(from) })
				hi := sort.Search(len(in), func(i int) bool { return in[i].at.After(to) })
				if hi > lo {
					count += hi - lo
					at = g.off + int32(lo)
					// Range counts preserve ambiguity even for thousands of identical
					// retries, without enumerating their Cartesian product.
					counts[g.off+k+int32(lo)]++
					counts[g.off+k+int32(hi)]--
				}
			}
			if count == 1 {
				candidates = append(candidates, matchCandidate{ref, at})
			}
		}
	}
	unique := sized(scratch.unique, len(ends))
	scratch.unique = unique
	for k := range groups {
		g := groups[k]
		count := int32(0)
		for i := int32(0); i < g.n; i++ {
			count += counts[g.off+int32(k)+i]
			if count == 1 {
				unique[g.off+i] = true
			}
		}
	}
	for _, c := range candidates {
		if unique[c.at] {
			matched.add(c.ref)
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
	return buildRequestBlocks(p, f, offset, limit, Clock(), []*rowChunk{gateway}, chunks, nil, nil)
}

// Shared days are never matched to this computer's gateway or session calls.
// now is the moment the page is asked at: its period and its chart's hours or
// days are read from it.
func buildRequestBlocks(p Period, f Filter, offset, limit int, now time.Time, gateways, chunks, others []*rowChunk, names map[string]string) RequestPage {
	return buildRequestBlocksWithCodex(p, f, offset, limit, now, gateways, chunks, others, names, sessions.CodexIdentities())
}

// buildRequestBlocksWithCodex's page owns its strings. A packed chunk is
// read through one block of strings at a time (chunkRows), and a row, an
// agent's name or a group kept by the page from it would hold that whole
// block: ~9 MB after the first page, kept by the request cache.
func buildRequestBlocksWithCodex(p Period, f Filter, offset, limit int, now time.Time, gateways, chunks, others []*rowChunk, names map[string]string, identities map[string]sessions.CodexIdentity) RequestPage {
	out := buildRequestBlocksOf(p, f, offset, limit, now, gateways, chunks, others, names, identities)
	ownStrings(reflect.ValueOf(&out).Elem())
	return out
}

// ownStrings copies every string v holds that can be set, map keys too.
func ownStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() && v.Len() > 0 {
			v.SetString(strings.Clone(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			ownStrings(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				ownStrings(f)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			ownStrings(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		m := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
			k.Set(it.Key())
			e.Set(it.Value())
			ownStrings(k)
			ownStrings(e)
			m.SetMapIndex(k, e)
		}
		v.Set(m)
	}
}

// pairedRows are the page's rows of p: each gateway call told the Codex
// thread of the session file's call it is paired with, and the session
// files' calls a gateway call matched, or another file's copy, left out
// (matched, skip). visit hands each row on in all's order. The chunks are
// copies, so the cached ones are left as they are. The pairs themselves
// are told the chunks and not kept: nothing read them, and a whole
// history's were held while its rows were visited (#2278).
type pairedRows struct {
	all           []*rowChunk
	skip, matched refSet
	since, until  time.Time
}

// pairing, in tests, is told each time pairRows pairs chunks' rows.
var pairing func()

func pairRows(p Period, now time.Time, gateways, chunks, others []*rowChunk, identities map[string]sessions.CodexIdentity) *pairedRows {
	return pairRowsIn(p.Since(now), p.Until(now), gateways, chunks, others, identities)
}

// pairRowsIn is pairRows over the rows from since to until.
func pairRowsIn(since, until time.Time, gateways, chunks, others []*rowChunk, identities map[string]sessions.CodexIdentity) *pairedRows {
	priceStubs(gateways)
	if pairing != nil {
		pairing()
	}
	gateways, chunks, others = cloneCodexChunks(gateways), cloneCodexChunks(chunks), cloneCodexChunks(others)
	duplicateSkip := foldCodexRows(chunks)
	pairs, compactions := codexChunkResponsePairs(gateways, chunks, duplicateSkip)
	for _, set := range [][]*rowChunk{gateways, chunks, others} {
		for _, chunk := range set {
			chunk.codexIdentities = identities
		}
	}
	skip := visibleLocal(chunks)
	for ref := range duplicateSkip.all() {
		skip.add(ref)
	}
	matched := matchedBlocks(gateways, chunks, skip, pairs, since, true)
	for gateway, local := range pairs {
		setCodexActor(gateway.Chunk, gateway.Index, local.Chunk.codexActors.at(local.Index))
		gateway.Chunk.setCodexCompaction(gateway.Index, compactions[local])
	}
	all := append(append(slices.Clone(gateways), chunks...), others...)
	return &pairedRows{all: all, skip: skip, matched: matched, since: since, until: until}
}

// visitActors is visit of the rows of the chunks a Codex thread is known
// for some row of (codexActors), the only rows Codex's effort adds up: it
// decoded every session file's chunk, ~90 MB of what eight Sessions polls
// allocated, for the few that are Codex's. The rows' strings are lent
// (lentRows), so fn clones what it keeps.
func (ps *pairedRows) visitActors(fn func(*Row)) {
	rows := lentRows()
	defer rows.release()
	var r Row
	for _, c := range ps.all {
		if c.codexActors.some() {
			if visitingActors != nil {
				visitingActors(c)
			}
			ps.visitChunk(&rows, c, &r, func(_ timedRef, r *Row) { fn(r) })
		}
	}
}

// visitingActors, in tests, is told each chunk visitActors reads.
var visitingActors func(*rowChunk)

// visitChunk is visit of c's rows alone, read through rows into r.
func (ps *pairedRows) visitChunk(rows *chunkRows, c *rowChunk, r *Row, fn func(timedRef, *Row)) {
	open := rows.of(c)
	for i := range open.Rows {
		pr := &open.Rows[i] // not a copy: a row is 216 bytes
		ref := rowRef{c, i}
		if pr.Time().Before(ps.since) || after(ps.until, pr.Time()) || ps.skip.has(ref) || ps.matched.has(ref) {
			continue
		}
		*r = open.row(i)
		fn(timedOf(ref, pr), r)
	}
}

func buildRequestBlocksOf(p Period, f Filter, offset, limit int, now time.Time, gateways, chunks, others []*rowChunk, names map[string]string, identities map[string]sessions.CodexIdentity) RequestPage {
	return buildRequestBlocksIn(p, f, offset, limit, now, gateways, chunks, others, names, identities, nil)
}

// buildRequestBlocksIn is the page added up from each chunk's cells,
// cache's for the chunks it has them for (nil: every chunk's read).
func buildRequestBlocksIn(p Period, f Filter, offset, limit int, now time.Time, gateways, chunks, others []*rowChunk, names map[string]string, identities map[string]sessions.CodexIdentity, cache *partCache) RequestPage {
	since := p.Since(now)
	chartFilter := f
	chartFilter.Day = ""
	// The heatmap's page is only its days, all HeatmapOf reads: over its
	// year the groups, shares and series below took seconds a call (#3493).
	if p == heatmapPeriod {
		return RequestPage{Rows: []Row{}, Agents: []string{}, Providers: []string{}, Purposes: []string{}, By: map[string][]Share{},
			Heat: heatSplit(since, now, gateways, chunks, others, chartFilter, identities, cache)}
	}
	out := RequestPage{Rows: []Row{}, Agents: []string{}, Providers: []string{}, Purposes: []string{}, By: map[string][]Share{}}
	agents, providers := map[string]bool{}, map[string]bool{}
	kinds := map[string]bool{}
	callers := map[string]*Group{}
	accounts := map[[2]string]*Group{}
	computers := map[string]*Share{}
	groups := map[string]map[string]*Share{}
	chartGroups := map[string]map[string]*Share{}
	seriesGroups := map[string]map[string]*Share{}
	keys := keyer{}
	for _, d := range Dimensions {
		groups[d] = map[string]*Share{}
		chartGroups[d] = map[string]*Share{}
		seriesGroups[d] = map[string]*Share{}
	}
	// Bound the requested prefix before adding limit: external offsets can
	// reach MaxInt, and a page beyond all sources needs no heap at all.
	maxRows := 0
	for _, set := range [][]*rowChunk{gateways, chunks, others} {
		for _, c := range set {
			maxRows += rowsIn(c)
		}
	}
	take := 0
	if offset < maxRows {
		take = offset + min(limit, maxRows-offset)
	}
	// a kept cell says which computer its rows are of whether this page
	// counts computers or not: the next one of its partKey may
	pass := newCellPass(p, f, now, names != nil || cache != nil)
	var ps *pairedRows
	var parts, newestParts []*chunkPart
	split := false
	if cache != nil {
		parts, ps, newestParts, split = pageSplit(p, f, now, take, pass, gateways, chunks, others, identities, cache)
	}
	if !split {
		if cache != nil {
			ps, cache.pairsNext = pairRowsMemo(p, now, gateways, chunks, others, identities, cache.pairs)
		} else {
			ps = pairRows(p, now, gateways, chunks, others, identities)
		}
		parts = ps.partsOf(pass, cache, identities)
		newestParts = parts
	}
	var first time.Time
	// Each dimension's groups are counted with its own pick cleared, and
	// the computers' with the computer's and day's (cellPass): a cell's
	// bits say which of them its rows are in, and its sums (d), made once,
	// are added to each (#3493).
	var r Row
	add := func(m map[string]*Share, k string, d *Totals) {
		s := m[k]
		if s == nil {
			s = &Share{ID: k}
			m[k] = s
		}
		s.plus(d)
	}
	for _, part := range parts {
		if !part.first.IsZero() && (first.IsZero() || part.first.Before(first)) {
			first = part.first
		}
		for i := range part.cells {
			cell := &part.cells[i]
			agent, kind, prov := cell.agent.Value(), cell.kind.Value(), cell.provider.Value()
			// looked up before they are set: most cells name what an
			// earlier one did, and a map's lookup is cheaper than its write
			if !agents[agent] {
				agents[agent] = true
			}
			if !kinds[kind] {
				kinds[kind] = true
			}
			if prov != "" && !providers[prov] {
				providers[prov] = true
			}
			keep := cell.bits&cellKeep != 0
			if keep {
				out.Total += cell.n
			}
			// a call turned away, and a request magpie sent on its own
			// (SourceProbe), are listed and counted in no total
			if cell.bits&cellUncounted != 0 {
				continue
			}
			d := &cell.d
			if keep {
				out.Sum.plus(d)
			}
			if id := cell.callerID.Value(); id != "" {
				g := callers[id]
				if g == nil {
					g = &Group{ID: id, CallerKeyID: id}
					callers[id] = g
				}
				g.CallerKeyName = cell.callerName.Value()
				g.plus(d)
			}
			if who := cell.account.Value(); who != "" {
				k := [2]string{prov, who}
				g := accounts[k]
				if g == nil {
					g = &Group{ID: prov + "@" + who, Provider: prov, Account: who}
					accounts[k] = g
				}
				g.plus(d)
			}
			// The two sources are counted with the Via pick cleared, as the
			// dimension groups are: the cells are what the reader switches
			// between, so each must go on saying its own number while
			// another is picked, or one of them would read 0 and there
			// would be no way back.
			if cell.bits&cellVia != 0 {
				switch cell.source {
				case cellDirect:
					out.Direct.plus(d)
				case cellReported:
					out.Reported.plus(d)
				default:
					out.Through.plus(d)
				}
			}
			cell.row(&r)
			if cell.bits&cellCharted != 0 {
				for _, dim := range Dimensions {
					add(seriesGroups[dim], keys.key(&r, dim), d)
				}
			}
			if cell.bits&cellComputer != 0 {
				add(computers, r.key("computer"), d)
			}
			for j, dim := range Dimensions {
				if cell.bits&(cellChart<<j) != 0 {
					add(chartGroups[dim], keys.key(&r, dim), d)
				}
				if cell.bits&(cellGroup<<j) != 0 {
					add(groups[dim], keys.key(&r, dim), d)
				}
			}
		}
	}
	selected, read := ps.newest(f, newestParts, take)
	slices.SortFunc(selected, func(a, b timedRef) int {
		if a.newer(b) {
			return -1
		}
		if b.newer(a) {
			return 1
		}
		return 0
	})
	var page []rowRef
	for _, ref := range selected[min(offset, len(selected)):] {
		page = append(page, ref.rowRef)
	}
	for _, ref := range page {
		out.Rows = append(out.Rows, read[ref])
	}
	for a := range agents {
		out.Agents = append(out.Agents, a)
	}
	slices.Sort(out.Agents)
	for a := range providers {
		out.Providers = append(out.Providers, a)
	}
	slices.Sort(out.Providers)
	purposes := map[string]bool{}
	for kind := range kinds {
		purposes[PurposeOf(kind)] = true
	}
	for purpose := range purposes {
		out.Purposes = append(out.Purposes, purpose)
	}
	slices.Sort(out.Purposes)
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
	var chartSince time.Time
	chartSince, out.Bucket, base = timeline(p, now, first)
	out.Series = make([]SeriesPoint, len(base))
	for i := range base {
		out.Series[i] = SeriesPoint{Point: base[i], By: map[string]map[string]Part{}}
		for _, d := range Dimensions {
			out.Series[i].By[d] = map[string]Part{}
		}
	}
	// a cell's bucket is its hour of the period, or its day: the chart of
	// days or weeks starts at the oldest charted call's, known only now
	sinceDay := dayNumber(chartSince)
	for _, part := range parts {
		for j := range part.cells {
			cell := &part.cells[j]
			if cell.bits&cellCharted == 0 || cell.bits&cellUncounted != 0 {
				continue
			}
			i := int(cell.bucket)
			if !pass.hours {
				i = int(cell.bucket - sinceDay)
				if i >= 0 && out.Bucket == "week" {
					i /= 7
				}
			}
			if i < 0 || i >= len(out.Series) {
				continue
			}
			pt := &out.Series[i]
			d := &cell.d
			pt.plus(d)
			cell.row(&r)
			for _, dim := range Dimensions {
				k := keys.key(&r, dim)
				part := pt.By[dim][k]
				part.plus(d)
				pt.By[dim][k] = part
			}
		}
	}
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
	return pageFromLedgerAt(p, f, offset, limit, all, Clock())
}

// pageFromLedgerAt is the page of a ledger read at now, its chart over the
// period read at the same moment.
func pageFromLedgerAt(p Period, f Filter, offset, limit int, all Ledgered, now time.Time) RequestPage {
	l := all.Filtered(f)
	out := RequestPage{Sum: l.Sum, Total: len(l.Rows), Agents: l.Agents, Providers: l.Providers, Purposes: []string{}, By: map[string][]Share{}}
	// the same split the streaming path makes, so both pages of the ledger
	// agree: "log" is a call read from an agent's own session file, which
	// the gateway never saw; the rest went through it. Counted with the source
	// pick cleared, as the streaming path does, so the three cells stay
	// switchable whichever one is picked.
	viaFilter := f
	viaFilter.Through = ""
	for _, r := range all.Rows {
		if r.IsRejected() || !viaFilter.keepsRow(&r) {
			continue
		}
		switch r.Source {
		case "log":
			out.Direct.addRow(r)
		case SourceReported:
			out.Reported.addRow(r)
		default:
			out.Through.addRow(r)
		}
	}
	callers, accounts := map[string]*Group{}, map[[2]string]*Group{}
	purposes := map[string]bool{}
	for i := len(all.Rows) - 1; i >= 0; i-- {
		purposes[PurposeOf(all.Rows[i].Kind)] = true
		var d Totals
		d.addRow(all.Rows[i])
		addCallerRow(callers, &all.Rows[i], &d)
		addAccountRow(accounts, &all.Rows[i], &d)
	}
	for purpose := range purposes {
		out.Purposes = append(out.Purposes, purpose)
	}
	slices.Sort(out.Purposes)
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
	out.Bucket, out.Series = ledgerSeriesAt(p, chartRows, now)
	if p == heatmapPeriod {
		out.Heat = heatmapOf(p.Since(now), now, func(add func(Row)) {
			for _, r := range chartRows {
				add(r)
			}
		})
	}
	for _, d := range Dimensions {
		g := f.Without(d)
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
func addCallerRow(groups map[string]*Group, r *Row, d *Totals) {
	if r.CallerKeyID == "" || r.IsRejected() {
		return
	}
	g := groups[r.CallerKeyID]
	if g == nil {
		g = &Group{ID: r.CallerKeyID, CallerKeyID: r.CallerKeyID}
		groups[r.CallerKeyID] = g
	}
	g.CallerKeyName = r.CallerKeyName
	g.plus(d)
}

// Account choices cover the period too: each account that answered a call,
// by provider, for the Account filter, whatever else is picked.
func addAccountRow(groups map[[2]string]*Group, r *Row, d *Totals) {
	who := r.Account()
	if who == "" || r.IsRejected() {
		return
	}
	k := [2]string{r.Provider, who}
	g := groups[k]
	if g == nil {
		g = &Group{ID: r.Provider + "@" + who, Provider: r.Provider, Account: who}
		groups[k] = g
	}
	g.plus(d)
}

func callerGroups[K comparable](groups map[K]*Group) []Group {
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

// prefetchEach reads the session files a query has to parse a few at a
// time, ahead of the loop that takes them in order. The first All after
// magpie starts parses every one of them, and one by one that was a wait on
// the disk for each: Requests stayed a skeleton for 40 s over 12k Codex
// sessions. read(i) gives what a make from newMake made of source i; at
// most a window of them waits to be taken, so the history is never all
// held at once. A source that wasn't wanted is read when it is asked for.
// Each reader, and the loop for a source not wanted, has a make of its
// own, so what a make keeps between sources needs no lock.
func prefetchEach[T any](sources []sessions.CallSource, want func(sessions.CallSource) bool, newMake func() func(sessions.CallSource) T) func(int) T {
	type slot struct {
		done chan struct{}
		v    T
	}
	slots := map[int]*slot{}
	var order []int
	for i, s := range sources {
		if want(s) {
			slots[i] = &slot{done: make(chan struct{})}
			order = append(order, i)
		}
	}
	var own func(sessions.CallSource) T
	alone := func(i int) T {
		if own == nil {
			own = newMake()
		}
		return own(sources[i])
	}
	if len(order) < 2 {
		return alone
	}
	workers := min(8, len(order))
	window := make(chan struct{}, 4*workers)
	next := make(chan int)
	go func() {
		for _, i := range order {
			window <- struct{}{}
			next <- i
		}
		close(next)
	}()
	for range workers {
		go func() {
			build := newMake()
			for i := range next {
				s := slots[i]
				s.v = build(sources[i])
				close(s.done)
			}
		}()
	}
	return func(i int) T {
		s := slots[i]
		if s == nil {
			return alone(i)
		}
		<-s.done
		<-window
		v := s.v
		var zero T
		s.v = zero
		return v
	}
}
