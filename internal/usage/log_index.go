package usage

// The JSONL file remains the ledger. This disposable index keeps immutable,
// compressed blocks of its metadata; appending copies at most the last block.
// Readers decode only blocks which can overlap their time window.
import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"time"
)

const logBlockRows = 512

type logSnapshot struct {
	path         string
	info         os.FileInfo
	off          int64
	hash         string
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

// File writes must never wait for a historical index rebuild.
var logAppends struct {
	sync.Mutex
	path       string
	base, last os.FileInfo
}

func readLogSnapshot() *logSnapshot {
	logIndex.Lock()
	defer logIndex.Unlock()
	path := Path()
	info, err := os.Stat(path)
	old := logIndex.snapshot
	if old != nil && old.path == path {
		if err != nil && old.info == nil {
			return old
		}
		if err == nil && old.info != nil && os.SameFile(old.info, info) && old.info.Size() == info.Size() && old.info.ModTime().Equal(info.ModTime()) {
			return old
		}
	}
	logIndex.version++
	next := &logSnapshot{path: path, info: info, version: logIndex.version, keyProviders: map[string]bool{}}
	if err != nil {
		logIndex.snapshot = next
		return next
	}
	logAppends.Lock()
	notePath, noteBase, noteLast := logAppends.path, logAppends.base, logAppends.last
	logAppends.Unlock()
	trusted := notePath == path && sameLogInfo(oldInfo(old), noteBase) && sameLogInfo(info, noteLast)
	continued := old != nil && old.path == path && old.info != nil && os.SameFile(old.info, info) && info.Size() > old.info.Size() && (trusted || old.hash != "" && recordHash(path, old.info.Size()) == old.hash)
	if continued {
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
	if !trusted {
		next.hash = recordHash(path, info.Size())
	}
	logAppends.Lock()
	// Preserve writes which arrived while this immutable snapshot was built.
	if logAppends.path == path && (sameLogInfo(logAppends.last, info) || sameLogInfo(logAppends.base, info) || notePath == path && sameLogInfo(noteLast, info) && sameLogInfo(noteBase, logAppends.base)) {
		logAppends.base = info
	} else if logAppends.last == nil {
		logAppends.path, logAppends.base, logAppends.last = path, info, info
	}
	logAppends.Unlock()
	logIndex.snapshot = next
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

func oldInfo(s *logSnapshot) os.FileInfo {
	if s == nil {
		return nil
	}
	return s.info
}
func sameLogInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
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
