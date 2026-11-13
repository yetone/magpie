package usage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The heatmap's settled days are kept on disk between runs (heat-v1.json).
// A started magpie paired the whole history for them, every gateway call
// with its session row, and so again every heatOldFresh: the cold start's
// peak, ~100 MB of a ~450 MB live heap, and seconds of CPU each time
// (#2278). They stand while what they were counted of is the same, which
// heatSettledKey names: the settled chunks, the recent chunks' rows before
// the cut, the threads' identities, the filter and the days, the code that
// counts them and what prices and names a call.

// settledCountVersion names how the settled days, a page's cells before
// the cut (page-v1.json) and Codex's effort (effort-v1.json) are counted.
// They were keyed by this package's source, as requests-v1.bin is: 14 of
// 39 releases changed it, and each such upgrade paired every user's whole
// history again on its first start, the cold start's peak (#2278). Raise
// it with any change to what a row counts as; TestSettledCountedAsVersioned
// fails until it is raised.
const settledCountVersion = "4"

func heatDiskPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "heat-v1.json")
}

// heatDiskEntry is the file: the key it was counted under, the cut and
// since it names, and the days.
type heatDiskEntry struct {
	Key   string    `json:"key"`
	Cut   time.Time `json:"cut,omitzero"`
	Since time.Time `json:"since,omitzero"`
	Days  []Day     `json:"days"`
}

func heatDiskEntryRead() (heatDiskEntry, bool) {
	b, err := os.ReadFile(heatDiskPath())
	if err != nil {
		return heatDiskEntry{}, false
	}
	var e heatDiskEntry
	if json.Unmarshal(b, &e) != nil {
		return heatDiskEntry{}, false
	}
	return e, true
}

// heatDiskRead is the days the file holds under key, or false.
func heatDiskRead(key string) ([]Day, bool) {
	e, ok := heatDiskEntryRead()
	if !ok || e.Key != key {
		return nil, false
	}
	return e.Days, true
}

// heatDiskWrite keeps days before cut from since under key, through a
// file renamed over the last.
func heatDiskWrite(key string, cut, since time.Time, days []Day) error {
	saveAsks() // the asks its rows were made of first (catalogBase)
	b, err := json.Marshal(heatDiskEntry{Key: key, Cut: cut, Since: since, Days: days})
	if err != nil {
		return err
	}
	path := heatDiskPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".heat-*.json")
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

// heatSettledKey names what the days before cut were counted of: each
// chunk by its rows (a session file's through sourceDigest) under the
// same root and meta, and the counting by the call shards' version and
// settledCountVersion. False: no disk to keep the count on.
func heatSettledKey(d heatDiskRef, cut, since time.Time, f Filter, identities map[string]sessions.CodexIdentity, settled [3][]*rowChunk, before heatSum) (string, bool) {
	if d.root == "" {
		return "", false
	}
	// the filter's zone by its name: %#v writes its pointer, another in
	// each run
	zone := ""
	if f.Zone != nil {
		zone = f.Zone.String()
	}
	f.Zone = nil
	h := sha256.New()
	fmt.Fprintf(h, "heat-v1\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%#v\x00%s\x00%d:%d:%d\x00",
		sessions.CallCacheVersion()+"\x00"+settledCountVersion, d.root, d.meta, cut.UnixNano(), since.UnixNano(), cut.Location(), f, zone, before.n, before.at, before.tokens)
	hashCodexIdentities(h, identities)
	var b []byte
	rows := lentRows()
	defer rows.release()
	for k, set := range settled {
		// by path: the sets are in mtime order, so a file touched moved,
		// and a pairing breaks its ties by path (timedRef.newer)
		set = slices.SortedStableFunc(slices.Values(set), func(a, b *rowChunk) int {
			return strings.Compare(a.Source.Path, b.Source.Path)
		})
		for _, c := range set {
			// a chunk without a serial (a probe's, made again for each
			// query) is named by its rows below, as pairSerials allows
			b = fmt.Appendf(b[:0], "\x01%d\x00%s\x00%d\x00%d\x00%s\x00", k, c.Computer, c.Count, c.Latest.UnixNano(), c.Source.Path)
			h.Write(b)
			if c.Source.Path != "" {
				sum := sourceDigest(c, &rows)
				h.Write(sum[:])
				continue
			}
			if c.raw != nil {
				// a gateway block by its rows as logged: meta names what
				// prices them, and one not priced has no rows of its own
				hashRows(h, rows.of(c.raw))
				continue
			}
			hashRows(h, rows.of(c))
		}
	}
	sourceDigestsSave()
	return fmt.Sprintf("%x", h.Sum(nil)), true
}

// sourceDigests are session files' chunks' digests (sourceDigest) by
// serial: those of the keys made since the last turn, and before it.
var sourceDigests struct {
	sync.Mutex
	// by and last are the serials used since the last turn, and before it,
	// which the kept file holds first. Their digests are in made, or in
	// what was kept on disk: not a map of 17k digests beside the list read
	// (#2278).
	by, last map[uint64]struct{}
	// made are the digests hashed that aren't kept on disk yet
	made map[uint64][sha256.Size]byte
}

// sourceDigestsKept is how many digests a turn holds, more than the
// session files of a long history.
const sourceDigestsKept = 1 << 16

// sourceDigest names a session file's chunk by its rows, not the file's
// size and mtime. An agent touches files it adds no call to (a hook's
// line, a title, an mtime bumped), and a settled key named by the mtime
// was new on each such touch: every poll of a page or the heatmap paired
// the whole history again, 1-3 s each on 300k calls (GrayRabbit). A
// chunk's serial names its file as read under one root and meta
// (sourceSerial), so its rows are hashed once a process. Codex actors
// are left out as before: the codex identities in the key decide them.
func sourceDigest(c *rowChunk, rows *chunkRows) [sha256.Size]byte {
	d := &sourceDigests
	d.Lock()
	sum, ok := d.made[c.serial]
	if !ok {
		sourceDigestsLoad()
		sum, ok = sourceDigestsDisk.kept.get(c.serial)
	}
	d.Unlock()
	made := !ok
	if made {
		h := sha256.New()
		io.WriteString(h, c.Source.Agent+"\x00"+c.Source.Key+"\x00")
		hashRows(h, rows.of(c))
		copy(sum[:], h.Sum(nil))
	}
	d.Lock()
	if made {
		sourceDigestsHashed++
		sourceDigestsDisk.dirty = true
		if d.made == nil {
			d.made = map[uint64][sha256.Size]byte{}
		}
		d.made[c.serial] = sum
	}
	if d.by == nil || len(d.by) >= sourceDigestsKept {
		d.last, d.by = d.by, map[uint64]struct{}{}
	}
	d.by[c.serial] = struct{}{}
	d.Unlock()
	derivedUsed() // by and last go when idle (dropDigestTurns)
	return sum
}

// hashRows writes an unpacked chunk's rows into h, each field as a number
// and each string as written: a row's time holds a pointer, its padding
// isn't written by every copy, and a chunk packed and the same unpacked
// number their strings and shapes apart. A shape is written where a row
// first has it, and by that row after.
func hashRows(h hash.Hash, c *rowChunk) {
	sc := hashScratchPool.Get().(*hashScratch)
	defer hashScratchPool.Put(sc)
	b, shapes := sc.b[:0], sc.shapes
	clear(shapes)
	text := func(i uint32) {
		s := ""
		if int(i) < len(c.Strings) {
			s = c.Strings[i]
		}
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	for i := range c.Rows {
		p := &c.Rows[i]
		b = binary.AppendVarint(b, p.Time().UnixNano())
		for _, v := range p.Varied {
			text(v)
		}
		if first, ok := shapes[p.Shape]; ok {
			b = binary.AppendUvarint(b, uint64(first))
		} else {
			shapes[p.Shape] = i
			b = binary.AppendUvarint(b, uint64(i))
			if int(p.Shape) < len(c.Shapes) {
				for _, v := range c.Shapes[p.Shape] {
					text(v)
				}
			}
		}
		for _, t := range p.Tokens {
			b = binary.AppendVarint(b, int64(t))
		}
		b = binary.AppendVarint(b, p.Order)
		b = binary.AppendVarint(b, p.RouteID)
		b = binary.AppendUvarint(b, math.Float64bits(p.Cost))
		b = binary.AppendUvarint(b, math.Float64bits(p.Credits))
		for _, v := range [...]int32{p.Millis, p.TTFT, p.FirstText, p.Sent, p.Flow, p.FirstReasoning, p.Own, p.Retried, p.Queued, p.Masking, p.Status, int32(p.Tries), p.UpBytes, p.DownBytes, p.FilesSaved} {
			b = binary.AppendVarint(b, int64(v))
		}
		b = binary.AppendUvarint(b, uint64(p.Flags))
		if len(b) > 30<<10 {
			h.Write(b)
			b = b[:0]
		}
	}
	h.Write(b)
	sc.b = b
}

// hashScratch is what hashRows writes through, kept between calls: a new
// 32 KB buffer and map for each chunk was 522 MB of a real history's start
// (a digest per session file's chunk), the most of any one function.
type hashScratch struct {
	b      []byte
	shapes map[uint32]int
}

var hashScratchPool = sync.Pool{New: func() any {
	return &hashScratch{b: make([]byte, 0, 32<<10), shapes: map[uint32]int{}}
}}

// heatDiskRef is where a heatmap's settled days are kept and what prices
// and names its calls: the index's root and meta. Empty, they aren't kept.
type heatDiskRef struct {
	root, meta string
}

// The cut moves a day each midnight, and every key names it: the first
// start of each day found none of its sums on disk and paired the whole
// history again, three times over (the page, the heatmap and Codex's
// effort), ~180 MB of a ~480 MB footprint each morning (#2278). Sums
// kept at an earlier cut still stand while that cut's key is the same:
// the days between are added from a pairing of the chunks they reach.

// splitAt is the chunks of sets with rows a pairing from cut on reaches,
// and the rest.
func splitAt(cut time.Time, sets [3][]*rowChunk) (recent, settled [3][]*rowChunk) {
	edge := cut.Add(-heatReach)
	for k, set := range sets {
		for _, c := range set {
			if c.Latest.IsZero() || !c.Latest.Before(edge) {
				recent[k] = append(recent[k], c)
			} else {
				settled[k] = append(settled[k], c)
			}
		}
	}
	return recent, settled
}

// settledAt is sets split at an earlier cut, and the key of its settled
// chunks there, or false.
func settledAt(d heatDiskRef, cut, since time.Time, f Filter, identities map[string]sessions.CodexIdentity, sets [3][]*rowChunk) ([3][]*rowChunk, string, bool) {
	recent, settled := splitAt(cut, sets)
	if _, ok := pairSerials(settled[0], settled[1], settled[2]); !ok {
		return recent, "", false
	}
	key, ok := heatSettledKey(d, cut, since, f, identities, settled, heatBefore(cut, recent))
	return recent, key, ok
}

// chunksIn is how many chunks sets hold.
func chunksIn(sets [3][]*rowChunk) int {
	return len(sets[0]) + len(sets[1]) + len(sets[2])
}
