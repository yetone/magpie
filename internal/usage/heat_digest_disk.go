package usage

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/yetone/magpie/internal/sessions"
)

// A session file's chunk is named in the heatmap's key by a digest of its
// rows (sourceDigest), and each start hashed every chunk's rows again to
// make them: ~0.15 s of CPU on a real history, and every chunk unpacked
// for it (#2278). A chunk's serial names its file as read under one root
// and meta (sourceSerial), the same in every run, so the digests are kept
// on disk by serial, under the code that makes the rows
// (requestDiskBinary): another build's rows are hashed again.

// sourceDigestsDisk is what was read of heat-digests.bin. Under
// sourceDigests' lock.
var sourceDigestsDisk struct {
	path  string     // where kept was read from
	kept  digestList // as read: no map of 17k entries beside sourceDigests'
	dirty bool       // a digest was made that isn't on disk
	// mapped holds the file kept is read from, where the system maps
	// files: its pages are the file's, not the heap's, and a lookup
	// touches a few of them. The file is 40 bytes a digest, up to
	// 2*sourceDigestsKept of them (5MB), each kept in the heap before.
	mapped *diskMapping
}

// sourceDigestsHashed counts the chunks sourceDigest hashed, for tests.
var sourceDigestsHashed int

func sourceDigestsPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "heat-digests.bin")
}

const sourceDigestsMagic = "magpie-heat-digests-2\n"

// digestList is serials and their digests, each 8+32 bytes, in the
// serials' order.
type digestList []byte

const digestLen = 8 + sha256.Size

func (l digestList) len() int { return len(l) / digestLen }

func (l digestList) at(i int) (uint64, []byte) {
	e := l[i*digestLen : (i+1)*digestLen]
	return binary.LittleEndian.Uint64(e), e[8:]
}

// find is serial's index in l.
func (l digestList) find(serial uint64) (int, bool) {
	i := sort.Search(l.len(), func(i int) bool { s, _ := l.at(i); return s >= serial })
	if i == l.len() {
		return i, false
	}
	s, _ := l.at(i)
	return i, s == serial
}

func (l digestList) get(serial uint64) (sum [sha256.Size]byte, ok bool) {
	i, ok := l.find(serial)
	if ok {
		_, b := l.at(i)
		copy(sum[:], b)
	}
	return sum, ok
}

// sourceDigestsLoad reads the kept digests, once for each place they are
// kept in. A file that can't be read, or was written by other row code,
// is nothing kept. Under sourceDigests' lock.
func sourceDigestsLoad() {
	p := sourceDigestsPath()
	if sourceDigestsDisk.path == p {
		return
	}
	sourceDigestsDisk.path, sourceDigestsDisk.kept, sourceDigestsDisk.dirty = p, nil, false
	sourceDigestsDisk.mapped = nil
	if m, ok := mapDiskFile(p); ok {
		if l := decodeSourceDigests(m.data, requestDiskBinary()); l != nil {
			sourceDigestsDisk.kept, sourceDigestsDisk.mapped = l, m
		}
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	sourceDigestsDisk.kept = decodeSourceDigests(b, requestDiskBinary())
}

// keepMapped reads kept again from p as just written, mapped: the bytes
// encoded are let go of. Kept as encoded when the file isn't b (another
// write since) or can't be mapped. Under sourceDigests' lock.
func keepMapped(p string, b []byte) {
	if sourceDigestsDisk.path != p || len(sourceDigestsDisk.kept) == 0 || &sourceDigestsDisk.kept[0] != &b[len(b)-len(sourceDigestsDisk.kept)] {
		return // kept is no longer what b holds
	}
	m, ok := mapDiskFile(p)
	if !ok || !bytes.Equal(m.data, b) {
		return
	}
	sourceDigestsDisk.kept = decodeSourceDigests(m.data, requestDiskBinary())
	sourceDigestsDisk.mapped = m
}

// decodeSourceDigests is b's digests when b was written for code, nil
// when not, when b is cut short or out of order.
func decodeSourceDigests(b []byte, code string) digestList {
	if !bytes.HasPrefix(b, []byte(sourceDigestsMagic)) {
		return nil
	}
	b = b[len(sourceDigestsMagic):]
	n, k := binary.Uvarint(b)
	if k <= 0 || uint64(len(b)-k) < n || string(b[k:k+int(n)]) != code {
		return nil
	}
	l := digestList(b[k+int(n):])
	if len(l)%digestLen != 0 {
		return nil
	}
	for i := 1; i < l.len(); i++ {
		if a, _ := l.at(i - 1); a >= binary.LittleEndian.Uint64(l[i*digestLen:]) {
			return nil
		}
	}
	return l
}

// encodeSourceDigests writes, for code, the digests of the serials the
// sets name, the first set's first, then the rest of kept's, at most limit
// of them. A serial's digest is made's, or kept's; one in neither is left
// out.
func encodeSourceDigests(code string, limit int, kept digestList, made map[uint64][sha256.Size]byte, sets ...map[uint64]struct{}) []byte {
	type one struct {
		serial uint64
		sum    []byte
	}
	var all []one
	seen := map[uint64]bool{}
	for _, s := range sets {
		for serial := range s {
			if seen[serial] || len(all) >= limit {
				continue
			}
			if sum, ok := made[serial]; ok {
				seen[serial] = true
				all = append(all, one{serial, append([]byte(nil), sum[:]...)})
			} else if i, ok := kept.find(serial); ok {
				seen[serial] = true
				_, sum := kept.at(i)
				all = append(all, one{serial, sum})
			}
		}
	}
	for i := 0; i < kept.len() && len(all) < limit; i++ {
		if serial, sum := kept.at(i); !seen[serial] {
			all = append(all, one{serial, sum})
		}
	}
	slices.SortFunc(all, func(a, b one) int { return cmp.Compare(a.serial, b.serial) })
	b := make([]byte, 0, len(sourceDigestsMagic)+binary.MaxVarintLen64+len(code)+len(all)*digestLen)
	b = append(b, sourceDigestsMagic...)
	b = binary.AppendUvarint(b, uint64(len(code)))
	b = append(b, code...)
	for _, e := range all {
		b = binary.LittleEndian.AppendUint64(b, e.serial)
		b = append(b, e.sum...)
	}
	return b
}

// sourceDigestWrites puts the writes in order: one encoded before the
// last written is not written over it.
var sourceDigestWrites struct {
	sync.Mutex
	made, wrote int // generations: under sourceDigests' lock, and under the Mutex
}

// sourceDigestsSave writes the digests of this run, when one was made that
// isn't on disk: those of the chunks hashed or used since the last turn
// and before it. Encoded under sourceDigests' lock, written behind it, as
// heat-v1.json is: once a start, and again when a file changed.
func sourceDigestsSave() {
	d := &sourceDigests
	d.Lock()
	if !sourceDigestsDisk.dirty || sourceDigestsDisk.path == "" {
		d.Unlock()
		return
	}
	sourceDigestsDisk.dirty = false
	// this run's first, then those kept from before it it hasn't met (a
	// query of a few files keeps the rest): a file gone falls out once
	// two turns' worth are made
	code := requestDiskBinary()
	b := encodeSourceDigests(code, 2*sourceDigestsKept, sourceDigestsDisk.kept, d.made, d.by, d.last)
	// what is written is what is kept: the digests made are in it
	sourceDigestsDisk.kept = decodeSourceDigests(b, code)
	d.made = nil
	p := sourceDigestsDisk.path
	w := &sourceDigestWrites
	w.made++
	gen := w.made
	d.Unlock()
	w.Lock()
	defer w.Unlock()
	if gen < w.wrote {
		return
	}
	w.wrote = gen
	if !writeFileRenamed(p, b) || !mapsFiles {
		return
	}
	d.Lock()
	keepMapped(p, b)
	d.Unlock()
}

// writeFileRenamed writes b to p through a temp file renamed over it.
// It reports whether p is b now.
func writeFileRenamed(p string, b []byte) bool {
	saveAsks() // the asks its rows were made of first (catalogBase)
	dir := filepath.Dir(p)
	if os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	tmp, err := os.CreateTemp(dir, ".heat-digests-*")
	if err != nil {
		return false
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
		return false
	}
	return true
}
