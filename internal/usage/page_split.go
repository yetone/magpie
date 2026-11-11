package usage

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unique"

	"github.com/yetone/magpie/internal/sessions"
)

// A page's cells of the rows before its last few days are kept as the
// heatmap's days are (heatSplit), in memory and in page-v1.json beside the
// sessions cache. A start's first page of All, the warm-up's, paired the
// whole history for them, every gateway call with its session row: after
// the heatmap's days and Codex's effort were kept, ~85 MB of a ~320 MB
// heap, and the Usage page's own first page paired it again beside it
// (#2278). Only the chunks of the days since cut are paired again, while
// what the cells before were counted of is the same and the page's rows
// are among theirs.

// pageOld is a page's cells of the rows before cut, from a pairing of
// every chunk, named as heatOld is. parts are the recent chunks' cells
// from cut on, as the last pairing of them paired them.
type pageOld struct {
	cut, since time.Time
	settled    []uint64
	before     heatSum
	at         time.Time
	key        string
	cells      *chunkPart
	parts      map[uint64]*chunkPart
}

// pageSplitting, in tests, is told how many chunks a split page pairs.
var pageSplitting func(chunks int)

// pageSplit is the cells of a page of p at now read through pass, the
// pairing its newest rows are read from and that pairing's cells, in its
// all's order. False: the page can't be split so (p begins after cut or
// ends before it, its chunks can't be named, or fewer than take rows the
// filter keeps are since cut), and nothing was paired for the page.
func pageSplit(p Period, f Filter, now time.Time, take int, pass *cellPass, gateways, chunks, others []*rowChunk, identities map[string]sessions.CodexIdentity, cache *partCache) (cells []*chunkPart, ps *pairedRows, newest []*chunkPart, ok bool) {
	since, until := p.Since(now), p.Until(now)
	cut := time.Date(now.Year(), now.Month(), now.Day()-heatRecent, 0, 0, 0, 0, now.Location())
	if cache.disk.root == "" || !cut.After(since) || !until.IsZero() && !until.After(cut) {
		return nil, nil, nil, false
	}
	sets := [3][]*rowChunk{gateways, chunks, others}
	recent, settled := splitAt(cut, sets)
	serials, named := pairSerials(settled[0], settled[1], settled[2])
	if !named {
		return nil, nil, nil, false
	}
	before := heatBefore(cut, recent)
	old := cache.pageOld
	same := func(o *pageOld) bool {
		return o != nil && o.cut.Equal(cut) && o.since.Equal(since) && o.before == before
	}
	kept := same(old) && slices.Equal(old.settled, serials) && time.Since(old.at) < heatOldFresh
	key := ""
	if !kept {
		k, named := heatSettledKey(cache.disk, cut, since, f, identities, settled, before)
		if !named {
			return nil, nil, nil, false
		}
		key = pageKeyOf(until, pass, k)
		switch {
		case same(old) && old.key == key:
			fresh := *old
			fresh.settled, fresh.at = serials, time.Now()
			old, kept = &fresh, true
		default:
			if c, at, found := pageDiskRead(key); found {
				if at >= pageDiskPages/2 {
					// read again near the end: kept from being the next dropped
					_ = pageDiskWrite(key, cut, since, c)
				}
				old, kept = &pageOld{cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, cells: c}, true
			} else if c, found := pageAdvance(cache.disk, cut, since, until, f, pass, identities, sets, key); found {
				old, kept = &pageOld{cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, cells: c}, true
			} else if c, found := pageCold(cut, since, pass, identities, sets, key, effortKeyOf(f, k)); found {
				old, kept = &pageOld{cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, cells: c}, true
			}
		}
	}
	if !kept {
		if pageSplitting != nil {
			pageSplitting(len(gateways) + len(chunks) + len(others))
		}
		ps, cache.pairsNext = pairRowsMemo(p, now, gateways, chunks, others, identities, cache.pairs)
		parts := ps.partsOf(pass, cache, identities)
		next := &pageOld{cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, cells: ps.cellsBefore(pass, cut)}
		cache.pageOldNext = next
		// not written, the cells are counted again on the next start
		_ = pageDiskWrite(key, cut, since, next.cells)
		return parts, ps, parts, true
	}
	if pageSplitting != nil {
		pageSplitting(chunksIn(recent))
	}
	next := *old
	next.parts = map[uint64]*chunkPart{}
	ps = pairRowsIn(cut, until, recent[0], recent[1], recent[2], identities)
	newest = ps.partsOf(pass, &partCache{old: old.parts, next: next.parts}, identities)
	n := 0
	for _, part := range newest {
		n += part.kept
	}
	if n < take {
		// a page of rows older than cut: every chunk is paired for it
		cache.pageOldNext = old
		return nil, nil, nil, false
	}
	cache.pageOldNext = &next
	// the pairing of every chunk is not this page's, and stays the next's
	cache.pairsNext = cache.pairs
	// the settled chunks' cells stay kept for the next pairing of them all
	for _, set := range settled {
		for _, c := range set {
			if part := cache.old[c.serial]; part != nil {
				cache.next[c.serial] = part
			}
		}
	}
	return append([]*chunkPart{old.cells}, newest...), ps, newest, true
}

// pageKeyOf is the key of a page's cells until, read through pass, of
// the settled chunks heatSettledKey names k.
func pageKeyOf(until time.Time, pass *cellPass, k string) string {
	return fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "page-v2\x00%d\x00%t\x00%t\x00%s", until.UnixNano(), pass.names, pass.hours, k)))
}

// pageAdvance is a page's cells before cut, from a page of the file's
// cells before an earlier cut counted under the same key there and the
// cells of a pairing of the chunks of the days between, written under
// key; or false.
func pageAdvance(d heatDiskRef, cut, since, until time.Time, f Filter, pass *cellPass, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key string) (*chunkPart, bool) {
	keys := map[int64]string{}
	for _, e := range pageDiskEntries() {
		if e.Cut.IsZero() || !e.Cut.Before(cut) || !e.Since.Equal(since) {
			continue
		}
		at := e.Cut.In(cut.Location())
		k, seen := keys[at.UnixNano()]
		var recent [3][]*rowChunk
		if !seen {
			var named bool
			if recent, k, named = settledAt(d, at, since, f, identities, sets); named {
				k = pageKeyOf(until, pass, k)
			}
			keys[at.UnixNano()] = k
		}
		if k == "" || k != e.Key {
			continue
		}
		if seen {
			recent, _ = splitAt(at, sets)
		}
		if pageSplitting != nil {
			pageSplitting(chunksIn(recent))
		}
		between := pairRowsIn(at, until, recent[0], recent[1], recent[2], identities).cellsBefore(pass, cut)
		cells := addCells(pageDiskPart(e), between)
		_ = pageDiskWrite(key, cut, since, cells)
		return cells, true
	}
	return nil, false
}

// pageCold is a page's cells before cut from since, of a pairing a few
// days at a time (coldSpans), written under key; or false. With
// effortKey, Codex's effort before cut is counted of the same pairing and
// kept under it, when the file doesn't have it: a first start paired the
// whole history for the warm-up's page and again for the effort of the
// same period, ~65 and ~105 MB of its live heap one after the other, and
// seconds of CPU each (#2278).
func pageCold(cut, since time.Time, pass *cellPass, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key, effortKey string) (*chunkPart, bool) {
	cells := &chunkPart{}
	var effort effortSums
	if effortKey != "" {
		if _, found := effortDiskRead(effortKey); !found {
			effort = effortSums{}
		}
	}
	if !coldSpans(since, cut, sets, func(from, to time.Time, span [3][]*rowChunk) {
		ps := pairRowsIn(from, to, span[0], span[1], span[2], identities)
		cells = addCells(cells, ps.cellsBefore(pass, to))
		if effort != nil {
			ps.visitActors(effort.add)
		}
	}) {
		return nil, false
	}
	_ = pageDiskWrite(key, cut, since, cells)
	if effort != nil {
		_ = effortDiskWrite(effortKey, cut, since, effort.values())
	}
	return cells, true
}

// effortKeyOf is the key Codex's effort before the cut is kept under, of
// the settled chunks heatSettledKey names k for f; "" when f filters
// anything, as the effort is of every row.
func effortKeyOf(f Filter, k string) string {
	if f != (Filter{}) {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte("effort-v1\x00"+k)))
}

// addCells is the cells of a and of b, the later, as one part: a cell of
// the same names, bits and bucket added up, in the order of their last.
func addCells(a, b *chunkPart) *chunkPart {
	out := &chunkPart{kept: a.kept + b.kept, first: a.first}
	if a.speeds != nil || b.speeds != nil {
		out.speeds = speedRank{}
		out.speeds.merge(a.speeds)
		out.speeds.merge(b.speeds)
	}
	if !b.first.IsZero() && (out.first.IsZero() || b.first.Before(out.first)) {
		out.first = b.first
	}
	key := func(c pageCell) pageCell {
		c.n, c.d = 0, Totals{}
		return c
	}
	later := make(map[pageCell]int, len(b.cells))
	for i, c := range b.cells {
		later[key(c)] = i
	}
	cells := slices.Clone(b.cells)
	out.cells = make([]pageCell, 0, len(a.cells)+len(b.cells))
	for _, c := range a.cells {
		if i, ok := later[key(c)]; ok {
			cells[i].n += c.n
			cells[i].d.plus(&c.d)
			continue
		}
		out.cells = append(out.cells, c)
	}
	out.cells = append(out.cells, cells...)
	return out
}

// cellsBefore is the cells of ps's rows before cut as one part: each
// chunk's, in all's order, a cell of the same names, bits and bucket
// added up, in the order of their last.
func (ps *pairedRows) cellsBefore(pass *cellPass, cut time.Time) *chunkPart {
	before := *ps
	before.until = cut
	rows := lentRows() // part keeps unique's handles
	defer rows.release()
	var r Row
	out := &chunkPart{}
	at := map[pageCell]int{}
	var last []int
	seq := 0
	for _, c := range ps.all {
		part := pass.part(&before, &rows, c, &r)
		out.kept += part.kept
		if part.speeds != nil {
			if out.speeds == nil {
				out.speeds = speedRank{}
			}
			out.speeds.merge(part.speeds)
		}
		if !part.first.IsZero() && (out.first.IsZero() || part.first.Before(out.first)) {
			out.first = part.first
		}
		for _, cell := range part.cells {
			seq++
			k := cell
			k.n, k.d = 0, Totals{}
			i, ok := at[k]
			if !ok {
				i = len(out.cells)
				at[k] = i
				out.cells = append(out.cells, k)
				last = append(last, 0)
			}
			out.cells[i].n += cell.n
			out.cells[i].d.plus(&cell.d)
			last[i] = seq
		}
	}
	order := make([]int, len(out.cells))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return cmp.Compare(last[a], last[b]) })
	cells := make([]pageCell, len(order))
	for i, j := range order {
		cells[i] = out.cells[j]
	}
	out.cells = cells
	return out
}

func pageDiskPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "page-v1.json")
}

// pageDiskCell is a pageCell as the file holds it.
type pageDiskCell struct {
	Provider   string `json:"p,omitempty"`
	Agent      string `json:"a,omitempty"`
	Model      string `json:"m,omitempty"`
	Kind       string `json:"k,omitempty"`
	CallerID   string `json:"ci,omitempty"`
	CallerName string `json:"cn,omitempty"`
	Account    string `json:"ac,omitempty"`
	Computer   string `json:"co,omitempty"`
	Source     uint8  `json:"s,omitempty"`
	Bits       uint16 `json:"b,omitempty"`
	Bucket     int32  `json:"u,omitempty"`
	N          int    `json:"n"`
	D          Totals `json:"d"`
}

// pageDiskEntry is one page's cells, under the key they were counted
// under.
type pageDiskEntry struct {
	Key   string         `json:"key"`
	Cut   time.Time      `json:"cut,omitzero"`
	Since time.Time      `json:"since,omitzero"`
	At    time.Time      `json:"at"`
	Kept  int            `json:"kept"`
	First time.Time      `json:"first"`
	Cells []pageDiskCell `json:"cells"`
	// Speeds are the model speed ranking's counts (speed_rank.go)
	Speeds speedRank `json:"speeds,omitempty"`
}

// pageDiskPages bounds the pages the file holds, the last written or read
// again first: the warm-up's All, the Usage page's period, the tray's
// periods and the filters a reader picks. Four let a reader's few filters
// drop the warm-up's, and every start after paired the whole history again.
const pageDiskPages = 8

func pageDiskEntries() []pageDiskEntry {
	b, err := os.ReadFile(pageDiskPath())
	if err != nil {
		return nil
	}
	var all []pageDiskEntry
	if json.Unmarshal(b, &all) != nil {
		return nil
	}
	return all
}

// pageDiskRead is the cells the file holds under key and where in the file
// they are, or false.
func pageDiskRead(key string) (*chunkPart, int, bool) {
	for at, e := range pageDiskEntries() {
		if e.Key == key {
			return pageDiskPart(e), at, true
		}
	}
	return nil, 0, false
}

// pageDiskPart is e's cells as one part.
func pageDiskPart(e pageDiskEntry) *chunkPart {
	part := &chunkPart{kept: e.Kept, cells: make([]pageCell, len(e.Cells)), speeds: e.Speeds}
	if !e.First.IsZero() {
		part.first = e.First.In(time.Local)
	}
	for i, c := range e.Cells {
		part.cells[i] = pageCell{
			provider: unique.Make(c.Provider), agent: unique.Make(c.Agent), model: unique.Make(c.Model), kind: unique.Make(c.Kind),
			callerID: unique.Make(c.CallerID), callerName: unique.Make(c.CallerName), account: unique.Make(c.Account), computer: unique.Make(c.Computer),
			source: c.Source, bits: c.Bits, bucket: c.Bucket, n: c.N, d: c.D,
		}
	}
	return part
}

// pageDiskWrite keeps part, a page's cells before cut from since, under
// key with the pages written last, through a file renamed over the last.
func pageDiskWrite(key string, cut, since time.Time, part *chunkPart) error {
	e := pageDiskEntry{Key: key, Cut: cut, Since: since, At: time.Now(), Kept: part.kept, First: part.first, Cells: make([]pageDiskCell, len(part.cells)), Speeds: part.speeds}
	for i := range part.cells {
		c := &part.cells[i]
		e.Cells[i] = pageDiskCell{
			Provider: c.provider.Value(), Agent: c.agent.Value(), Model: c.model.Value(), Kind: c.kind.Value(),
			CallerID: c.callerID.Value(), CallerName: c.callerName.Value(), Account: c.account.Value(), Computer: c.computer.Value(),
			Source: c.source, Bits: c.bits, Bucket: c.bucket, N: c.n, D: c.d,
		}
	}
	all := []pageDiskEntry{e}
	for _, o := range pageDiskEntries() {
		if o.Key != key && len(all) < pageDiskPages {
			all = append(all, o)
		}
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	path := pageDiskPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".page-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, err = out.Write(b)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
