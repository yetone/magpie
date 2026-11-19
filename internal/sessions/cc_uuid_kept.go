package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The uuids read of each file of a group that starts alike (ccUUIDsOf),
// and of what a copy holds that its keeper doesn't (ccRestOf.ids), are
// kept on disk as well. In memory only, a start had none of them, so the
// first turn after it of a session resumed into copies read every file of
// the group whole: for a long session resumed many times (Claude Code's
// older --resume wrote each a whole copy, ~100 MB a file for the reporter
// on X) that was gigabytes and many seconds on each start after a turn.
// Kept, a file that grew is read on from where it was read to, as it is
// while magpie runs, as long as what was read is still the file's start
// (prefixHash).

// ccUUIDDir holds one file for each file whose uuids are kept, named by a
// hash of its path, so a turn writes the uuids of the file it grew only.
func ccUUIDDir() string { return filepath.Join(filepath.Dir(CachePath()), "cc-uuids") }

func ccUUIDFile(dir, path string) string {
	h := sha256.Sum256([]byte(path))
	return filepath.Join(dir, hex.EncodeToString(h[:12])+".bin")
}

const ccUUIDMagic = "ccu1"

// ccUUIDDirty is the files whose uuids changed since they were written.
// Under mu.
var ccUUIDDirty = map[string]bool{}

// ccUUIDWrites orders the writes of each kept file, by its path. Saved
// waits for them with or without mu, so the map has a lock of its own.
var ccUUIDWrites = struct {
	sync.Mutex
	m map[string]*keptWriter
}{m: map[string]*keptWriter{}}

func ccUUIDWriter(fp string) *keptWriter {
	ccUUIDWrites.Lock()
	defer ccUUIDWrites.Unlock()
	w := ccUUIDWrites.m[fp]
	if w == nil {
		w = &keptWriter{}
		ccUUIDWrites.m[fp] = w
	}
	return w
}

// ccUUIDWait waits for the uuid files being written.
func ccUUIDWait() {
	ccUUIDWrites.Lock()
	ws := make([]*keptWriter, 0, len(ccUUIDWrites.m))
	for _, w := range ccUUIDWrites.m {
		ws = append(ws, w)
	}
	ccUUIDWrites.Unlock()
	for _, w := range ws {
		w.Wait()
	}
}

func encodeUUIDs(path string, m *ccUUIDs) []byte {
	var b bytes.Buffer
	b.WriteString(ccUUIDMagic)
	var n [binary.MaxVarintLen64]byte
	str := func(s string) {
		b.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
		b.WriteString(s)
	}
	num := func(v int64) { b.Write(n[:binary.PutVarint(n[:], v)]) }
	str(path)
	num(m.size)
	num(m.mod)
	num(m.off)
	b.Write(m.hash[:])
	str(m.forked)
	b.Write(n[:binary.PutUvarint(n[:], uint64(len(m.set)))])
	keys := make([][16]byte, 0, len(m.set))
	for k := range m.set {
		keys = append(keys, k)
	}
	// in order, so the same set writes the same bytes
	slices.SortFunc(keys, func(a, c [16]byte) int { return bytes.Compare(a[:], c[:]) })
	for _, k := range keys {
		b.Write(k[:])
	}
	return b.Bytes()
}

var errUUIDFile = errors.New("not a kept uuid file")

func decodeUUIDs(b []byte) (string, *ccUUIDs, error) {
	if !bytes.HasPrefix(b, []byte(ccUUIDMagic)) {
		return "", nil, errUUIDFile
	}
	b = b[len(ccUUIDMagic):]
	bad := false
	uv := func() uint64 {
		v, k := binary.Uvarint(b)
		if k <= 0 {
			bad = true
			return 0
		}
		b = b[k:]
		return v
	}
	num := func() int64 {
		v, k := binary.Varint(b)
		if k <= 0 {
			bad = true
			return 0
		}
		b = b[k:]
		return v
	}
	str := func() string {
		l := uv()
		if bad || l > uint64(len(b)) {
			bad = true
			return ""
		}
		s := string(b[:l])
		b = b[l:]
		return s
	}
	path := str()
	m := &ccUUIDs{}
	m.size, m.mod, m.off = num(), num(), num()
	if bad || len(b) < len(m.hash) {
		return "", nil, errUUIDFile
	}
	copy(m.hash[:], b)
	b = b[len(m.hash):]
	m.forked = str()
	count := uv()
	if bad || count*16 != uint64(len(b)) {
		return "", nil, errUUIDFile
	}
	m.set = make(uuidSet, count)
	for i := uint64(0); i < count; i++ {
		var k [16]byte
		copy(k[:], b[i*16:])
		m.set[k] = struct{}{}
	}
	return path, m, nil
}

// ccUUIDKept is f's uuids as kept on disk, when what they were read from
// is still f's start; nil otherwise, and f is read from its start. Under
// mu.
func ccUUIDKept(f file) *ccUUIDs {
	b, err := os.ReadFile(ccUUIDFile(ccUUIDDir(), f.path))
	if err != nil {
		return nil
	}
	path, m, err := decodeUUIDs(b)
	if err != nil || path != f.path || f.size < m.size || f.size == m.size && f.mod.UnixNano() != m.mod ||
		m.off > f.size || m.hash == (digest{}) || prefixHash(f.path, m.off) != m.hash {
		return nil
	}
	return m
}

// ccUUIDSave writes the uuids that changed. Encoded under mu, written
// behind it.
func ccUUIDSave() {
	if len(ccUUIDDirty) == 0 {
		return
	}
	dir := ccUUIDDir()
	for p := range ccUUIDDirty {
		m := ccUUIDMemo[p]
		if m == nil || m.hash == (digest{}) {
			continue
		}
		fp := ccUUIDFile(dir, p)
		ccUUIDWriter(fp).write(fp, encodeUUIDs(p, m))
	}
	clear(ccUUIDDirty)
}

// ccUUIDDrop lets the kept uuids of the file at path go, as when it is
// moved to the trash. Under mu.
func ccUUIDDrop(path string) {
	delete(ccUUIDMemo, path)
	delete(ccUUIDDirty, path)
	fp := ccUUIDFile(ccUUIDDir(), path)
	ccUUIDWriter(fp).Wait()
	os.Remove(fp)
}

// ccUUIDPruned is the folder of kept uuids last pruned: once a start.
// Under mu.
var ccUUIDPruned string

// ccUUIDPrune removes, once a start, the kept uuids of files that are
// gone. Of the files still there, those in no group that starts alike
// any more stay: a caller judges some of the files only (ccFamily), so
// what it left out says nothing of them.
func ccUUIDPrune() {
	dir := ccUUIDDir()
	if ccUUIDPruned == dir {
		return
	}
	ccUUIDPruned = dir
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".bin") {
			continue
		}
		fp := filepath.Join(dir, n)
		path, ok := ccUUIDPathOf(fp)
		if ok {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				continue
			}
		} else if path == "?" {
			continue // not read: unknown, not gone
		}
		ccUUIDWriter(fp).Wait()
		os.Remove(fp)
	}
}

// ccUUIDPathOf is the path of the file whose uuids fp keeps, read from
// its head; ok false with "?" when fp can't be read, with "" when it is
// not a kept uuid file.
func ccUUIDPathOf(fp string) (string, bool) {
	f, err := os.Open(fp)
	if err != nil {
		return "?", false
	}
	defer f.Close()
	b := make([]byte, 4096+len(ccUUIDMagic)+binary.MaxVarintLen64)
	k, _ := io.ReadFull(f, b)
	b = b[:k]
	if !bytes.HasPrefix(b, []byte(ccUUIDMagic)) {
		return "", false
	}
	b = b[len(ccUUIDMagic):]
	l, n := binary.Uvarint(b)
	if n <= 0 {
		return "", false
	}
	if l > uint64(len(b)-n) {
		return "?", false // a path longer than the head read
	}
	return string(b[n : n+int(l)]), true
}

// What a copy holds that its keeper doesn't is kept with the uuids of the
// messages it was read from (ccRestOf.ids) in cc-rest-ids.bin, beside the
// rests (cc-rest.json), by the same key: without them, a keeper that grew
// after a start had every copy of it read whole again.

func ccRestIDsPath() string { return filepath.Join(filepath.Dir(CachePath()), "cc-rest-ids.bin") }

const ccRestIDsMagic = "ccr1"

var ccRestIDWrites keptWriter

func encodeRestIDs(ids map[string][][16]byte) []byte {
	var b bytes.Buffer
	b.WriteString(ccRestIDsMagic)
	var n [binary.MaxVarintLen64]byte
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b.Write(n[:binary.PutUvarint(n[:], uint64(len(k)))])
		b.WriteString(k)
		b.Write(n[:binary.PutUvarint(n[:], uint64(len(ids[k])))])
		for _, id := range ids[k] {
			b.Write(id[:])
		}
	}
	return b.Bytes()
}

func decodeRestIDs(b []byte) map[string][][16]byte {
	if !bytes.HasPrefix(b, []byte(ccRestIDsMagic)) {
		return nil
	}
	b = b[len(ccRestIDsMagic):]
	out := map[string][][16]byte{}
	for len(b) > 0 {
		l, k := binary.Uvarint(b)
		if k <= 0 || l > uint64(len(b)-k) {
			return nil
		}
		key := string(b[k : k+int(l)])
		b = b[k+int(l):]
		c, k := binary.Uvarint(b)
		if k <= 0 || c > uint64(len(b)-k)/16 {
			return nil
		}
		b = b[k:]
		ids := make([][16]byte, c)
		for i := range ids {
			copy(ids[i][:], b[i*16:])
		}
		b = b[c*16:]
		out[key] = ids
	}
	return out
}
