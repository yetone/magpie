package usage

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The log's full blocks are kept on disk between runs (log-v1.bin). A
// started magpie parsed the whole log again, a record at a time, before
// its first page: a long history's 109 MB took ~620 MB of allocations in
// the first seconds, the start's largest part, and its collections (#2278).
// The file stands for the log's first bytes while their SHA-256 is the
// one it was written with, under the same code (logDiskBinary); a
// start reads the blocks from it and parses only the lines after them, as
// a parse continues the snapshot before it.

const logDiskMagic = "magpie-log-v2\n"

func logDiskPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "log-v1.bin")
}

// logFull is a snapshot as it was where its full blocks end: how many,
// the log's bytes up to the last of them, their hash and its running
// state, and what the lines up to there said. probes is how many of the
// probe blocks were full there: a log of MCP tool calls has no gateway
// block at all, and with none it was never kept, so every start parsed
// its 25k lines again (~60 MB of allocations, #2278).
type logFull struct {
	n               int
	probes          int
	off             int64
	hash            string
	digest          []byte
	first, firstOwn time.Time
	keyProviders    map[string]bool
	hosts           map[string]string
}

// logDiskEvery bounds how often the file is written again: a block fills
// every logBlockRows calls, and a start parses what came after the last
// write.
const logDiskEvery = 10 * time.Minute

// logDiskLoaded, in tests, is told how many blocks a start read from the
// file.
var logDiskLoaded func(n int)

var logDiskWrote struct {
	sync.Mutex
	path string
	off  int64
	at   time.Time
}

// logDisk is the snapshot a start continues from: the file's blocks and
// what was read with them, or nil.
type logDisk struct {
	full   logFull
	probes []*rowChunk
	blocks []*rowChunk
}

type logDiskProbe struct {
	R   Record `json:"r"`
	Off int64  `json:"o"`
}

// logDiskRead is the file's snapshot of the log at path, when the log's
// first bytes are still the ones it was written of. Where the system maps
// the file, the blocks are of its pages, as requests-v1.bin's are
// (diskMapping): read into the heap, a 240k-call history's 7 MB of them
// stayed there for good, and the collector let the heap grow by as much
// again (#2278).
func logDiskRead(path string, size int64) *logDisk {
	if m, ok := mapDiskFile(logDiskPath()); ok {
		var d *logDisk
		if !readMapped(m, func() { d = logDiskOf(m.data, m, path, size) }) {
			d = nil
		}
		m.read()
		return d
	}
	b, err := os.ReadFile(logDiskPath())
	if err != nil {
		return nil
	}
	return logDiskOf(b, nil, path, size)
}

// logDiskOf is logDiskRead of the file's bytes b, of m's pages with m.
func logDiskOf(b []byte, m *diskMapping, path string, size int64) *logDisk {
	header, b, ok := cutDiskEntry(b)
	if !ok {
		return nil
	}
	take := func() uint64 {
		n, k := binary.Uvarint(header)
		if k <= 0 {
			header = nil
			return 0
		}
		header = header[k:]
		return n
	}
	text := func() string {
		n := take()
		if header == nil || n > uint64(len(header)) {
			header = nil
			return ""
		}
		s := string(header[:n])
		header = header[n:]
		return s
	}
	if text() != logDiskMagic || text() != logDiskBinary() || text() != path || header == nil {
		return nil
	}
	d := &logDisk{full: logFull{keyProviders: map[string]bool{}, hosts: map[string]string{}}}
	d.full.hash, d.full.digest = text(), []byte(text())
	d.full.off, d.full.n, d.full.probes = int64(take()), int(take()), int(take())
	for _, t := range []*time.Time{&d.full.first, &d.full.firstOwn} {
		if s := text(); s != "" && t.UnmarshalText([]byte(s)) != nil {
			return nil
		}
	}
	for range take() {
		d.full.keyProviders[text()] = true
	}
	for range take() {
		k := text()
		d.full.hosts[k] = text()
	}
	if header == nil || len(header) != 0 || d.full.off > size || d.full.hash == "" || logRecordHash(path, d.full.off) != d.full.hash {
		return nil
	}
	chunk := func() *rowChunk {
		var raw []byte
		raw, b, ok = cutDiskEntry(b)
		if !ok {
			return nil
		}
		c, err := readDiskEntry(raw)
		if err != nil {
			return nil
		}
		c.serial, c.mapped = nextChunkSerial(), m
		return c
	}
	kept := probeRows{}
	for range d.full.probes {
		c := chunk()
		if c == nil || c.Count != logBlockRows {
			return nil
		}
		kept.blocks = append(kept.blocks, c)
	}
	// the probes after the full probe blocks, up to the full point
	raw, b, ok := cutDiskEntry(b)
	var probes []logDiskProbe
	if !ok || json.Unmarshal(raw, &probes) != nil {
		return nil
	}
	for _, p := range probes {
		kept.add(p.R, p.Off)
	}
	d.probes = kept.done()
	for range d.full.n {
		c := chunk()
		if c == nil {
			return nil
		}
		d.blocks = append(d.blocks, c)
	}
	if len(b) != 0 {
		return nil
	}
	return d
}

// logDiskKept notes that the file holds the log at path up to off.
func logDiskKept(path string, off int64) {
	logDiskWrote.Lock()
	logDiskWrote.path, logDiskWrote.off, logDiskWrote.at = path, off, time.Now()
	logDiskWrote.Unlock()
}

// logDiskWrite keeps s's full blocks, when they reach further than the
// file was last written with and logDiskEvery has passed since.
func logDiskWrite(s *logSnapshot) {
	if s.full.off == 0 || s.full.hash == "" || s.full.digest == nil {
		return
	}
	logDiskWrote.Lock()
	defer logDiskWrote.Unlock()
	w := &logDiskWrote
	if w.path == s.path && (s.full.off <= w.off || time.Since(w.at) < logDiskEvery) {
		return
	}
	if logDiskSave(s) == nil {
		w.path, w.off, w.at = s.path, s.full.off, time.Now()
	}
}

func logDiskSave(s *logSnapshot) error {
	var h []byte
	for _, t := range []string{logDiskMagic, logDiskBinary(), s.path, s.full.hash, string(s.full.digest)} {
		h = appendDiskString(h, t)
	}
	h = binary.AppendUvarint(h, uint64(s.full.off))
	h = binary.AppendUvarint(h, uint64(s.full.n))
	h = binary.AppendUvarint(h, uint64(s.full.probes))
	for _, t := range []time.Time{s.full.first, s.full.firstOwn} {
		text := ""
		if !t.IsZero() {
			b, err := t.MarshalText()
			if err != nil {
				return err
			}
			text = string(b)
		}
		h = appendDiskString(h, text)
	}
	h = binary.AppendUvarint(h, uint64(len(s.full.keyProviders)))
	for k := range s.full.keyProviders {
		h = appendDiskString(h, k)
	}
	h = binary.AppendUvarint(h, uint64(len(s.full.hosts)))
	for k, v := range s.full.hosts {
		h = appendDiskString(appendDiskString(h, k), v)
	}
	// The full probe blocks go as entries; the probes after them, of the
	// lines up to the full point, as records: the lines after are parsed
	// again. All of them went as records, and a start parsed them back
	// one at a time.
	if s.full.probes > len(s.probes) {
		return errDiskEntry
	}
	probes := []logDiskProbe{}
	eachProbe(s.probes[s.full.probes:], time.Time{}, func(r Record, off int64) {
		if off <= s.full.off {
			probes = append(probes, logDiskProbe{r, off})
		}
	})
	pb, err := json.Marshal(probes)
	if err != nil {
		return err
	}
	path := logDiskPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), "log-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	bw := bufio.NewWriterSize(out, 1<<20)
	err = writeDiskEntry(bw, h)
	var buf []byte
	chunks := append(slices.Clone(s.probes[:s.full.probes]), nil)
	chunks = append(chunks, s.blocks[:s.full.n]...)
	for _, c := range chunks {
		if err != nil {
			break
		}
		if c == nil {
			err = writeDiskEntry(bw, pb)
			continue
		}
		b := diskEntry(buf, c)
		if b == nil {
			err = errDiskEntry
			break
		}
		err = writeDiskEntry(bw, b)
		buf = b
	}
	if err == nil {
		err = bw.Flush()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
