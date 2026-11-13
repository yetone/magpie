package usage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// Codex's effort before the last few days is kept as the heatmap's days
// are (heatSplit), in memory and in effort-v1.json beside the sessions
// cache. A start built it from a pairing of the whole history, every
// gateway call with its session row: after the heatmap's days were kept
// (heat-v1.json) that was the cold start's largest part, ~90 MB of a
// ~310 MB heap (#2278). Only the chunks of the days since cut are paired
// again, while what the effort before was counted of is the same.

// effortOld is the effort of the rows before cut, from a pairing of every
// chunk, named as heatOld is.
type effortOld struct {
	root    string
	cut     time.Time
	since   time.Time
	settled []uint64
	before  heatSum
	at      time.Time
	key     string
	sums    map[string][]sessions.ModelEffort
}

// effortOlds is each period's, under codexEffortBuilding.
var effortOlds = map[Period]*effortOld{}

// effortPairing, in tests, is told how many chunks an effort build pairs.
var effortPairing func(int)

func effortDiskPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "effort-v1.json")
}

type effortDiskEntry struct {
	Key   string                            `json:"key"`
	Cut   time.Time                         `json:"cut,omitzero"`
	Since time.Time                         `json:"since,omitzero"`
	Sums  map[string][]sessions.ModelEffort `json:"sums"`
}

// effortDiskPeriods bounds the periods the file holds, the last written
// first. It held one: the Sessions list's All and the overview's period
// wrote over each other, so every start paired one of them a few days at
// a time again, ~100 MB of a warm start's live heap and seconds of CPU
// (#2278).
const effortDiskPeriods = 4

// effortDiskEntries is the file's entries; a file of one entry, as
// releases before wrote it, is that one.
func effortDiskEntries() []effortDiskEntry {
	b, err := os.ReadFile(effortDiskPath())
	if err != nil {
		return nil
	}
	var all []effortDiskEntry
	if json.Unmarshal(b, &all) != nil {
		var e effortDiskEntry
		if json.Unmarshal(b, &e) != nil {
			return nil
		}
		all = []effortDiskEntry{e}
	}
	return slices.DeleteFunc(all, func(e effortDiskEntry) bool { return e.Sums == nil })
}

func effortDiskRead(key string) (map[string][]sessions.ModelEffort, bool) {
	for _, e := range effortDiskEntries() {
		if e.Key == key {
			internEffortNames(e.Sums)
			return e.Sums, true
		}
	}
	return nil, false
}

// internEffortNames makes the sums' model and effort names one string
// each: the JSON gave every entry its own copy, and the sums are kept, a
// few names over ~20k entries on a long Codex history (#2278).
func internEffortNames(sums map[string][]sessions.ModelEffort) {
	seen := map[string]string{}
	one := func(s string) string {
		if k, ok := seen[s]; ok {
			return k
		}
		seen[s] = s
		return s
	}
	for _, list := range sums {
		for i := range list {
			list[i].Model, list[i].Effort = one(list[i].Model), one(list[i].Effort)
		}
	}
}

// effortDiskWrite keeps sums under key, in place of the entry of the same
// since, through a file renamed over the last.
func effortDiskWrite(key string, cut, since time.Time, sums map[string][]sessions.ModelEffort) error {
	all := []effortDiskEntry{{Key: key, Cut: cut, Since: since, Sums: sums}}
	for _, o := range effortDiskEntries() {
		if o.Key != key && !o.Since.Equal(since) && len(all) < effortDiskPeriods {
			all = append(all, o)
		}
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	path := effortDiskPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".effort-*.json")
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

// effortSplit adds p's Codex effort at now into sums, from the effort
// before cut that is kept and a pairing of the recent chunks. full is the
// pairing of every chunk, for when the kept effort doesn't stand. False:
// p can't be split so (it begins after cut, or its chunks can't be
// named), and nothing was added.
func effortSplit(sums effortSums, p Period, now time.Time, gateways, chunks, others []*rowChunk, identities map[string]sessions.CodexIdentity, disk heatDiskRef, full func() *pairedRows) bool {
	since, until := p.Since(now), p.Until(now)
	cut := time.Date(now.Year(), now.Month(), now.Day()-heatRecent, 0, 0, 0, 0, now.Location())
	if !cut.After(since) || disk.root == "" {
		return false
	}
	sets := [3][]*rowChunk{gateways, chunks, others}
	recent, settled := splitAt(cut, sets)
	serials, ok := pairSerials(settled[0], settled[1], settled[2])
	if !ok {
		return false
	}
	before := heatBefore(cut, recent)
	old := effortOlds[p]
	same := func(o *effortOld) bool {
		return o != nil && o.root == disk.root && o.cut.Equal(cut) && o.since.Equal(since) && o.before == before
	}
	kept := same(old) && slices.Equal(old.settled, serials) && time.Since(old.at) < heatOldFresh
	if !kept {
		key, named := heatSettledKey(disk, cut, since, Filter{}, identities, settled, before)
		if !named {
			return false
		}
		settledKey := key
		key = effortKeyOf(Filter{}, settledKey)
		switch {
		case same(old) && old.key == key:
			fresh := *old
			fresh.settled, fresh.at = serials, time.Now()
			old, kept = &fresh, true
		default:
			if s, found := effortDiskRead(key); found {
				old, kept = &effortOld{root: disk.root, cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, sums: s}, true
			} else if s, found := effortAdvance(disk, cut, since, until, identities, sets, key); found {
				old, kept = &effortOld{root: disk.root, cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, sums: s}, true
			} else if s, found := effortCold(cut, since, until, identities, sets, key, newCellPass(p, Filter{}, now, true), settledKey); found {
				old, kept = &effortOld{root: disk.root, cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, sums: s}, true
			}
		}
		if !kept {
			if effortPairing != nil {
				effortPairing(len(gateways) + len(chunks) + len(others))
			}
			part := effortSums{}
			full().visitActors(func(r *Row) {
				sums.add(r)
				if r.Time.Before(cut) {
					part.add(r)
				}
			})
			next := &effortOld{root: disk.root, cut: cut, since: since, settled: serials, before: before, at: time.Now(), key: key, sums: part.values()}
			effortOlds[p] = next
			// not written, the effort is counted again on the next start
			_ = effortDiskWrite(key, cut, since, next.sums)
			return true
		}
	}
	effortOlds[p] = old
	for actor, values := range old.sums {
		for _, v := range values {
			sums.addValue(actor, v)
		}
	}
	if effortPairing != nil {
		effortPairing(chunksIn(recent))
	}
	pairRowsIn(cut, until, recent[0], recent[1], recent[2], identities).visitActors(func(r *Row) { sums.add(r) })
	return true
}

// effortCold is the effort from since to cut, of a pairing a few days
// at a time (coldSpans), written under key; or false. The cells before
// cut of the period's unfiltered page, read through pass, are counted of
// the same pairing and kept too, when the file doesn't have them: the
// warm-up's page of All paired the whole history again after (pageCold).
func effortCold(cut, since, until time.Time, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key string, pass *cellPass, settledKey string) (map[string][]sessions.ModelEffort, bool) {
	part := effortSums{}
	pageKey := pageKeyOf(until, pass, settledKey)
	var cells *chunkPart
	if _, _, found := pageDiskRead(pageKey); !found {
		cells = &chunkPart{}
	}
	if !coldSpans(since, cut, sets, func(from, to time.Time, span [3][]*rowChunk) {
		ps := pairRowsIn(from, to, span[0], span[1], span[2], identities)
		ps.visitActors(part.add)
		if cells != nil {
			cells = addCells(cells, ps.cellsBefore(pass, to))
		}
	}) {
		return nil, false
	}
	sums := part.values()
	_ = effortDiskWrite(key, cut, since, sums)
	if cells != nil {
		_ = pageDiskWrite(pageKey, cut, since, cells)
	}
	return sums, true
}

// effortAdvance is the effort before cut, from the file's effort before
// an earlier cut counted under the same key there and a pairing of the
// chunks of the days between, written under key; or false.
func effortAdvance(d heatDiskRef, cut, since, until time.Time, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk, key string) (map[string][]sessions.ModelEffort, bool) {
	var e effortDiskEntry
	for _, o := range effortDiskEntries() {
		if o.Since.Equal(since) {
			e = o
			break
		}
	}
	if e.Cut.IsZero() || !e.Cut.Before(cut) {
		return nil, false
	}
	at := e.Cut.In(cut.Location())
	recent, k, ok := settledAt(d, at, since, Filter{}, identities, sets)
	if !ok || fmt.Sprintf("%x", sha256.Sum256([]byte("effort-v1\x00"+k))) != e.Key {
		return nil, false
	}
	if effortPairing != nil {
		effortPairing(chunksIn(recent))
	}
	part := effortSums{}
	for actor, values := range e.Sums {
		for _, v := range values {
			part.addValue(actor, v)
		}
	}
	pairRowsIn(at, until, recent[0], recent[1], recent[2], identities).visitActors(func(r *Row) {
		if r.Time.Before(cut) {
			part.add(r)
		}
	})
	sums := part.values()
	_ = effortDiskWrite(key, cut, since, sums)
	return sums, true
}
