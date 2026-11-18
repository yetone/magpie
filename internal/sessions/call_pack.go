package sessions

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// A cold read of a Claude Code history wrote each session's calls to a
// shard of its own, made, closed and renamed for each of ~12,000
// sessions (and a revisions file beside the recent ones). On Windows
// Defender scans every file as it is closed, and those writes were 15 s of
// a Sessions page's 19 s. The calls a Sessions read rides along for an
// idle session are kept in packs instead: one file for many sessions,
// written once a pass (or every packFlushBytes), each session's frames as
// its shard holds them. A session being written keeps its own shard and
// revisions, so the lines it gains are appended there.
//
// A session's own shard, written by any later read of it, is read before
// its pack's section, and a pack is never written to again. A pack that
// can't be read whole (cut off, its trailer or a frame's CRC wrong) is as
// if it were not there: its sessions' calls are read again from their
// files.

const packMagic = "MPCALLS1"

// packFlushBytes is how much of a pass is held before it goes to a pack.
const packFlushBytes = 32 << 20

type packEntry struct {
	Path   string
	Off, N int64 // the calls' frames
}

// packRef is where a session's packed calls are: in file at the entry's
// offsets, or, before its pack is written, in mem.
type packRef struct {
	file string
	e    packEntry
	mem  []byte
}

var callPacks struct {
	sync.Mutex
	root    string // the calls dir byPath is of
	loaded  bool
	byPath  map[string]*packRef
	pending []*packRef
	bytes   int
	seq     int
}

// packsFor makes byPath that of root, reading the packs there once. Held
// with callPacks' lock.
func packsFor(root string) {
	if callPacks.root == root && callPacks.loaded {
		return
	}
	callPacks.root, callPacks.loaded = root, true
	callPacks.byPath = map[string]*packRef{}
	callPacks.pending, callPacks.bytes = nil, 0
	names, _ := filepath.Glob(filepath.Join(root, "*.pack"))
	slices.Sort(names) // named by when written: a later pack's entry stands
	for _, name := range names {
		for _, e := range readPackIndex(name) {
			callPacks.byPath[e.Path] = &packRef{file: name, e: e}
		}
	}
}

// readPackIndex is the entries of the pack at name, or none when it can't
// be read whole.
func readPackIndex(name string) []packEntry {
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	tail := int64(len(packMagic) + 8)
	if err != nil || st.Size() < int64(len(packMagic))+tail {
		return nil
	}
	head := make([]byte, len(packMagic))
	end := make([]byte, tail)
	if _, err := f.ReadAt(head, 0); err != nil || string(head) != packMagic {
		return nil
	}
	if _, err := f.ReadAt(end, st.Size()-tail); err != nil || string(end[8:]) != packMagic {
		return nil
	}
	n := int64(binary.LittleEndian.Uint32(end[:4]))
	at := st.Size() - tail - n
	if n == 0 || at < int64(len(packMagic)) {
		return nil
	}
	b := make([]byte, n)
	if _, err := f.ReadAt(b, at); err != nil || crc32.ChecksumIEEE(b) != binary.LittleEndian.Uint32(end[4:8]) {
		return nil
	}
	var es []packEntry
	if gob.NewDecoder(bytes.NewReader(b)).Decode(&es) != nil {
		return nil
	}
	for _, e := range es {
		if e.Off < int64(len(packMagic)) || e.N <= 0 || e.Off+e.N > at {
			return nil
		}
	}
	return es
}

// packCalls holds st's calls, with no revisions window, for the pass's
// pack.
func packCalls(st *callFile) {
	var b bytes.Buffer
	if !encodeCalls(&b, st, 0) {
		return
	}
	root := callCacheDir()
	callPacks.Lock()
	packsFor(root)
	r := &packRef{e: packEntry{Path: st.Path}, mem: b.Bytes()}
	callPacks.byPath[st.Path] = r
	callPacks.pending = append(callPacks.pending, r)
	callPacks.bytes += len(r.mem)
	full := callPacks.bytes >= packFlushBytes
	callPacks.Unlock()
	if full {
		flushCallPack()
	}
}

// flushCallPack writes the calls packCalls holds to a pack of their own.
func flushCallPack() {
	callPacks.Lock()
	refs, root := callPacks.pending, callPacks.root
	callPacks.pending, callPacks.bytes = nil, 0
	callPacks.seq++
	name := filepath.Join(root, time.Now().UTC().Format("20060102T150405.000000000")+"-"+strconv.Itoa(callPacks.seq)+".pack")
	callPacks.Unlock()
	if len(refs) == 0 {
		return
	}
	es, ok := writePack(name, refs)
	callPacks.Lock()
	defer callPacks.Unlock()
	for i, r := range refs {
		if callPacks.root != root || callPacks.byPath[r.e.Path] != r {
			continue // forgotten (Reset), or packed again since
		}
		if ok {
			callPacks.byPath[r.e.Path] = &packRef{file: name, e: es[i]}
		} else {
			// not on disk: read again from its file next time
			delete(callPacks.byPath, r.e.Path)
		}
	}
}

// writePack writes refs to a pack at name, through a temp file renamed
// over it, and says where each one's bytes went.
func writePack(name string, refs []*packRef) ([]packEntry, bool) {
	dir := filepath.Dir(name)
	if os.MkdirAll(dir, 0700) != nil {
		return nil, false
	}
	out, err := os.CreateTemp(dir, "calls-*.tmp")
	if err != nil {
		return nil, false
	}
	defer os.Remove(out.Name())
	w := &countingWriter{w: out}
	ok := writeAll(w, []byte(packMagic))
	es := make([]packEntry, len(refs))
	for i, r := range refs {
		es[i] = packEntry{Path: r.e.Path, Off: w.n, N: int64(len(r.mem))}
		ok = ok && writeAll(w, r.mem)
	}
	var idx bytes.Buffer
	ok = ok && gob.NewEncoder(&idx).Encode(es) == nil
	var end [8]byte
	binary.LittleEndian.PutUint32(end[:4], uint32(idx.Len()))
	binary.LittleEndian.PutUint32(end[4:], crc32.ChecksumIEEE(idx.Bytes()))
	ok = ok && writeAll(w, idx.Bytes()) && writeAll(w, end[:]) && writeAll(w, []byte(packMagic))
	if cerr := out.Close(); !ok || cerr != nil || os.Rename(out.Name(), name) != nil {
		return nil, false
	}
	return es, true
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

func writeAll(w io.Writer, b []byte) bool {
	_, err := w.Write(b)
	return err == nil
}

// packedCalls is the frames kept in a pack for path; ok false when it has
// none or they can't be read.
func packedCalls(path string) (calls []byte, ok bool) {
	root := callCacheDir()
	callPacks.Lock()
	packsFor(root)
	r := callPacks.byPath[path]
	callPacks.Unlock()
	if r == nil {
		return nil, false
	}
	if r.mem != nil {
		return r.mem, true
	}
	f, err := os.Open(r.file)
	if err != nil {
		forgetPacked(path, r) // removed: its calls are read again
		return nil, false
	}
	defer f.Close()
	calls = make([]byte, r.e.N)
	if _, err := f.ReadAt(calls, r.e.Off); err != nil {
		forgetPacked(path, r)
		return nil, false
	}
	return calls, true
}

// callsPacked says whether a pack holds path's calls.
func callsPacked(path string) bool {
	root := callCacheDir()
	callPacks.Lock()
	defer callPacks.Unlock()
	packsFor(root)
	r := callPacks.byPath[path]
	if r != nil && r.file != "" {
		if _, err := os.Stat(r.file); err != nil {
			delete(callPacks.byPath, path)
			return false
		}
	}
	return r != nil
}

// forgetPacked forgets path's packed calls when they are still r.
func forgetPacked(path string, r *packRef) {
	callPacks.Lock()
	if callPacks.byPath[path] == r {
		delete(callPacks.byPath, path)
	}
	callPacks.Unlock()
}

// forgetCallPacks writes what is held and forgets the packs read, for a
// Reset or another HOME.
func forgetCallPacks() {
	flushCallPack()
	callPacks.Lock()
	callPacks.root, callPacks.loaded, callPacks.byPath = "", false, nil
	callPacks.Unlock()
}

// sweepCallPacks removes the packs in root none of whose sessions is
// still listed (on), and forgets them. A pack written in the last minute
// is left: its sessions may not point at it yet (flushCallPack).
func sweepCallPacks(root string, on map[string]struct{}) {
	callPacks.Lock()
	packsFor(root)
	live := map[string]bool{}
	for p, r := range callPacks.byPath {
		if r.file == "" {
			continue
		}
		if _, ok := on[p]; ok {
			live[r.file] = true
		}
	}
	callPacks.Unlock()
	names, _ := filepath.Glob(filepath.Join(root, "*.pack"))
	for _, name := range names {
		if live[name] {
			continue
		}
		if st, err := os.Stat(name); err != nil || time.Since(st.ModTime()) < time.Minute {
			continue
		}
		callPacks.Lock()
		for p, r := range callPacks.byPath {
			if r.file == name {
				delete(callPacks.byPath, p)
			}
		}
		callPacks.Unlock()
		_ = os.Remove(name)
	}
}
