package usage

// The heatmap (#1369): the last 53 weeks a day each, as a calendar of
// contributions lays them out, Monday on top. It is the request page's own
// index read over that span — QueryPage's cached chunks, priced once — and
// not another pass over the logs, so a page asking for it again reads only
// what changed. It counts what the Requests tab's chart counts: the rows its
// filters keep, but for the day picked, and not the calls the gateway turned
// away itself.

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// HeatmapWeeks is how many weeks the heatmap shows, this one the last.
const HeatmapWeeks = 53

// heatmapPeriod is the span the heatmap is read over: QueryPage's period for
// it, from the Monday HeatmapWeeks-1 weeks before this week's. Not one the
// page's period switch offers.
const heatmapPeriod Period = "heatmap"

// heatmapSince is the Monday the heatmap starts on, as of now.
func heatmapSince(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7-7*(HeatmapWeeks-1))
}

// Day is one day's calls, by the local calendar day they fell on.
type Day struct {
	Date   string  `json:"date"` // YYYY-MM-DD
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"` // in, out and through the cache, as the chart counts them
	Cost   float64 `json:"cost"`   // of the priced calls, USD
	// Unpriced: calls with tokens but no known price, not in Cost
	Unpriced int `json:"unpriced,omitempty"`
}

// Heatmap is the days from From to To (today), both YYYY-MM-DD in the
// reader's time zone (Filter.Zone): only those with calls are listed, oldest first.
type Heatmap struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days []Day  `json:"days"`
}

// HeatmapOf is the last HeatmapWeeks weeks of the calls the filter keeps, a
// day each. The filter's Day is not one of its dimensions here: the heatmap
// is every day's.
func HeatmapOf(f Filter) Heatmap {
	f.Day = ""
	if h := QueryPage(heatmapPeriod, f, 0, 1).Heat; h != nil {
		return *h
	}
	return Heatmap{Days: []Day{}}
}

// heatmapOf sums the rows visit gives it by their day in now's zone (the
// reader's), from since to now's day; a row outside those days is left out.
func heatmapOf(since, now time.Time, visit func(add func(Row))) *Heatmap {
	days := heatDays{}
	visit(func(r Row) { days.add(&r, since, now.Location()) })
	return heatmapFrom(since, now, days.list())
}

// heatDays adds rows up a day each, by the day's name.
type heatDays struct {
	at map[string]*Day
	// a day's name is made once for each day, not for each of its calls
	names map[[2]int]string
}

// add adds r to its day in loc, unless the heatmap doesn't count it.
func (h *heatDays) add(r *Row, since time.Time, loc *time.Location) {
	if r.IsRejected() || r.IsProbe() || r.Time.Before(since) {
		return
	}
	if h.at == nil {
		h.at, h.names = map[string]*Day{}, map[[2]int]string{}
	}
	t := r.Time.In(loc)
	k := [2]int{t.Year(), t.YearDay()}
	d, ok := h.names[k]
	if !ok {
		d = t.Format(time.DateOnly)
		h.names[k] = d
	}
	x := h.at[d]
	if x == nil {
		x = &Day{Date: d}
		h.at[d] = x
	}
	if !r.HandedOn {
		x.Calls++ // a request, not each try at it
	}
	x.Tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
	switch {
	case r.Priced:
		x.Cost += r.Cost
	case r.Input+r.Output > 0:
		x.Unpriced++
	}
}

// addDay adds a day's sums to its day.
func (h *heatDays) addDay(d Day) {
	if h.at == nil {
		h.at = map[string]*Day{}
	}
	x := h.at[d.Date]
	if x == nil {
		x = &Day{Date: d.Date}
		h.at[d.Date] = x
	}
	x.Calls += d.Calls
	x.Tokens += d.Tokens
	x.Cost += d.Cost
	x.Unpriced += d.Unpriced
}

// list is the days, oldest first.
func (h *heatDays) list() []Day {
	out := make([]Day, 0, len(h.at))
	for _, x := range h.at {
		out = append(out, *x)
	}
	slices.SortFunc(out, func(a, b Day) int { return strings.Compare(a.Date, b.Date) })
	return out
}

// heatmapFrom is days from since to now's day, a day after it left out.
func heatmapFrom(since, now time.Time, days []Day) *Heatmap {
	out := &Heatmap{From: since.Format(time.DateOnly), To: now.Format(time.DateOnly), Days: []Day{}}
	for _, d := range days {
		if d.Date <= out.To {
			out.Days = append(out.Days, d)
		}
	}
	return out
}

// heatOf is heatmapOf of ps's rows f keeps, added up a chunk at a time:
// cache's sums of a chunk paired as before are taken as they are. A call
// through magpie changes the gateway's log, and the Requests tab's refresh
// read every row of the year again for it, and paired them: ~4 GB a
// minute and seconds of CPU a refresh while an agent worked (#2278).
func (ps *pairedRows) heatOf(since, now time.Time, f Filter, cache *partCache, identities map[string]sessions.CodexIdentity) *Heatmap {
	return heatmapFrom(since, now, ps.heatDaysOf(since, now, f, cache, identities, nil))
}

// heatDaysOf is heatOf's days, oldest first, each with a call, of the
// chunks of ps that only keeps (nil: all).
func (ps *pairedRows) heatDaysOf(since, now time.Time, f Filter, cache *partCache, identities map[string]sessions.CodexIdentity, only func(*rowChunk) bool) []Day {
	sigs := ps.sigs(identities)
	rows := lentRows() // a day keeps its date and numbers
	defer rows.release()
	var r Row
	all := heatDays{}
	for _, c := range ps.all {
		if only != nil && !only(c) {
			continue
		}
		sig := sigs[c]
		var part *chunkPart
		if cache != nil && c.serial != 0 {
			if old := cache.old[c.serial]; old != nil && old.sig == sig {
				part = old
			}
		}
		if part == nil {
			if heatReading != nil {
				heatReading(c)
			}
			one := heatDays{}
			ps.visitChunk(&rows, c, &r, func(_ timedRef, r *Row) {
				if f.keepsRow(r) {
					one.add(r, since, now.Location())
				}
			})
			part = &chunkPart{sig: sig, days: one.list()}
		}
		if cache != nil && c.serial != 0 {
			cache.next[c.serial] = part
		}
		for _, d := range part.days {
			all.addDay(d)
		}
	}
	return all.list()
}

// heatRecent is how many days before today the heatmap pairs again for a
// call (heatSplit), and heatReach how far before them a chunk's newest
// call can be and still be paired with one of theirs: the gateway's calls
// are read from a day before a period (gatewaySince), the other
// computers' days from two.
const (
	heatRecent = 2
	heatReach  = 48 * time.Hour
)

// heatOldFresh is how long heatSplit keeps the days before its last few
// while only the chunks of those days change. A session file a call is
// written to now can hold older calls too, and those are taken to stay as
// they were: an agent appends to its file. What changes them anyway, a
// file written again or a thread's identity learnt, shows within this:
// the days are named again (heatSettledKey), and counted again only when
// the name changed.
var heatOldFresh = 10 * time.Minute

// heatOld is the heatmap's days before cut, from a pairing of every
// chunk, while settled, the serials of the chunks with no call since
// heatReach before cut, are the same. parts are the other chunks' sums
// from cut on, as the last pairing of them paired them.
type heatOld struct {
	cut     time.Time
	settled []uint64
	at      time.Time
	days    []Day
	parts   map[uint64]*chunkPart
	// before is the recent chunks' rows before cut, which days counts
	before heatSum
	// key names what days were counted of (heatSettledKey); "" unnamed
	key string
}

// heatSum is a sum of rows as read, to tell a change in them.
type heatSum struct {
	n          int
	at, tokens int64
}

// heatBefore is the sum of the recent chunks' rows before cut: a recent
// chunk can hold calls of the days before (a session file copied in, a
// rollout forked from an old one), and they change the days the last
// pairing of every chunk counted. A named chunk's sum is kept by its
// serial and the cut: the heatmap, the page and Codex's effort each asked
// it of every recent chunk, unpacked whole, on each read, ~120 MB of a
// start's first 18s's allocations on a year's history (#2278).
func heatBefore(cut time.Time, recent [3][]*rowChunk) heatSum {
	derivedUsed()
	priceStubs(recent[0])
	heatBefores.Lock()
	heatBefores.turn++
	turn := heatBefores.turn
	var s heatSum
	var open []*rowChunk
	for _, set := range recent {
		for _, c := range set {
			if k := c.serial; k != 0 {
				if kept, ok := heatBefores.by[k]; ok && kept.cut.Equal(cut) {
					kept.turn = turn
					heatBefores.by[k] = kept
					s.plus(kept.sum)
					continue
				}
			}
			open = append(open, c)
		}
	}
	heatBefores.Unlock()
	rows := lentRows()
	defer rows.release()
	made := map[uint64]heatSum{}
	for _, c := range open {
		if heatBeforeOpening != nil {
			heatBeforeOpening(c)
		}
		var part heatSum
		unpacked := rows.of(c)
		for i := range unpacked.Rows {
			if pr := &unpacked.Rows[i]; pr.Time().Before(cut) {
				part.n++
				part.at += pr.Time().UnixNano()
				for _, t := range pr.Tokens {
					part.tokens += int64(t)
				}
			}
		}
		s.plus(part)
		if c.serial != 0 {
			made[c.serial] = part
		}
	}
	heatBefores.Lock()
	if heatBefores.by == nil {
		heatBefores.by = map[uint64]heatBeforeOf{}
	}
	for k, part := range made {
		heatBefores.by[k] = heatBeforeOf{cut: cut, sum: part, turn: turn}
	}
	for k, kept := range heatBefores.by {
		if kept.turn+hintsKept < heatBefores.turn {
			delete(heatBefores.by, k)
		}
	}
	heatBefores.Unlock()
	return s
}

func (s *heatSum) plus(o heatSum) {
	s.n += o.n
	s.at += o.at
	s.tokens += o.tokens
}

// heatBeforeOf is a chunk's rows before cut, kept as hintsOf keeps a
// chunk's hints, for as many reads after the last that asked for it.
type heatBeforeOf struct {
	cut  time.Time
	sum  heatSum
	turn uint64
}

var heatBefores struct {
	sync.Mutex
	by   map[uint64]heatBeforeOf
	turn uint64
}

// heatBeforeOpening, in tests, is told each chunk heatBefore unpacks.
var heatBeforeOpening func(*rowChunk)

// heatSplit is heatOf of the rows of every chunk, paired. A call through
// magpie changes the gateway's newest block and a session file, and the
// heatmap paired a year of calls again for it: ~1.8 s and most of the
// GC of each refresh of the Usage page while an agent worked (#2278). The
// days before the last heatRecent are taken from the last pairing of
// every chunk while the chunks that could change them are the same, and
// only the chunks of the days since are paired again, as a page of those
// days pairs them.
func heatSplit(since, now time.Time, gateways, chunks, others []*rowChunk, f Filter, identities map[string]sessions.CodexIdentity, cache *partCache) *Heatmap {
	cut := time.Date(now.Year(), now.Month(), now.Day()-heatRecent, 0, 0, 0, 0, now.Location())
	if cache == nil || !cut.After(since) {
		return heatmapFrom(since, now, pairRows(heatmapPeriod, now, gateways, chunks, others, identities).heatDaysOf(since, now, f, cache, identities, nil))
	}
	sets := [3][]*rowChunk{gateways, chunks, others}
	recent, settled := splitAt(cut, sets)
	// recentDays adds up the recent chunks' rows from cut on, paired as
	// the next call pairs them, into parts
	recentDays := func(old, parts map[uint64]*chunkPart) []Day {
		ps := pairRowsIn(cut, heatmapPeriod.Until(now), recent[0], recent[1], recent[2], identities)
		return ps.heatDaysOf(cut, now, f, &partCache{old: old, next: parts}, identities, nil)
	}
	serials, ok := pairSerials(settled[0], settled[1], settled[2])
	old := cache.heatOld
	var before heatSum
	if ok {
		before = heatBefore(cut, recent)
	}
	var key string
	named := false
	kept := ok && old != nil && old.cut.Equal(cut) && slices.Equal(old.settled, serials) && old.before == before && time.Since(old.at) < heatOldFresh
	if ok && !kept {
		// the days are counted again only when what they were counted of
		// changed: kept, or on disk from the last run
		if key, named = heatSettledKey(cache.disk, cut, since, f, identities, settled, before); named {
			if old != nil && old.key == key && old.cut.Equal(cut) && old.before == before {
				fresh := *old
				fresh.settled, fresh.at = serials, time.Now()
				old, kept = &fresh, true
			} else if days, found := heatDiskRead(key); found {
				old, kept = &heatOld{cut: cut, settled: serials, at: time.Now(), days: days, before: before, key: key}, true
			} else if days, found := heatAdvance(cache.disk, cut, since, now, f, identities, sets, key); found {
				old, kept = &heatOld{cut: cut, settled: serials, at: time.Now(), days: days, before: before, key: key}, true
			} else if days, found := heatCold(cut, since, now, f, identities, sets, key); found {
				old, kept = &heatOld{cut: cut, settled: serials, at: time.Now(), days: days, before: before, key: key}, true
			}
		}
	}
	if !kept {
		if heatPairing != nil {
			heatPairing(len(gateways) + len(chunks) + len(others))
		}
		days := pairEvery(since, now, gateways, chunks, others, identities, cache).heatDaysOf(since, now, f, cache, identities, nil)
		if ok {
			n, _ := slices.BinarySearchFunc(days, cut.Format(time.DateOnly), func(d Day, day string) int { return strings.Compare(d.Date, day) })
			parts := map[uint64]*chunkPart{}
			var kept map[uint64]*chunkPart
			if old != nil {
				kept = old.parts
			}
			recentDays(kept, parts)
			next := &heatOld{cut: cut, settled: serials, at: time.Now(), days: slices.Clip(days[:n]), parts: parts, before: before}
			if named {
				next.key = key
				// not written, the days are counted again on the next start
				_ = heatDiskWrite(key, cut, since, next.days)
			}
			cache.heatOldNext = next
		}
		return heatmapFrom(since, now, days)
	}
	next := *old
	next.parts = map[uint64]*chunkPart{}
	cache.heatOldNext = &next
	// the settled chunks' sums stay kept for the next pairing of them all
	for _, set := range settled {
		for _, c := range set {
			if part := cache.old[c.serial]; part != nil {
				cache.next[c.serial] = part
			}
		}
	}
	all := heatDays{}
	for _, d := range old.days {
		all.addDay(d)
	}
	if heatPairing != nil {
		heatPairing(chunksIn(recent))
	}
	for _, d := range recentDays(old.parts, next.parts) {
		all.addDay(d)
	}
	return heatmapFrom(since, now, all.list())
}

// heatAdvance is the days from since to cut, from the file's days before
// an earlier cut counted under the same key there and a pairing of the
// chunks of the days between, written under key; or false.
func heatAdvance(d heatDiskRef, cut, since, now time.Time, f Filter, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key string) ([]Day, bool) {
	e, ok := heatDiskEntryRead()
	if !ok || e.Cut.IsZero() || !e.Cut.Before(cut) || e.Since.IsZero() || e.Since.After(since) {
		return nil, false
	}
	at := e.Cut.In(cut.Location())
	recent, k, ok := settledAt(d, at, e.Since.In(since.Location()), f, identities, sets)
	if !ok || k != e.Key {
		return nil, false
	}
	if heatPairing != nil {
		heatPairing(chunksIn(recent))
	}
	from, to := since.Format(time.DateOnly), cut.Format(time.DateOnly)
	all := heatDays{}
	for _, day := range e.Days {
		if day.Date >= from {
			all.addDay(day)
		}
	}
	// the days before the earlier cut are paired from it on, a pairing
	// from since drops none of them
	for _, day := range pairRowsIn(at, heatmapPeriod.Until(now), recent[0], recent[1], recent[2], identities).heatDaysOf(at, now, f, nil, identities, nil) {
		if day.Date < to {
			all.addDay(day)
		}
	}
	days := all.list()
	_ = heatDiskWrite(key, cut, since, days)
	return days, true
}

// heatCold is the days from since to cut, of a pairing a few days at a
// time (coldSpans), written under key; or false.
func heatCold(cut, since, now time.Time, f Filter, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key string) ([]Day, bool) {
	all := heatDays{}
	if !coldSpans(since, cut, sets, func(from, to time.Time, span [3][]*rowChunk) {
		end := to.Format(time.DateOnly)
		for _, day := range pairRowsIn(from, to, span[0], span[1], span[2], identities).heatDaysOf(from, now, f, nil, identities, nil) {
			if day.Date < end {
				all.addDay(day)
			}
		}
	}) {
		return nil, false
	}
	days := all.list()
	_ = heatDiskWrite(key, cut, since, days)
	return days, true
}

// heatPairing, in tests, is told how many chunks heatSplit pairs.
var heatPairing func(chunks int)

// heatReading, in tests, is told each chunk heatOf adds up again.
var heatReading func(*rowChunk)
