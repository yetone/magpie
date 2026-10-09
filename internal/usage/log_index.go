package usage

// The JSONL file remains the ledger. This disposable index keeps immutable,
// compressed blocks of its metadata; appending copies at most the last block.
// Readers decode only blocks which can overlap their time window.
import (
	"bufio"
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"time"
)

const (
	logBlockRows   = 512
	logStampSettle = time.Second
)

// Raw and priced chunks share the request cache budget. Oversized histories
// remain queryable but their raw blocks are rebuilt instead of retained.

type logSnapshot struct {
	bytes        int64
	uncached     bool
	settled      bool
	path         string
	info         os.FileInfo
	off          int64
	hash         string // SHA-256 of the log's first off bytes, the ones parsed
	digest       []byte // the hash's running state, which the next parse continues
	version      uint64
	blocks       []*rowChunk
	first        time.Time
	keyProviders map[string]bool
}

var logIndex struct {
	sync.Mutex
	snapshot *logSnapshot
	version  uint64
}

var logIndexNow = time.Now
var logRecordHash = recordHash

// File writes must never wait for a historical index rebuild.
var logAppends struct {
	sync.Mutex
	path       string
	base, last os.FileInfo
}

func readLogSnapshot() *logSnapshot { return logSnapshotFor(false) }

// Metadata-only callers can use cached results without rebuilding a history
// whose blocks do not fit the cache budget.
func logSnapshotFor(metadataOnly bool) *logSnapshot {
	logIndex.Lock()
	defer logIndex.Unlock()
	path := Path()
	start := logIndexNow()
	info, err := statLogFile(path)
	settled := settledLogStamp(info, start)
	old := logIndex.snapshot
	unchanged := old != nil && old.path == path && (err != nil && old.info == nil || err == nil && sameLogInfo(old.info, info))
	if unchanged && info != nil {
		if logChangeStamp(info) == "" || !old.settled {
			unchanged = old.hash != "" && logRecordHash(path, old.off) == old.hash
			if unchanged && settled {
				promoted := *old
				promoted.info, promoted.settled = info, true
				old = &promoted
				logIndex.snapshot = old
			}
		} else {
			unchanged = old.settled && settled
		}
	}
	if unchanged && (!old.uncached || metadataOnly) {
		return old
	}
	if !unchanged {
		logIndex.version++
	}

	next := &logSnapshot{path: path, info: info, settled: settled, version: logIndex.version, keyProviders: map[string]bool{}}
	if err != nil {
		logIndex.snapshot = next
		return next
	}
	logAppends.Lock()
	notePath, noteBase, noteLast := logAppends.path, logAppends.base, logAppends.last
	logAppends.Unlock()
	trusted := old != nil && old.settled && logChangeStamp(info) != "" && notePath == path && sameLogInfo(oldInfo(old), noteBase) && sameLogInfo(info, noteLast)
	continued := old != nil && old.path == path && old.info != nil && sameLogFile(old.info, info) && info.Size() > old.info.Size() && (trusted || old.hash != "" && logRecordHash(path, old.off) == old.hash)
	// The parse is the fingerprint: it hashes the lines it reads, continuing
	// the old snapshot's hash when it continues its blocks.
	sum := sha256.New()
	if continued && !old.uncached && resumeLogHash(sum, old.digest) {
		next.off, next.first = old.off, old.first
		next.blocks = slices.Clone(old.blocks)
		next.keyProviders = maps.Clone(old.keyProviders)
	}
	f, err := os.Open(path)
	if err != nil {
		return next
	} // never publish an incomplete read as current
	defer f.Close()
	if _, err = f.Seek(next.off, 0); err != nil {
		return next
	}
	var tail *rowChunk
	if n := len(next.blocks); n > 0 && next.blocks[n-1].Count < logBlockRows {
		tail = next.blocks[n-1].writable()
		next.blocks = next.blocks[:n-1]
	}
	rd := bufio.NewReaderSize(io.LimitReader(f, info.Size()-next.off), 64<<10)
	for next.off < info.Size() {
		b, e := rd.ReadBytes('\n')
		if e != nil {
			break
		} // a partial final line is read again after it completes
		next.off += int64(len(b))
		sum.Write(b)
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if tail == nil {
			tail = &rowChunk{}
		}
		tail.add(Row{Record: r}, "", next.off, false)
		if !r.IsRejected() && (next.first.IsZero() || r.Time.Before(next.first)) {
			next.first = r.Time
		}
		if r.ProviderKeyID != "" {
			next.keyProviders[r.Provider] = true
		}
		if len(tail.Rows) == logBlockRows {
			next.blocks = append(next.blocks, tail.freeze())
			tail = nil
		}
	}
	if tail != nil {
		next.blocks = append(next.blocks, tail.freeze())
	}
	next.hash, next.digest = fmt.Sprintf("%x", sum.Sum(nil)), marshalLogHash(sum)
	// A rewrite during the read must not give old blocks the new content's hash.
	if (!trusted || !settled) && next.hash != logRecordHash(path, next.off) {
		next.hash, next.digest = "", nil
		next.settled = false
	}
	logAppends.Lock()
	// Preserve writes which arrived while this immutable snapshot was built.
	if logAppends.path == path && (sameLogInfo(logAppends.last, info) || sameLogInfo(logAppends.base, info) || notePath == path && sameLogInfo(noteLast, info) && sameLogInfo(noteBase, logAppends.base)) {
		logAppends.base = info
	} else if logAppends.last == nil {
		logAppends.path, logAppends.base, logAppends.last = path, info, info
	}
	logAppends.Unlock()
	for _, c := range next.blocks {
		next.bytes += c.Bytes
	}
	if next.bytes > requestCacheBytes {
		next.uncached = true
		kept := *next
		kept.blocks = nil
		logIndex.snapshot = &kept
	} else {
		logIndex.snapshot = next
	}
	return next
}

func (s *logSnapshot) visit(since time.Time, fn func(Record)) {
	for _, packed := range s.blocks {
		if !since.IsZero() && packed.Latest.Before(since) {
			continue
		}
		c := packed.unpack()
		if c == nil {
			continue
		}
		for i, p := range c.Rows {
			if !p.Time.Before(since) {
				fn(c.row(i).Record)
			}
		}
	}
}

func (c *rowChunk) writable() *rowChunk {
	d := c.unpack()
	if d == nil {
		return &rowChunk{}
	}
	next := *d
	next.Rows, next.Strings = slices.Clone(d.Rows), slices.Clone(d.Strings)
	next.dict = make(map[string]uint32, len(next.Strings))
	for i, s := range next.Strings {
		next.dict[s] = uint32(i)
	}
	next.Bytes += int64(32 * len(next.Strings))
	return &next
}

func (c *rowChunk) freeze() *rowChunk {
	if c.dict != nil {
		c.dict = nil
		c.Bytes -= int64(32 * len(c.Strings))
	}
	return c.pack()
}

func resumeLogHash(h hash.Hash, state []byte) bool {
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok || state == nil || u.UnmarshalBinary(state) != nil {
		h.Reset()
		return false
	}
	return true
}

func marshalLogHash(h hash.Hash) []byte {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return nil
	}
	state, err := m.MarshalBinary()
	if err != nil {
		return nil
	}
	return state
}

func oldInfo(s *logSnapshot) os.FileInfo {
	if s == nil {
		return nil
	}
	return s.info
}

// A read in the change stamp's clock tick cannot prove that a later equal
// stamp still names the same bytes. Decide before reading, not at reuse time.
func settledLogStamp(info os.FileInfo, start time.Time) bool {
	c := logChangeTime(info)
	return !c.IsZero() && c.Before(start.Add(-logStampSettle))
}

func sameLogInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && sameLogFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && logChangeStamp(a) == logChangeStamp(b)
}

// Append records the exact file states around its successful write. Contiguous
// writes by this process need no historical prefix scan; external edits still
// go through the fingerprint/rebuild path.
func noteLogAppend(path string, before, after os.FileInfo) {
	logAppends.Lock()
	defer logAppends.Unlock()
	if logAppends.path != path || !sameLogInfo(before, logAppends.last) {
		logAppends.path, logAppends.base = path, before
	}
	logAppends.last = after
}
