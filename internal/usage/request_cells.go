package usage

import (
	"cmp"
	"container/heap"
	"hash/maphash"
	"reflect"
	"slices"
	"sync/atomic"
	"time"
	"unique"

	"github.com/yetone/magpie/internal/sessions"
)

// A page's sums are made of each chunk's own: its rows told apart by all a
// sum, a group or a point of the chart reads of them (pageCell). A call
// changes one session file's chunk, or the gateway's last block, and every
// other chunk's cells are the same as on the page before, so a page built
// after a call adds up the cells kept for those (partCache) and reads only
// the chunks that changed. Reading every row of the history again was ~1s a
// refresh of All over 420k calls, every few seconds a live agent wrote
// (#3493).

// cellDims is how many Dimensions there are: a cell has a bit for each.
const cellDims = 5

func init() {
	if len(Dimensions) != cellDims {
		panic("usage: cellDims is not len(Dimensions)")
	}
}

// chunkSerial names a chunk as it was made: a packed session file's, another
// computer's day, a frozen block of the gateway's. Its copies keep it, and
// nothing a page reads of a chunk changes while it has it; 0 is a chunk
// kept for no page (a probe's, a test's).
var chunkSerial atomic.Uint64

func nextChunkSerial() uint64 { return chunkSerial.Add(1) }

// a cell's bits
const (
	cellKeep      = 1 << iota             // the filter keeps its rows
	cellUncounted                         // turned away or a probe: listed, in no sum
	cellVia                               // kept with the source pick cleared
	cellCharted                           // in the chart
	cellComputer                          // in the computers' shares
	cellGroup                             // and on, one a dimension: in its groups
	cellChart     = cellGroup << cellDims // one a dimension: in its ChartBy
)

// cell sources, as the source cells count them
const (
	cellThrough = iota
	cellDirect
	cellReported
)

// cellRow is what a cell tells its rows apart by, as the chunk's strings.
type cellRow struct {
	provider, agent, model, kind, callerID, callerName, account, computer string
	source                                                                uint8
	bits                                                                  uint16
	// bucket is the chart's hour from the period's start (-1 before it),
	// or the row's day (dayNumber) for a chart of days or weeks
	bucket int32
}

// pageCell is a chunk's rows of one cellRow: n of them, d their sums. Its
// strings are unique's, so the cells kept hold no chunk's strings.
type pageCell struct {
	provider, agent, model, kind, callerID, callerName, account, computer unique.Handle[string]
	source                                                                uint8
	bits                                                                  uint16
	bucket                                                                int32
	n                                                                     int
	d                                                                     Totals
}

// chunkPart is a chunk's cells, in the order of their rows' last (so the
// last row's caller key name is the one shown, as before), for the state
// sig is of (pairedRows.sig).
type chunkPart struct {
	sig   uint64
	cells []pageCell
	kept  int       // rows the filter keeps
	first time.Time // the oldest charted row's time
	// days are the heatmap's sums of the chunk, oldest first (heatOf)
	days []Day
	// speeds are the counted requests of the rows the filter keeps, by
	// model (speed_rank.go)
	speeds speedRank
	// big is the request of the rows the filter keeps that wrote the most
	// to the cache (BigWrite)
	big BigWrite
	// newest is the chunk's newest rows a page read of it (newest)
	newest atomic.Pointer[chunkNewest]
}

// partKey is what a chunk's cells are of besides the chunk: the page's
// period at now and its filter.
type partKey struct {
	p            Period
	f            Filter
	since, until time.Time
	loc          *time.Location
}

// partCache is the cells a page is built with: old, those kept by the
// page of the same partKey before, next, those of this page's chunks, which
// the index keeps for the next.
type partCache struct {
	old, next map[uint64]*chunkPart
	// pairs is the last pairing of the period, and pairsNext this page's
	pairs, pairsNext *pairMemo
	// heatOld is the heatmap's days before its last few the page before
	// kept, and heatOldNext this page's (heatSplit)
	heatOld, heatOldNext *heatOld
	// pageOld is a page's cells before its last few days the page before
	// kept, and pageOldNext this page's (pageSplit)
	pageOld, pageOldNext *pageOld
	// disk is where heatSplit and pageSplit keep what is settled between
	// runs
	disk heatDiskRef
}

// partSets bounds the partKeys kept: a page is open on one period and
// filter, and its card and the Sessions page ask for a few more.
const partSets = 4

// dayNumber is t's day as a count of days, as calendarDays counts them.
func dayNumber(t time.Time) int32 {
	y, m, d := t.Date()
	return int32(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// cellPass is how a page reads a row into its cell.
type cellPass struct {
	f, chart, via, computer Filter
	without, withoutDay     [cellDims]Filter
	unpicked                [cellDims]bool
	computerUnpicked, names bool
	hours                   bool
	since                   time.Time
	loc                     *time.Location
}

func newCellPass(p Period, f Filter, now time.Time, names bool) *cellPass {
	c := &cellPass{f: f, chart: f, via: f, computer: f, names: names, since: p.Since(now), loc: now.Location()}
	c.chart.Day = ""
	c.via.Through = ""
	c.computer.Computer, c.computer.Day = "", ""
	c.computerUnpicked = c.computer == f
	for i, d := range Dimensions {
		c.without[i] = f.Without(d)
		c.unpicked[i] = c.without[i] == f
		c.withoutDay[i] = c.without[i]
		c.withoutDay[i].Day = ""
	}
	_, bucket, _ := timeline(p, now, time.Time{})
	c.hours = bucket == "hour"
	return c
}

// part is c's cells, read through rows into r.
func (pass *cellPass) part(ps *pairedRows, rows *chunkRows, c *rowChunk, r *Row) *chunkPart {
	part := &chunkPart{}
	at := map[cellRow]int{}
	var raw []cellRow
	var cells []pageCell
	var last []int
	n := 0
	ps.visitChunk(rows, c, r, func(_ timedRef, r *Row) {
		n++
		f := &pass.f
		uncounted := r.IsRejected() || r.IsProbe()
		keep := f.keepsRow(r)
		k := cellRow{provider: r.Provider, agent: r.Agent, kind: r.Kind}
		if keep {
			k.bits |= cellKeep
			part.kept++
		}
		if uncounted {
			k.bits |= cellUncounted
		} else {
			k.model, k.callerID, k.callerName, k.account, k.computer = r.Model, r.CallerKeyID, r.CallerKeyName, r.Account(), r.Computer
			switch r.Source {
			case "log":
				k.source = cellDirect
			case SourceReported:
				k.source = cellReported
			default:
				k.source = cellThrough
			}
			if pass.via.keepsRow(r) {
				k.bits |= cellVia
			}
			if keep || f.Day != "" && pass.chart.keepsRow(r) {
				k.bits |= cellCharted
				if part.first.IsZero() || r.Time.Before(part.first) {
					part.first = r.Time
				}
				t := r.Time.In(pass.loc)
				switch {
				case !pass.hours:
					k.bucket = dayNumber(t)
				case t.Before(pass.since):
					k.bucket = -1
				default:
					k.bucket = int32(t.Sub(pass.since).Hours())
				}
			}
			if pass.names && (pass.computerUnpicked && keep || !pass.computerUnpicked && pass.computer.keepsRow(r)) {
				k.bits |= cellComputer
			}
			for i := range cellDims {
				if f.Day != "" && pass.withoutDay[i].keepsRow(r) {
					k.bits |= cellChart << i
				}
				if pass.unpicked[i] && keep || !pass.unpicked[i] && pass.without[i].keepsRow(r) {
					k.bits |= cellGroup << i
				}
			}
		}
		i, ok := at[k]
		if !ok {
			i = len(cells)
			at[k] = i
			raw = append(raw, k)
			cells = append(cells, pageCell{source: k.source, bits: k.bits, bucket: k.bucket})
			last = append(last, 0)
		}
		cells[i].n++
		last[i] = n
		if !uncounted {
			cells[i].d.addRow(*r)
			if keep {
				if part.speeds == nil {
					part.speeds = speedRank{}
				}
				part.speeds.add(r)
				part.big.add(r)
			}
		}
	})
	for i := range cells {
		k, cell := &raw[i], &cells[i]
		cell.provider, cell.agent, cell.model, cell.kind = unique.Make(k.provider), unique.Make(k.agent), unique.Make(k.model), unique.Make(k.kind)
		cell.callerID, cell.callerName, cell.account, cell.computer = unique.Make(k.callerID), unique.Make(k.callerName), unique.Make(k.account), unique.Make(k.computer)
	}
	order := make([]int, len(cells))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return cmp.Compare(last[a], last[b]) })
	part.cells = make([]pageCell, len(cells))
	for i, j := range order {
		part.cells[i] = cells[j]
	}
	return part
}

// row is the cell's names as a row has them, for Row.key.
func (c *pageCell) row(r *Row) {
	r.Provider, r.Agent, r.Model = c.provider.Value(), c.agent.Value(), c.model.Value()
	r.CallerKeyID, r.Computer = c.callerID.Value(), c.computer.Value()
}

var sigSeed = maphash.MakeSeed()

// mix is a well-spread hash of a few numbers, summed into a signature
// whatever order a map hands them in.
func mix(vs ...uint64) uint64 {
	h := uint64(0x9e3779b97f4a7c15)
	for _, v := range vs {
		h ^= v
		h *= 0xbf58476d1ce4e5b9
		h ^= h >> 31
		h *= 0x94d049bb133111eb
		h ^= h >> 29
	}
	return h
}

// sigs is what each chunk's rows read on this page beyond the chunk
// itself: the rows another one stands for (skip, matched), the Codex
// thread each is of (pairs, a session file's own) and what is known of
// that thread, and each compaction a gateway call was told. Two pages
// whose chunk has one serial and one sig read the same rows from it.
func (ps *pairedRows) sigs(identities map[string]sessions.CodexIdentity) map[*rowChunk]uint64 {
	out := make(map[*rowChunk]uint64, len(ps.all))
	for ref := range ps.skip.all() {
		out[ref.Chunk] += mix(1, uint64(ref.Index))
	}
	for ref := range ps.matched.all() {
		out[ref.Chunk] += mix(2, uint64(ref.Index))
	}
	actors := map[string]uint64{}
	actor := func(a string) uint64 {
		h, ok := actors[a]
		if ok {
			return h
		}
		id := identities[a]
		parent := identities[id.ParentSession]
		h = mix(maphash.String(sigSeed, a), maphash.String(sigSeed, id.ID), maphash.String(sigSeed, id.Role), maphash.String(sigSeed, id.RootSession),
			maphash.String(sigSeed, id.ParentSession), maphash.String(sigSeed, parent.ID), maphash.String(sigSeed, parent.Role))
		actors[a] = h
		return h
	}
	for _, c := range ps.all {
		h := out[c]
		if c.codexIdentities != nil {
			h += mix(3)
		}
		for i := range c.codexActors.len() {
			if a := c.codexActors.at(i); a != "" {
				h += mix(4, uint64(i), actor(a))
			}
		}
		for i, compacted := range c.codexCompactions {
			v := uint64(2)
			if compacted != nil && *compacted {
				v = 1
			} else if compacted != nil {
				v = 0
			}
			h += mix(5, uint64(i), v)
		}
		out[c] = h
	}
	return out
}

// partsOf are the cells of each of ps's chunks, in all's order: cache's,
// when it has them for the chunk as this page reads it, else read.
func (ps *pairedRows) partsOf(pass *cellPass, cache *partCache, identities map[string]sessions.CodexIdentity) []*chunkPart {
	sigs := ps.sigs(identities)
	parts := make([]*chunkPart, len(ps.all))
	rows := lentRows() // a cell keeps unique's handles, not the rows' strings
	defer rows.release()
	var r Row
	for i, c := range ps.all {
		sig := sigs[c]
		if cache != nil && c.serial != 0 {
			if old := cache.old[c.serial]; old != nil && old.sig == sig {
				parts[i] = old
				cache.next[c.serial] = old
				continue
			}
		}
		part := pass.part(ps, &rows, c, &r)
		part.sig = sig
		if cache != nil && c.serial != 0 {
			cache.next[c.serial] = part
		}
		parts[i] = part
	}
	return parts
}

// newest are the take newest rows f keeps, newest last in heap order. A
// chunk is read only while its newest call (Latest) could be one of them,
// newest chunk first: a page of All reads the few its rows are in, not
// every one. What a chunk gave is kept on its part, the rows with their
// refs, and a chunk read for as many rows before is not read again: a page
// read again after a call decoded the few newest chunks again, among them
// a long session's whole file (57k calls), for its refs and then for its
// rows (rowsOf), ~390 MB of 90s while an agent worked (#2278). read is the
// rows of the refs selected, each its own copy.
func (ps *pairedRows) newest(f Filter, parts []*chunkPart, take int) (selected newestHeap, read map[rowRef]Row) {
	selected = newestHeap{}
	if take <= 0 {
		return selected, nil
	}
	var order []*rowChunk
	partOf := map[*rowChunk]*chunkPart{}
	for i, c := range ps.all {
		if parts[i].kept > 0 {
			order = append(order, c)
			partOf[c] = parts[i]
		}
	}
	slices.SortStableFunc(order, func(a, b *rowChunk) int {
		if a.Latest.IsZero() != b.Latest.IsZero() {
			if a.Latest.IsZero() {
				return -1
			}
			return 1
		}
		return b.Latest.Compare(a.Latest)
	})
	kept := map[rowRef]*Row{}
	push := func(ref timedRef, r *Row) {
		kept[ref.rowRef] = r
		if len(selected) < take {
			heap.Push(&selected, ref)
		} else if ref.newer(selected[0]) {
			selected[0] = ref
			heap.Fix(&selected, 0)
		}
	}
	rows := lentRows() // a row is kept as its ref
	defer rows.release()
	var r Row
	for _, c := range order {
		if len(selected) == take && !c.Latest.IsZero() && c.Latest.Before(selected[0].at) {
			break
		}
		part := partOf[c]
		if had := part.newest.Load(); had != nil && (had.all || len(had.rows) >= take) {
			for i := range had.rows {
				k := &had.rows[i]
				push(timedRef{rowRef{c, int(k.i)}, k.at, k.order}, &k.row)
			}
			continue
		}
		if newestOpening != nil {
			newestOpening(c)
		}
		own, n := newestHeap{}, 0
		ps.visitChunk(&rows, c, &r, func(ref timedRef, r *Row) {
			if !f.keepsRow(r) {
				return
			}
			n++
			if len(own) < take {
				heap.Push(&own, ref)
			} else if ref.newer(own[0]) {
				own[0] = ref
				heap.Fix(&own, 0)
			}
		})
		k := &chunkNewest{all: n <= take, rows: make([]newestRow, len(own))}
		open := rows.of(c) // still c's: decoded once
		for i, ref := range own {
			k.rows[i] = newestRow{i: int32(ref.Index), at: ref.at, order: ref.order, row: open.row(ref.Index)}
			ownStrings(reflect.ValueOf(&k.rows[i].row).Elem()) // lent
			push(ref, &k.rows[i].row)
		}
		part.newest.Store(k)
	}
	read = make(map[rowRef]Row, len(selected))
	for _, ref := range selected {
		read[ref.rowRef] = *kept[ref.rowRef]
	}
	return selected, read
}

// chunkNewest is a chunk's newest rows its page's filter keeps, as many
// as the page read it for; all: every one it keeps.
type chunkNewest struct {
	rows []newestRow
	all  bool
}

type newestRow struct {
	i     int32
	at    time.Time
	order int64
	row   Row
}

// newestOpening, in tests, is told each chunk newest decodes.
var newestOpening func(*rowChunk)
