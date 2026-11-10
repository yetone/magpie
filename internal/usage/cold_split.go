package usage

import (
	"strings"
	"sync"
	"time"
	"unsafe"
)

// A first start, with no sums kept on disk, built the page's cells, the
// heatmap's days and Codex's effort before the cut from one pairing of
// the whole history: every gateway call priced, every session row's
// message id in one set, every call matched in one map, ~210 MB of a
// ~355 MB live heap and a ~500 MB footprint on a year's history (#2278).
// They are built as a day's start advances them (pageAdvance), a few
// days of history at a time: a span's chunks are those with a row a
// pairing of it reaches, and each span's pairing is let go before the
// next.

// coldSpan is the days of history a first build pairs at a time. Zero
// pairs them all at once (tests compare the two). Four days held a
// span's pairing ~25 MB under two weeks' at the same CPU (#2278).
var coldSpan = 4

// coldSpanning, in tests, is told how many chunks each span pairs.
var coldSpanning func(chunks int)

// coldSpans calls fn for each span of days from since, or the day of the
// oldest row of sets, to cut, with the chunks of sets a pairing of that
// span reaches: those with a row from heatReach before it to heatReach
// after, of a chunk reaching past that its rows within it alone
// (within). A gateway block not priced yet is priced for its span alone,
// and let go after it. False: coldSpan is zero, and fn was not called.
func coldSpans(since, cut time.Time, sets [3][]*rowChunk, fn func(from, to time.Time, span [3][]*rowChunk)) bool {
	if coldSpan <= 0 {
		return false
	}
	first := chunkFirsts(sets)
	oldest := time.Time{}
	for _, set := range sets {
		for _, c := range set {
			if at := first(c); !at.IsZero() && (oldest.IsZero() || at.Before(oldest)) {
				oldest = at
			}
		}
	}
	from := since
	if oldest.IsZero() {
		from = cut
	} else if day := time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, cut.Location()); from.Before(day) {
		from = day
	}
	for from.Before(cut) {
		to := time.Date(from.Year(), from.Month(), from.Day()+coldSpan, 0, 0, 0, 0, cut.Location())
		if to.After(cut) {
			to = cut
		}
		lo, hi := from.Add(-heatReach), to.Add(heatReach)
		var span [3][]*rowChunk
		alone := map[*blockPricer]*blockPricer{}
		for k, set := range sets {
			for _, c := range set {
				at := first(c)
				if at.IsZero() || c.Latest.Before(lo) || !at.Before(hi) {
					continue
				}
				if b := c.lazy; b != nil {
					// priced into a copy: the query's stub stays one, and
					// the span's priced rows go back to the pool after it
					if alone[b] == nil {
						alone[b] = b.alone()
					}
					copy := *c
					copy.lazy = alone[b]
					c = &copy
				} else if c.raw == nil && (at.Before(lo) || !c.Latest.Before(hi)) {
					if c = c.within(lo, hi); c == nil {
						continue
					}
				}
				span[k] = append(span[k], c)
			}
		}
		if coldSpanning != nil {
			coldSpanning(chunksIn(span))
		}
		fn(from, to, span)
		for _, b := range alone {
			b.release()
		}
		from = to
	}
	return true
}

// within is c's rows from lo to before hi, and those of no time, as a
// chunk of its own; nil for none. A session written over weeks was
// paired whole in each span it reached: the last spans of a long
// history paired ~340k rows each, ~230k of them a few hundred such
// files' (#2278). Its rows past a span's reach pair with none of it.
// It has the strings its rows name alone, copied into one block of its
// own: a copy of the whole table held the whole file's decoded strings
// through each span it reached, 5–12 MB of a first start's most live
// heap (241–245 MB from 250–253).
func (c *rowChunk) within(lo, hi time.Time) *rowChunk {
	var rows chunkRows
	defer rows.release()
	open := rows.of(c)
	out := &rowChunk{Source: c.Source, Computer: c.Computer}
	var actors codexActors
	// ids is each string's id in out, plus one, and order out's strings
	// by their id in c; the first is "" in both
	ids := make([]uint32, len(open.Strings))
	order := []uint32{}
	if len(ids) > 0 {
		ids[0], order = 1, append(order, 0)
	}
	in := func(p *packedRow) bool {
		t := p.Time()
		return t.IsZero() || !t.Before(lo) && t.Before(hi)
	}
	// the rows counted first: grown by append, out's rows were 245 MB of
	// what a real history's start allocated, twice what they needed
	n := 0
	for i := range open.Rows {
		if in(&open.Rows[i]) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	out.Rows = make([]packedRow, 0, n)
	size := 0
	for i := range open.Rows {
		p := &open.Rows[i]
		if !in(p) {
			continue
		}
		t := open.texts(p)
		for k, id := range t {
			if ids[id] == 0 {
				order = append(order, id)
				ids[id] = uint32(len(order))
				size += len(open.Strings[id])
			}
			t[k] = ids[id] - 1
		}
		row := *p
		out.setTexts(&row, t)
		out.Rows = append(out.Rows, row)
		if c.codexActors.ids != nil {
			actors.add(c.codexActors.at(i))
		}
		if t := p.Time(); t.After(out.Latest) {
			out.Latest = t
		}
	}
	if len(out.Rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.Grow(size)
	for _, id := range order {
		b.WriteString(open.Strings[id])
	}
	text := b.String()
	out.Strings = make([]string, len(order))
	for i, id := range order {
		n := len(open.Strings[id])
		out.Strings[i], text = text[:n], text[n:]
	}
	out.shapeIDs, out.codexActors = nil, actors
	out.Count = len(out.Rows)
	out.Bytes = int64(len(out.Rows)) * int64(unsafe.Sizeof(packedRow{}))
	return out
}

// alone is a pricer of b's prices that keeps the blocks it prices to
// itself, until release.
func (b *blockPricer) alone() *blockPricer {
	o := *b
	o.shared, o.held, o.pooled = false, nil, nil
	return &o
}

// chunkFirsts is the time of each chunk's oldest row, zero for one with
// none. A named chunk's is kept by its serial: the page, the heatmap and
// Codex's effort each ask it of every chunk.
func chunkFirsts(sets [3][]*rowChunk) func(*rowChunk) time.Time {
	derivedUsed()
	firsts.Lock()
	firsts.turn++
	turn := firsts.turn
	if firsts.by == nil {
		firsts.by = map[uint64]chunkFirst{}
	}
	for k, kept := range firsts.by {
		if kept.turn+hintsKept < turn {
			delete(firsts.by, k)
		}
	}
	firsts.Unlock()
	rows := lentRows()
	known := map[*rowChunk]time.Time{}
	for _, set := range sets {
		for _, c := range set {
			if c.Latest.IsZero() {
				continue
			}
			if c.serial != 0 {
				firsts.Lock()
				kept, ok := firsts.by[c.serial]
				if ok {
					kept.turn = turn
					firsts.by[c.serial] = kept
				}
				firsts.Unlock()
				if ok {
					known[c] = kept.at
					continue
				}
			}
			read := c
			if c.lazy != nil {
				read = c.raw // the block's times are its priced copy's
			}
			var at time.Time
			open := rows.of(read)
			for i := range open.Rows {
				if t := open.Rows[i].Time(); at.IsZero() || t.Before(at) {
					at = t
				}
			}
			known[c] = at
			if c.serial != 0 {
				firsts.Lock()
				firsts.by[c.serial] = chunkFirst{at: at, turn: turn}
				firsts.Unlock()
			}
		}
	}
	rows.release()
	return func(c *rowChunk) time.Time { return known[c] }
}

type chunkFirst struct {
	at   time.Time
	turn uint64
}

var firsts struct {
	sync.Mutex
	by   map[uint64]chunkFirst
	turn uint64
}
