package usage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The session files' chunks are kept on disk between runs (requests-v1.bin).
// A started magpie opened every session file's call shard (~15.6k of them)
// and turned each call into a row again before its first Requests page
// could show, and so did a page asked for once two idle minutes had trimmed
// the chunks (#3493). An entry stands for its session file while the file's
// size and mtime are the same, as a call shard does, and the whole file
// while the code that wrote it and what prices and names a call (the
// index's root and meta) are the same.
//
// The file is a header, then entries one after another, each its length,
// its CRC-32 and its bytes; a bad entry ends the read, and the entries
// before it stand.

const requestDiskMagic = "magpie-requests-v1\n"

func requestDiskPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "requests-v1.bin")
}

// requestDiskBinary names the code a file was written by: another may make
// a row of a call differently. It was the binary itself, so every upgrade
// read every session file again (~15.6k call shards, a cold start's
// longest step). It was then this package's whole source, which a change
// anywhere in it named anew: on a long history the start after such an
// upgrade read every call shard again, ~14 s of CPU and 2.6 GB of
// allocations, for an edit to the effort or log files (#2278). It is the
// call shards' version and the source of the files a row of a call is made,
// kept and read back through (rowSource).
var requestDiskBinary = sync.OnceValue(func() string {
	return sourceKey(rowSource, sessions.CallCacheVersion())
})

// logDiskBinary names the code log-v1.bin was written by, as
// requestDiskBinary does requests-v1.bin's: the files a log is read into
// blocks, kept and read back through (logSource).
var logDiskBinary = sync.OnceValue(func() string {
	return sourceKey(logSource, "log")
})

// sourceKey is a hash of salt and src's files, or the binary's own name
// when they can't be read, as it was.
func sourceKey(src embed.FS, salt string) string {
	binary := func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		return statKey(exe)
	}
	names, err := fs.Glob(src, "*.go")
	if err != nil || len(names) == 0 {
		return binary()
	}
	h := sha256.New()
	io.WriteString(h, salt)
	for _, name := range names { // in lexical order
		b, err := src.ReadFile(name)
		if err != nil {
			return binary()
		}
		fmt.Fprintf(h, "\x00%s\x00%d\x00", name, len(b))
		h.Write(b)
	}
	return fmt.Sprintf("source %x", h.Sum(nil))
}

// rowSource is the files a session file's chunk of rows is made, written
// and read back through: TestRequestDiskNamesTheCodeOfItsRows finds them
// from its roots, and fails when one is missing or another isn't one.
//
//go:embed block_price.go client_agents.go codex_actors.go codex_identity.go gateway_alias.go ledger.go request_disk.go request_disk_map_other.go request_disk_map_unix.go request_page.go request_snapshot.go request_spill.go served.go session_provider.go usage.go
var rowSource embed.FS

// logSource is the files a log is read into blocks, kept and read back
// through, found as rowSource's are.
//
//go:embed block_price.go client_agents.go codex_actors.go codex_identity.go ledger.go log_change_darwin.go log_change_linux.go log_change_other.go log_change_windows.go log_disk.go log_file.go log_file_other.go log_index.go probes.go refset.go request_cells.go request_disk.go request_disk_map_other.go request_disk_map_unix.go request_page.go request_snapshot.go served.go usage.go
var logSource embed.FS

// requestDiskLeast is how many session files must want reading before the
// file is read for them: a page after a call reads one or two again.
var requestDiskLeast = 64

// diskHeader is the file's first entry.
func diskHeader(binary, root, meta string) []byte {
	var b []byte
	for _, s := range []string{requestDiskMagic, binary, root, meta} {
		b = appendDiskString(b, s)
	}
	return b
}

func appendDiskString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// diskEntry is c as an entry, appended to b[:0], or nil when it can't be
// kept: a chunk of another computer's day, or one whose times are in a
// zone that can't be named again. A write hands each entry the last one's
// b: grown from nil an entry at a time, a first write of 15.7k sessions
// allocated ~300 MB in the seconds after a cold start's first page, and
// the heap grew to 764 MB before a collection, the footprint's peak
// (#2278).
func diskEntry(b []byte, c *rowChunk) []byte {
	if c.Computer != "" {
		return nil
	}
	archive, locations := c.Archive, c.Locations
	if len(archive) == 0 {
		archive, locations = c.encode(func([]byte) bool { return true })
	}
	b = b[:0]
	put := func(v uint64) { b = binary.AppendUvarint(b, v) }
	s := c.Source
	for _, t := range []string{s.Path, s.Key, s.Agent} {
		b = appendDiskString(b, t)
	}
	put(uint64(s.Size))
	put(uint64(s.Modified.UnixNano()))
	put(uint64(c.Count))
	// a chunk without calls has no Latest: the zero time's UnixNano is
	// another time, read back as a call long ago, and such a chunk was
	// settled after a read where it wasn't after a build (heatSplit), so a
	// new version paired its whole history on two starts, not one (#2278)
	latest := int64(0)
	if !c.Latest.IsZero() {
		latest = c.Latest.UnixNano()
	}
	put(uint64(latest))
	put(uint64(len(locations)))
	for _, loc := range locations {
		name, offset := time.Unix(0, 0).In(loc).Zone()
		if loc == time.Local {
			name = "Local"
		} else if loc != time.UTC && loc.String() != name {
			// a zone of the tz database, named by it
			name = "@" + loc.String()
		}
		b = appendDiskString(b, name)
		put(uint64(int64(offset)))
	}
	put(uint64(c.codexActors.len()))
	for i := range c.codexActors.len() {
		b = appendDiskString(b, c.codexActors.at(i))
	}
	put(uint64(len(archive)))
	if !readMapped(c.mapped, func() { b = append(b, archive...) }) {
		return nil
	}
	return b
}

var errDiskEntry = errors.New("bad requests entry")

// readDiskEntry is the chunk an entry holds.
func readDiskEntry(b []byte) (*rowChunk, error) {
	take := func() uint64 {
		n, k := binary.Uvarint(b)
		if k <= 0 {
			b = nil
			return 0
		}
		b = b[k:]
		return n
	}
	text := func() (string, bool) {
		n := take()
		if b == nil || n > uint64(len(b)) {
			return "", false
		}
		s := string(b[:n])
		b = b[n:]
		return s, true
	}
	c := &rowChunk{}
	var ok [3]bool
	c.Source.Path, ok[0] = text()
	c.Source.Key, ok[1] = text()
	c.Source.Agent, ok[2] = text()
	if ok != [3]bool{true, true, true} {
		return nil, errDiskEntry
	}
	c.Source.Size = int64(take())
	c.Source.Modified = time.Unix(0, int64(take()))
	c.Count = int(take())
	if latest := int64(take()); latest != 0 {
		c.Latest = time.Unix(0, latest).In(time.Local)
	}
	n := take()
	if b == nil || n > uint64(len(b)) {
		return nil, errDiskEntry
	}
	for range n {
		name, good := text()
		offset := int(int64(take()))
		if !good || b == nil {
			return nil, errDiskEntry
		}
		var loc *time.Location
		switch {
		case name == "Local":
			loc = time.Local
		case name == "UTC":
			loc = time.UTC
		case len(name) > 0 && name[0] == '@':
			l, err := time.LoadLocation(name[1:])
			if err != nil {
				return nil, err
			}
			loc = l
		default:
			loc = time.FixedZone(name, offset)
		}
		c.Locations = append(c.Locations, loc)
	}
	n = take()
	if b == nil || n > uint64(len(b)) {
		return nil, errDiskEntry
	}
	for range n {
		actor, good := text()
		if !good {
			return nil, errDiskEntry
		}
		c.codexActors.add(actor)
	}
	n = take()
	if b == nil || n != uint64(len(b)) || n == 0 {
		return nil, errDiskEntry
	}
	c.Archive = b
	c.Bytes = int64(len(b)+len(c.Locations)*8) + c.codexActors.bytes()
	return c, nil
}

// diskMapping is the file mapped into memory, where the system can map
// it: the chunks read from it are of its pages, not of the heap. Their
// 41MB of a 421k-call history were a fifth of a warm magpie's heap, and
// the collector let the heap grow by as much again before collecting
// (#3493). It is unmapped once no chunk read from it is left.
type diskMapping struct {
	data []byte
	// spill: a build's own file (archiveSpill), not requests-v1.bin
	spill bool
}

// diskUnmapped, in tests, is told each time a mapping is let go of.
var diskUnmapped atomic.Pointer[func()]

// readMapped runs fn, which reads m's pages (none, with m nil). A fault
// reading them, the file cut short under the mapping by another program,
// is false and not a crash: magpie only ever renames a new file over it.
func readMapped(m *diskMapping, fn func()) (ok bool) {
	if m == nil {
		fn()
		return true
	}
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if r := recover(); r != nil {
			if _, fault := r.(runtime.Error); !fault {
				panic(r)
			}
			ok = false
		}
	}()
	fn()
	runtime.KeepAlive(m)
	return true
}

// readDiskEntries hands each entry of the file at path to each in turn,
// while the file is of binary, root and meta. raw is the entry's bytes,
// whole, to be written on as they are; with m, they are of m's pages,
// read-only, and good while m is.
func readDiskEntries(path, binary, root, meta string, each func(raw []byte, m *diskMapping) bool) {
	if m, ok := mapDiskFile(path); ok {
		readMapped(m, func() {
			header, b, ok := cutDiskEntry(m.data)
			if !ok || string(header) != string(diskHeader(binary, root, meta)) {
				return
			}
			for {
				var raw []byte
				if raw, b, ok = cutDiskEntry(b); !ok || !each(raw, m) {
					return
				}
			}
		})
		m.read()
		return
	}
	in, err := os.Open(path)
	if err != nil {
		return
	}
	defer in.Close()
	r := bufio.NewReaderSize(in, 1<<20)
	header, ok := nextDiskEntry(r)
	if !ok || string(header) != string(diskHeader(binary, root, meta)) {
		return
	}
	for {
		raw, ok := nextDiskEntry(r)
		if !ok || !each(raw, nil) {
			return
		}
	}
}

// cutDiskEntry is nextDiskEntry of b, and what follows it. The entry can't
// be appended to in place: b may be a read-only mapping.
func cutDiskEntry(b []byte) (entry, rest []byte, ok bool) {
	n, k := binary.Uvarint(b)
	if k <= 0 || n == 0 || n > 256<<20 || uint64(len(b)-k) < 4+n {
		return nil, nil, false
	}
	sum := binary.LittleEndian.Uint32(b[k:])
	end := k + 4 + int(n)
	entry = b[k+4 : end : end]
	if crc32.ChecksumIEEE(entry) != sum {
		return nil, nil, false
	}
	return entry, b[end:], true
}

func nextDiskEntry(r *bufio.Reader) ([]byte, bool) {
	n, err := binary.ReadUvarint(r)
	if err != nil || n == 0 || n > 256<<20 {
		return nil, false
	}
	var sum [4]byte
	if _, err := io.ReadFull(r, sum[:]); err != nil {
		return nil, false
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil || crc32.ChecksumIEEE(b) != binary.LittleEndian.Uint32(sum[:]) {
		return nil, false
	}
	return b, true
}

func writeDiskEntry(w *bufio.Writer, b []byte) error {
	var head [binary.MaxVarintLen64 + 4]byte
	n := binary.PutUvarint(head[:], uint64(len(b)))
	binary.LittleEndian.PutUint32(head[n:], crc32.ChecksumIEEE(b))
	if _, err := w.Write(head[:n+4]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// fromDisk puts in idx the chunks the file holds of the sources in want,
// each still the size and mtime it was. idx is a private copy (private).
func (idx *requestIndex) fromDisk(want map[string]sessions.CallSource) {
	readDiskEntries(requestDiskPath(), requestDiskBinary(), idx.root, idx.meta, func(raw []byte, m *diskMapping) bool {
		c, err := readDiskEntry(raw)
		if err != nil {
			return false
		}
		c.mapped = m
		s, ok := want[c.Source.Path]
		if !ok || s.Size != c.Source.Size || !s.Modified.Equal(c.Source.Modified) {
			return true
		}
		c.Source = s
		c.serial = sourceSerial(idx.root, idx.meta, s)
		c.used = useAt(idx.tick)
		idx.chunks[s.Path] = c
		return true
	})
}

// toDisk writes chunks, and the entries of the file before it for the
// paths in on that chunks doesn't hold, as the file for root and meta.
func toDisk(root, meta string, chunks map[string]*rowChunk, on map[string]bool) error {
	path, binary := requestDiskPath(), requestDiskBinary()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), "requests-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	w := bufio.NewWriterSize(out, 1<<20)
	err = writeDiskEntry(w, diskHeader(binary, root, meta))
	var buf []byte
	for _, c := range chunks {
		if err != nil {
			break
		}
		if b := diskEntry(buf, c); b != nil {
			err = writeDiskEntry(w, b)
			buf = b
		}
	}
	if err == nil {
		readDiskEntries(path, binary, root, meta, func(raw []byte, _ *diskMapping) bool {
			p, ok := diskEntryPath(raw)
			if !ok {
				return false
			}
			if on[string(p)] && chunks[string(p)] == nil {
				err = writeDiskEntry(w, raw)
			}
			return err == nil
		})
	}
	if err == nil {
		err = w.Flush()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}

// onDisk is a copy of each chunk of chunks whose Archive is of the heap
// or a build's spill, with the same Archive of the pages of the file toDisk just wrote; save
// swaps them in for the chunks of the same serial. Made
// in this run, they held a cold build's archives on the heap until the
// next start: 32MB of 15.7k sessions, and a resting heap 40MB over a warm
// start's (#3493). Nothing on a system that maps no file.
func onDisk(root, meta string, chunks map[string]*rowChunk) map[string]*rowChunk {
	kept := map[string]*rowChunk{}
	readDiskEntries(requestDiskPath(), requestDiskBinary(), root, meta, func(raw []byte, m *diskMapping) bool {
		if m == nil {
			return false
		}
		path, ok := diskEntryPath(raw)
		if !ok {
			return false
		}
		c := chunks[string(path)]
		if c == nil || c.serial == 0 || c.pagesRank() == 2 || len(c.Archive) == 0 {
			return true
		}
		size, modified, archive, ok := diskEntryArchive(raw)
		if !ok || size != c.Source.Size || modified != c.Source.Modified.UnixNano() || !bytes.Equal(archive, c.Archive) {
			return true
		}
		next := *c
		next.Archive, next.mapped = archive, m
		kept[string(path)] = &next
		return true
	})
	return kept
}

// diskEntryPath is the path of the source an entry is of, of b's bytes.
func diskEntryPath(b []byte) ([]byte, bool) {
	n, k := binary.Uvarint(b)
	if k <= 0 || n > uint64(len(b)-k) {
		return nil, false
	}
	return b[k : k+int(n)], true
}

// diskEntryArchive is the size and mtime of the source an entry is of, and
// its archive, of b's bytes: what onDisk compares, with nothing else of
// the entry copied out (readDiskEntry's strings were ~95 MB of a first
// write's ~440 MB, #2278).
func diskEntryArchive(b []byte) (size, modified int64, archive []byte, ok bool) {
	take := func() uint64 {
		n, k := binary.Uvarint(b)
		if k <= 0 {
			b = nil
			return 0
		}
		b = b[k:]
		return n
	}
	skip := func() bool {
		n := take()
		if b == nil || n > uint64(len(b)) {
			return false
		}
		b = b[n:]
		return true
	}
	for range 3 { // path, key, agent
		if !skip() {
			return 0, 0, nil, false
		}
	}
	size, modified = int64(take()), int64(take())
	take() // count
	take() // latest
	n := take()
	if b == nil || n > uint64(len(b)) {
		return 0, 0, nil, false
	}
	for range n { // a zone's name and offset
		if !skip() {
			return 0, 0, nil, false
		}
		if take(); b == nil {
			return 0, 0, nil, false
		}
	}
	n = take()
	if b == nil || n > uint64(len(b)) {
		return 0, 0, nil, false
	}
	for range n { // actors
		if !skip() {
			return 0, 0, nil, false
		}
	}
	n = take()
	if b == nil || n != uint64(len(b)) || n == 0 {
		return 0, 0, nil, false
	}
	return size, modified, b, true
}
