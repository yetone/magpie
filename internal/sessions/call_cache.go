package sessions

// Request metadata uses checksummed append-only frames. A growing session saves
// only new/changed calls and its parser continuation. Corrupt shards rebuild
// from source. Sessions summaries never load these files.

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"hash/crc32"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// v3 persists Codex cumulative counters explicitly; v2 shards can contain
// inflated deltas after a reload, so rebuild their derived rows from source.
// v4: a Codex call's input no longer holds what it wrote to the cache (#589).
// v5: an OpenCode call keeps the effort its prompt asked for (#680).
// v6: Codex response records and compact disk-only parser state.
const callCacheVersion = "calls-v6"
const maxKeptCalls = 131072
const maxKeptFiles = 64

var callsMu sync.Mutex
var callIO [64]sync.Mutex
var callGeneration uint64
var callCache = map[string]*callFile{}
var callCounts = map[string]int{} // small allocation hints, never call records
var callOrder []string
var callRoot string

func callCacheDir() string             { return filepath.Join(filepath.Dir(CachePath()), callCacheVersion) }
func callCachePath(path string) string { return filepath.Join(callCacheDir(), hashHead(path)+".gob") }

func resetCalls() {
	callsMu.Lock()
	defer callsMu.Unlock()
	callCache, callOrder = map[string]*callFile{}, nil
	callContinuations, callContinuationOrder = map[string]callContinuation{}, nil
	callCounts = map[string]int{}
	callRoot = ""
	callGeneration++
	resetOCCalls()
}

func pruneCalls(files []file) {
	root := callCacheDir()
	callsMu.Lock()
	moved := callRoot != root
	if moved {
		callGeneration++
		callCache, callOrder, callRoot = map[string]*callFile{}, nil, root
		callCounts = map[string]int{}
	}
	on := make(map[string]bool, len(files))
	shards := make(map[string]bool, len(files))
	for _, f := range files {
		on[f.path] = true
		shards[hashHead(f.path)+".gob"] = true
	}
	order := callOrder[:0]
	for _, path := range callOrder {
		if on[path] {
			order = append(order, path)
		} else {
			delete(callCache, path)
		}
	}
	callOrder = order
	for path := range callCounts {
		if !on[path] {
			delete(callCounts, path)
		}
	}
	trimCallRevisions(time.Now())
	callsMu.Unlock()
	if moved {
		// an older version's shards are never read again
		olds, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "calls-v*"))
		for _, old := range olds {
			if old != root {
				_ = os.RemoveAll(old)
			}
		}
	}
	// IO never holds the metadata lock. A grace period protects live writers.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		orphan := strings.HasSuffix(e.Name(), ".gob") && !shards[e.Name()]
		if strings.HasPrefix(e.Name(), "calls-") && strings.HasSuffix(e.Name(), ".tmp") {
			if st, err := e.Info(); err == nil && time.Since(st.ModTime()) > time.Hour {
				orphan = true
			}
		}
		if orphan {
			_ = os.Remove(filepath.Join(root, e.Name()))
		}
	}
}

func keepCalls(path string, st *callFile) {
	defer trimCallRevisions(time.Now())
	callCounts[path] = len(st.Calls)
	delete(callCache, path)
	order := callOrder[:0]
	for _, p := range callOrder {
		if p != path {
			order = append(order, p)
		}
	}
	callOrder = order
	if len(st.Calls) > maxKeptCalls {
		return
	}
	n := len(st.Calls)
	for _, s := range callCache {
		n += len(s.Calls)
	}
	for len(callOrder) > 0 && (n > maxKeptCalls || len(callOrder) >= maxKeptFiles) {
		p := callOrder[0]
		n -= len(callCache[p].Calls)
		delete(callCache, p)
		callOrder = callOrder[1:]
	}
	callCache[path] = st
	callOrder = append(callOrder, path)
}

func readCalls(f file) *callFile {
	h := fnv.New32a()
	_, _ = h.Write([]byte(f.path))
	lock := &callIO[h.Sum32()%uint32(len(callIO))]
	lock.Lock()
	defer lock.Unlock()
	callsMu.Lock()
	old, generation := callCache[f.path], callGeneration
	callsMu.Unlock()
	if old == nil {
		old = restoreCallContinuation(loadCalls(f))
	}
	if old != nil && old.Size == f.size && old.Mod == f.mod.UnixNano() {
		callsMu.Lock()
		if generation == callGeneration {
			keepCalls(f.path, old)
		}
		callsMu.Unlock()
		return old
	}
	if f.cold || wslCold(f.path) {
		// in a stopped WSL distro: opening it would start it
		if old != nil {
			return old
		}
		return &callFile{Path: f.path, Agent: f.agent}
	}
	if f.agent == "opencode" {
		// rows, not lines: a session that changed is read again whole
		st, ok := readOpenCodeCalls(f)
		if !ok && old != nil {
			return old // not readable now: what was read before
		}
		if ok {
			st.Size, st.Mod = f.size, f.mod.UnixNano()
			writeCalls(st, 0, false)
		}
		callsMu.Lock()
		if ok && generation == callGeneration {
			keepCalls(f.path, st)
		}
		callsMu.Unlock()
		return st
	}
	st := prepareCalls(f, old)
	continued := old != nil && st.Off == old.Off && st.Off > 0
	start := 0
	if continued {
		start = len(old.Calls)
	}
	off, err := scanAt(f.path, st.Off, func(b []byte) bool { return callHead(st, b) }, func(b []byte, start, end int64) bool {
		st.At, st.End = start, end
		if f.agent == "codex" {
			codexCallLine(st, b)
		} else {
			claudeCallLine(st, b)
		}
		return true
	})
	if err == nil {
		st.Off, st.Size, st.Mod = off, f.size, f.mod.UnixNano()
		st.ContentHash = prefixHash(f.path, f.size)
		writeCalls(st, start, continued)
	}
	st.dirty = nil
	callsMu.Lock()
	if generation == callGeneration {
		keepCalls(f.path, st)
	}
	callsMu.Unlock()
	return st
}

// Common strings are stored once per append frame; File is implied by State.
type callPatch struct {
	Index int
	Call  Call
	Text  [12]uint32
	Began time.Time
}
type callFrame struct {
	State   callDiskState
	Strings []string
	Patches []callPatch
}

// Disk state contains only scalar continuation metadata. Even a nil runtime
// index makes gob emit its recursive type descriptors in every append frame.
// Keep those types entirely outside the wire schema.
type callDiskState struct {
	ContentHash        string
	Size, Mod, Off     int64
	Head               string
	HeadSize           int
	Path, Agent        string
	At, End, AsstEnd   int64
	Session, Requested string
	LastUser, LastAsst time.Time
}

func diskCallState(s *callFile) callDiskState {
	return callDiskState{s.ContentHash, s.Size, s.Mod, s.Off, s.Head, s.HeadSize,
		s.Path, s.Agent, s.At, s.End, s.AsstEnd, s.Session, s.Requested, s.LastUser, s.LastAsst}
}
func (s callDiskState) restore() *callFile {
	return &callFile{ContentHash: s.ContentHash, Size: s.Size, Mod: s.Mod, Off: s.Off,
		Head: s.Head, HeadSize: s.HeadSize, Path: s.Path, Agent: s.Agent, At: s.At, End: s.End,
		AsstEnd: s.AsstEnd, Session: s.Session, Requested: s.Requested, LastUser: s.LastUser, LastAsst: s.LastAsst}
}

func callText(c *Call) [12]*string {
	return [12]*string{&c.Agent, &c.Session, &c.Model, &c.Requested, &c.Effort, &c.Cwd, &c.Upstream, &c.AccountID, &c.UserID, &c.Error, &c.ErrorText, &c.File}
}

func loadCalls(f file) *callFile {
	in, err := os.Open(callCachePath(f.path))
	if err != nil {
		return nil
	}
	defer in.Close()
	var st *callFile
	var calls []Call
	msgs := map[string]int{}
	began := map[string]time.Time{}
	for {
		var header [8]byte
		_, err = io.ReadFull(in, header[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		size := binary.LittleEndian.Uint32(header[:4])
		if size == 0 || size > 256<<20 {
			return nil
		}
		data := make([]byte, int(size))
		if _, err = io.ReadFull(in, data); err != nil || crc32.ChecksumIEEE(data) != binary.LittleEndian.Uint32(header[4:]) {
			return nil
		}
		var frame callFrame
		if gob.NewDecoder(bytes.NewReader(data)).Decode(&frame) != nil || frame.State.Path != f.path || frame.State.Agent != f.agent {
			return nil
		}
		if st != nil && frame.State.Off < st.Off {
			return nil
		}
		for _, p := range frame.Patches {
			if p.Index < 0 || p.Index > len(calls) {
				return nil
			}
			for i, field := range callText(&p.Call) {
				if int(p.Text[i]) >= len(frame.Strings) {
					return nil
				}
				*field = frame.Strings[p.Text[i]]
			}
			p.Call.File = f.path
			if p.Index == len(calls) {
				calls = append(calls, p.Call)
			} else {
				calls[p.Index] = p.Call
			}
			if p.Call.Msg != "" {
				msgs[p.Call.Msg] = p.Index
				if !p.Began.IsZero() {
					began[p.Call.Msg] = p.Began
				}
			}
		}
		st = frame.State.restore()
	}
	if st == nil {
		return nil
	}
	st.Calls, st.Msgs, st.Began = calls, msgs, began
	return st
}

func writeCalls(st *callFile, start int, appendOnly bool) {
	frame := callFrame{State: diskCallState(st), Strings: []string{""}}
	dict := map[string]uint32{"": 0}
	add := func(i int) {
		p := callPatch{Index: i, Call: st.Calls[i], Began: st.Began[st.Calls[i].Msg]}
		p.Call.File = ""
		for j, field := range callText(&p.Call) {
			id, ok := dict[*field]
			if !ok {
				id = uint32(len(frame.Strings))
				dict[*field] = id
				frame.Strings = append(frame.Strings, *field)
			}
			p.Text[j] = id
			*field = ""
		}
		frame.Patches = append(frame.Patches, p)
	}
	for i := 0; i < start; i++ {
		if st.dirty[i] {
			add(i)
		}
	}
	for i := start; i < len(st.Calls); i++ {
		add(i)
	}
	var data bytes.Buffer
	if gob.NewEncoder(&data).Encode(frame) != nil {
		return
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(data.Len()))
	binary.LittleEndian.PutUint32(header[4:], crc32.ChecksumIEEE(data.Bytes()))
	dir := callCacheDir()
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	var out *os.File
	var err error
	if appendOnly {
		out, err = os.OpenFile(callCachePath(st.Path), os.O_WRONLY|os.O_APPEND, 0600)
	} else {
		out, err = os.CreateTemp(dir, "calls-*.tmp")
	}
	if err != nil {
		return
	}
	if !appendOnly {
		defer os.Remove(out.Name())
	}
	_, err = out.Write(header[:])
	if err == nil {
		_, err = out.Write(data.Bytes())
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && !appendOnly {
		_ = os.Rename(out.Name(), callCachePath(st.Path))
	}
}
