package sessions

// Request metadata uses checksummed append-only frames. A growing session saves
// only new/changed calls and its parser continuation. Corrupt shards rebuild
// from source. Sessions summaries never load these files.

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// v3 persists Codex cumulative counters explicitly; v2 shards can contain
// inflated deltas after a reload, so rebuild their derived rows from source.
// v4: a Codex call's input no longer holds what it wrote to the cache (#589).
// v5: an OpenCode call keeps the effort its prompt asked for (#680).
// v6: Codex response records and compact disk-only parser state.
// v7: reconcile recent Claude message revisions.
// v8: a Claude Code call's 1-hour cache writes.
// v9: a dsh call carries the provider it named and the model that answered,
// and a call through magpie's own gateway is left out of dsh's (the gateway
// logs it itself).
// v10: the calls ZCode's and WorkBuddy's session files hold are read too, and
// WorkBuddy's that went through the gateway are left out of them as a dsh
// one is.
// v12: a ZCode call's input leaves out the cache, and its output the
// reasoning counted twice, as its session's summary does (#1947).
// v13: a call after a compaction carries how long the compaction took
// (Compact): Claude Code's compact_boundary durationMs, Codex's compaction
// call's own time.
// v14: Codex calls keep the thread that made the request.
// v15: Codex calls keep the compaction marker independently of its duration.
// What each version names is what a cache of the one before it cannot serve:
// the version is part of the directory's name, so a cache written by an older
// one is never read (pruneCalls). It is raised for a change to every agent's
// calls, or to the shards' form; a change to one agent's calls raises that
// agent's callParseVersions instead.
const callCacheVersion = "calls-v15"

// callParseVersions is each agent's calls' version, for a change to how one
// agent's calls are read: its shards written before are read again from
// their sessions, and every other agent's are kept. Raising callCacheVersion
// for one agent read every agent's history again, every call shard of a
// long history (~16,000 files, minutes of CPU) after an update. An agent
// not named here is at 0, as every shard written before this was.
//
// cindy 1: a call through magpie's link (provider "magpie-<8 hex>") is
// the gateway's, and left out (cindyGatewayProvider).
// codex 1, zcode 1: a fork's copies of its parent's calls are the
// parent's (codex_fork.go, zcDB.copied).
// codex 2, zcode 2: only the copies are; a fork's own calls are its own
// (zcode 1 left out every call of a session holding one copy).
// antigravity 1: a Gemini deployment's name with any one letter after it
// (gemini-4-argon-d) is its model's (GeminiDeployment).
// zcode 3: a call names its session as a request does, without the sess_
// prefix (zcodeBareID), so the gateway's record of it pairs with it.
var callParseVersions = map[string]int{"antigravity": 1, "cindy": 1, "codex": 3, "zcode": 3}

// CallCacheVersion is callCacheVersion and callParseVersions, for a cache
// of what is made of the calls (usage's requests-v1.bin and heatmap): a
// raised agent's calls change what is made of them.
func CallCacheVersion() string {
	v := callCacheVersion
	for _, agent := range slices.Sorted(maps.Keys(callParseVersions)) {
		if n := callParseVersions[agent]; n != 0 {
			v += fmt.Sprintf(",%s=%d", agent, n)
		}
	}
	return v
}

const maxKeptCalls = 131072
const maxKeptFiles = 64

var callsMu sync.Mutex
var callIO [64]sync.Mutex
var callGeneration uint64
var callCache = map[string]*callFile{}
var callCounts = map[string]int{} // small allocation hints, never call records
var callOrder []string
var callRoot string

// callSwept is when pruneCalls last looked for shards no session file
// names any more. Every Requests page lists the sources, and reading a
// folder of ~16k shards and hashing every path each time was a tenth of
// a warm page (#3493). The shard of a file read here goes as soon as the
// file does; any other is left for a minute at most.
var callSwept time.Time

const callSweepEvery = time.Minute

func callCacheDir() string             { return filepath.Join(filepath.Dir(CachePath()), callCacheVersion) }
func callCachePath(path string) string { return filepath.Join(callCacheDir(), hashHead(path)+".gob") }

func resetCalls() {
	callsMu.Lock()
	defer callsMu.Unlock()
	callCache, callOrder = map[string]*callFile{}, nil
	callContinuations, callContinuationOrder = map[string]callContinuation{}, nil
	callCounts = map[string]int{}
	callRoot = ""
	callSwept = time.Time{}
	callGeneration++
	resetOCCalls()
	// ZCode's listing and WorkBuddy's gateway ids name another HOME's files
	// and models.json: a read after a Reset() would otherwise hand back
	// sources that are not here. Both are stamped, so the stale stamp alone
	// would miss; clearing them makes a new HOME read its own
	zcCallList.Lock()
	zcCallList.stamp, zcCallList.files = "", nil
	zcCallList.Unlock()
	wbGateway.Lock()
	wbGateway.byPath = nil
	wbGateway.Unlock()
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
	on := pathSet()
	defer putPathSet(on)
	for _, f := range files {
		on[f.path] = struct{}{}
	}
	order := callOrder[:0]
	for _, path := range callOrder {
		if _, ok := on[path]; ok {
			order = append(order, path)
		} else {
			delete(callCache, path)
		}
	}
	callOrder = order
	gone := false
	for path := range callCounts {
		if _, ok := on[path]; !ok {
			delete(callCounts, path)
			gone = true
		}
	}
	order = callContinuationOrder[:0]
	for _, path := range callContinuationOrder {
		if _, ok := on[path]; ok {
			order = append(order, path)
		} else {
			delete(callContinuations, path)
		}
	}
	callContinuationOrder = order

	trimCallRevisions(time.Now())
	// a session file read here and gone since takes its shard with it now
	sweep := moved || gone || time.Since(callSwept) >= callSweepEvery
	if sweep {
		callSwept = time.Now()
	}
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
	if !sweep {
		return
	}
	// IO never holds the metadata lock. A grace period protects live writers.
	shards := make(map[string]bool, len(files))
	for _, f := range files {
		shards[hashHead(f.path)+".gob"] = true
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		orphan := strings.HasSuffix(e.Name(), ".gob") && !shards[e.Name()] ||
			strings.HasSuffix(e.Name(), ".rev") && !shards[strings.TrimSuffix(e.Name(), ".rev")+".gob"]
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
	if callRoot == "" {
		// what is kept is of this root: a Sessions parse that read a
		// rollout's calls along (callsRide) keeps them before any Requests
		// read named the root, and pruneCalls then took the root for a
		// moved one and dropped them, Codex window and all. The shard on
		// disk has no window, so the next line read the rollout whole
		// again (#4369: 10.8 MB for one line after a start).
		callRoot = callCacheDir()
	}
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

// wholeCallReaders read an agent's calls from one session file at a time,
// rather than a line at a time: its calls are not lines, or its file cannot
// be read on from the middle of one. Every agent in callSources that is not
// in callFiles needs an entry here, in lineCallReaders or in
// resumeCallReaders, or its calls are never read.
var wholeCallReaders = map[string]func(file) (*callFile, bool){
	"opencode":     readOpenCodeCalls,
	"zcode":        readZCodeCalls,
	"workbuddy":    readWorkBuddyCalls,
	"workbuddy-ai": readWorkBuddyCalls,
	"codebuddy":    readWorkBuddyCalls, // #1779
	// #1605
	"minimax-code": readMiniMaxCalls,
	"agy":          readAgyCalls,         // #2097
	"antigravity":  readAntigravityCalls, // #2190
}

// resumeCallReaders read an agent's calls from a file only appended to that
// the line reader can't read on through, as dsh's packed file of zstd
// frames: each is handed what the last read came to and reads on from
// where it left off, or whole when it can't. It gives how many of the
// calls it hands back were in old, kept as they were (0 when it read
// whole, or what it had counted no longer stands), so only the rest are
// appended to the disk shard.
var resumeCallReaders = map[string]func(f file, old *callFile) (st *callFile, kept int, ok bool){
	"dsh": readDshCalls,
}

// lineCallReader reads an agent's calls a line at a time, as Claude Code's
// and Codex's are: a file that only grew is read on from where the last read
// left off (prepareCalls), and only the calls it adds are written to the
// disk shard. begin starts a file read from its start; line reads one whole
// line of it. Nothing a line leaves for the next may live outside st. kept,
// when a reader carries something from line to line, says whether st holds
// it: a read kept on disk before it was carried there does not, and is
// read whole again.
type lineCallReader struct {
	begin func(st *callFile, f file)
	line  func(st *callFile, b []byte)
	kept  func(st *callFile) bool
}

// lineCallReaders are the agents in callSources, beside callFiles' own,
// whose calls are each one line of a file only appended to.
var lineCallReaders = map[string]lineCallReader{
	"kimi":  {kimiCallsBegin, kimiCallLine, nil},         // #1607
	"pi":    {piCallsBegin, readPiCallLine, piCallsKept}, // #1951
	"raft":  {piCallsBegin, readPiCallLine, piCallsKept},
	"cindy": {piCallsBegin, readPiCallLine, piCallsKept},
	"omp":   {piCallsBegin, readPiCallLine, piCallsKept},
	"grok":  {grokCallsBegin, grokCallLine, grokCallsKept}, // #4423
}

// callLock is the lock a file's calls are read under, so that two readers
// never read the same file at once.
func callLock(path string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return &callIO[h.Sum32()%uint32(len(callIO))]
}

func readCalls(f file) *callFile {
	lock := callLock(f.path)
	lock.Lock()
	defer lock.Unlock()
	callsMu.Lock()
	old, generation := callCache[f.path], callGeneration
	callsMu.Unlock()
	owned := false
	if old == nil {
		old = loadKept(f)
		owned = true
	}
	if old != nil && old.Size == f.size && old.Mod == f.mod.UnixNano() {
		if owned {
			// a Claude session's calls a parse read along (callsRide) left
			// their window on disk, not here
			old = withCallRevisions(old)
		}
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
	// a file to read: few at once while only the warm-up asks (quiet.go)
	defer readSlot()()
	defer readBegin()()
	// Agents whose calls are read whole rather than line by line: a session
	// that changed is read again, and one that did not is kept. OpenCode's
	// and ZCode's are rows of a database or JSON documents; dsh's file is
	// packed in frames, so it cannot be read on from the middle of one.
	if read, ok := resumeCallReaders[f.agent]; ok {
		st, kept, readOK := read(f, old)
		if !readOK && old != nil {
			return old
		}
		if readOK {
			st.Size, st.Mod = f.size, f.mod.UnixNano()
			writeCalls(st, kept, kept > 0)
		}
		callsMu.Lock()
		if readOK && generation == callGeneration {
			keepCalls(f.path, st)
		}
		callsMu.Unlock()
		return st
	}
	if read, ok := wholeCallReaders[f.agent]; ok {
		st, readOK := read(f)
		if !readOK && old != nil {
			return old // not readable now: what was read before
		}
		if readOK {
			st.Size, st.Mod = f.size, f.mod.UnixNano()
			writeCalls(st, 0, false)
		}
		callsMu.Lock()
		if readOK && generation == callGeneration {
			keepCalls(f.path, st)
		}
		callsMu.Unlock()
		return st
	}
	if r, ok := lineCallReaders[f.agent]; ok && r.kept != nil && old != nil && !r.kept(old) {
		old = nil
	}
	st := prepareCalls(f, withCallRevisions(old), owned)
	continued := old != nil && st.Off == old.Off && st.Off > 0
	start := 0
	if continued {
		start = len(old.Calls)
	}
	line, thin := claudeCallLine, thinClaude
	if f.agent == "codex" {
		line, thin = codexCallLine, nil
	} else if r, ok := lineCallReaders[f.agent]; ok {
		if st.Off == 0 {
			r.begin(st, f)
		}
		line, thin = r.line, nil
	}
	off, err := scanThin(f.path, st.Off, func(b []byte) bool { return callHead(st, b) }, thin, func(b []byte, start, end int64) bool {
		st.At, st.End = start, end
		line(st, b)
		return true
	})
	if err == nil {
		st.Off, st.Size, st.Mod = off, f.size, f.mod.UnixNano()
		// the bytes read, not the listed size: a listing may fold another
		// file's size into this one's (Kimi's state.json), and hashing past
		// the end would leave no hash to read on from
		st.ContentHash = prefixHash(f.path, off)
		writeCalls(st, start, continued)
	}
	// Do not turn a one-time historical scan into an indefinitely retained
	// window. Previously retained windows may survive idle time in spare slots.
	if st.Claude != nil && (old == nil || old.Claude == nil) && !workingRevision(f.mod.UnixNano(), time.Now()) {
		st = st.withoutRevisions()
	}
	if err == nil && (f.agent == "claude" || f.agent == "claude-desktop") {
		writeCallRevisions(st)
	}

	st.dirty = nil
	callsMu.Lock()
	if generation == callGeneration {
		keepCalls(f.path, st)
	}
	callsMu.Unlock()
	return st
}

// callsRide reads a packed Codex rollout's calls on the scan that parses its
// summary. A packed file is decompressed whole for every read, and when the
// Codex app packs or repacks every rollout (11,591 on 10-20) the start's
// warm-up parsed each for the Sessions page and then read each again for
// Requests' calls: ~48 GB decompressed twice. Reading both on one pass
// leaves readCalls a shard on disk that is already up to date. A Claude
// session read from its start rides too: a first start scanned each again
// for its calls, ~20 s of its CPU on 13 GB of them (#2278).
type callsRide struct {
	f          file
	st         *callFile
	generation uint64
	lock       *sync.Mutex
	sum, calls bool // what each reader's head said of the line being read
	claude     bool
}

// callsAlong is a ride for f's calls when f is a Codex rollout whose calls
// are to be read again, or a Claude session whose calls were never read,
// and nil otherwise. A plain rollout rides only when both reads start at
// its first line (parseWith lets the ride go otherwise): read alone, a
// cold start read every rollout twice, 3.5 GB for 1.7 GB of them (#4369).
// sum is the summary the parse goes on from: a plain rollout's with its
// Codex window is read on, so its calls are left to readCalls unlooked.
// It holds the file's calls lock until release, so a readCalls of the
// file meanwhile waits for what the ride reads rather than read it again.
func callsAlong(f file, sum *state) *callsRide {
	claude := f.agent == "claude" || f.agent == "claude-desktop"
	onward := !packed(f.path) && sum != nil && sum.Codex != nil
	if f.agent != "codex" && !claude || f.agent == "codex" && onward || f.cold || wslCold(f.path) {
		return nil
	}
	lock := callLock(f.path)
	lock.Lock()
	callsMu.Lock()
	old, generation := callCache[f.path], callGeneration
	callsMu.Unlock()
	if claude {
		// calls read before are read on, or again, by readCalls as they were
		if _, err := os.Stat(callCachePath(f.path)); old != nil || !errors.Is(err, fs.ErrNotExist) {
			lock.Unlock()
			return nil
		}
		return &callsRide{f: f, st: prepareCalls(f, nil, false), generation: generation, lock: lock, claude: true}
	}
	owned := false
	if old == nil {
		old = loadKept(f)
		owned = true
	}
	if old != nil && old.Size == f.size && old.Mod == f.mod.UnixNano() {
		lock.Unlock()
		return nil
	}
	return &callsRide{f: f, st: prepareCalls(f, old, owned), generation: generation, lock: lock}
}

// ride is a scan's head and line for both readers: each head sees every
// line, as on a scan of its own, and each line goes to the readers whose
// head wanted it.
func (r *callsRide) ride(head func([]byte) bool, line func([]byte, int64, int64) bool) (func([]byte) bool, func([]byte, int64, int64) bool) {
	return func(b []byte) bool {
			r.sum = head == nil || head(b)
			r.calls = callHead(r.st, b)
			return r.sum || r.calls
		}, func(b []byte, start, end int64) bool {
			if r.sum {
				line(b, start, end)
			}
			if r.calls {
				r.st.At, r.st.End = start, end
				if r.claude {
					claudeCallLine(r.st, b)
				} else {
					codexCallLine(r.st, b)
				}
			}
			return true
		}
}

// release lets the file's calls lock go.
func (r *callsRide) release() { r.lock.Unlock() }

// finish keeps the calls read, as readCalls would have, when the scan
// read to its end; after a failed one readCalls reads them itself.
func (r *callsRide) finish(off int64, err error) {
	if err != nil {
		return
	}
	st, f := r.st, r.f
	st.Off, st.Size, st.Mod = off, f.size, f.mod.UnixNano()
	st.ContentHash = prefixHash(f.path, off)
	writeCalls(st, 0, false)
	if r.claude {
		if st.Claude != nil && !workingRevision(f.mod.UnixNano(), time.Now()) {
			st = st.withoutRevisions()
		}
		writeCallRevisions(st)
	}
	st.dirty = nil
	if r.claude {
		// on disk only: a Sessions read keeps no calls (readCalls loads them)
		return
	}
	callsMu.Lock()
	if r.generation == callGeneration {
		keepCalls(f.path, st)
	}
	callsMu.Unlock()
}

// Common strings are stored once per append frame; File is implied by State.
type callPatch struct {
	Index int
	Call  Call
	Text  [13]uint32
	Began time.Time
}
type callFrame struct {
	State   callDiskState
	Strings []string
	Patches []callPatch
	// More: the frames after it, up to one without More, are one write's
	// (framePatches); a file ending in one was cut off mid-write
	More bool
	// Calls is how many calls the file holds after this write's frames,
	// to make room for them all at the first
	Calls int
}

// framePatches is the most patches one frame holds. A write was one frame
// whatever its size, and a frame is decoded whole: a Codex session of 52k
// calls was one 8 MB frame, and reading it allocated ~100 MB and held ~70 MB
// at once (24 MB of patches beside the calls made of them), the largest
// part of the peak a start after an update reached in rebuilding the
// Requests cache (#3493). A write is cut into frames of this many, read one
// after another into the same buffers.
const framePatches = 2048

// Disk state contains only scalar continuation metadata. Even a nil runtime
// index makes gob emit its recursive type descriptors in every append frame.
// Keep those types entirely outside the wire schema.
type callDiskState struct {
	ContentHash        string
	Size, Mod, Off     int64
	Head               string
	HeadSize           int
	Path, Agent        string
	Parser             int // callParseVersions[Agent] when written
	At, End, AsstEnd   int64
	Session, Requested string
	LastUser, LastAsst time.Time
	// dsh's carry (dshCarry), as JSON: nil when there is none
	Dsh []byte
	// Pi's and omp's carry (piCarry), flat: PiOn when there is one
	PiOn, PiHeader      bool
	PiCwd               string
	PiSince             time.Time
	PiProvider, PiModel string
	// Grok Build's carry (grokCallCarry), flat: GrokOn when there is one
	GrokOn    bool
	GrokSince time.Time
	GrokModel string
	GrokAsked string
}

func diskCallState(s *callFile) callDiskState {
	d := callDiskState{ContentHash: s.ContentHash, Size: s.Size, Mod: s.Mod, Off: s.Off, Head: s.Head, HeadSize: s.HeadSize,
		Path: s.Path, Agent: s.Agent, Parser: callParseVersions[s.Agent], At: s.At, End: s.End, AsstEnd: s.AsstEnd, Session: s.Session, Requested: s.Requested,
		LastUser: s.LastUser, LastAsst: s.LastAsst}
	if s.Dsh != nil {
		d.Dsh, _ = json.Marshal(s.Dsh)
	}
	if p := s.Pi; p != nil {
		d.PiOn, d.PiHeader, d.PiCwd, d.PiSince, d.PiProvider, d.PiModel = true, p.Header, p.Cwd, p.Since, p.Provider, p.Model
	}
	if g := s.Grok; g != nil {
		d.GrokOn, d.GrokSince, d.GrokModel, d.GrokAsked = true, g.Since, g.Model, g.Asked
	}
	return d
}
func (s callDiskState) restore() *callFile {
	st := &callFile{ContentHash: s.ContentHash, Size: s.Size, Mod: s.Mod, Off: s.Off,
		Head: s.Head, HeadSize: s.HeadSize, Path: s.Path, Agent: s.Agent, At: s.At, End: s.End,
		AsstEnd: s.AsstEnd, Session: s.Session, Requested: s.Requested, LastUser: s.LastUser, LastAsst: s.LastAsst}
	if len(s.Dsh) > 0 {
		var c dshCarry
		if json.Unmarshal(s.Dsh, &c) == nil {
			st.Dsh = &c
		}
	}
	if s.PiOn {
		st.Pi = &piCarry{s.PiHeader, s.PiCwd, s.PiSince, s.PiProvider, s.PiModel}
	}
	if s.GrokOn {
		st.Grok = &grokCallCarry{Since: s.GrokSince, Model: s.GrokModel, Asked: s.GrokAsked}
	}
	return st
}

func callText(c *Call) [13]*string {
	return [13]*string{&c.Agent, &c.Session, &c.ThreadID, &c.Model, &c.Requested, &c.Effort, &c.Cwd, &c.Upstream, &c.AccountID, &c.UserID, &c.Error, &c.ErrorText, &c.File}
}

// frameBuffer is what loadCalls reads a shard's frames into, kept between
// shards: a cold Requests build reads every one of ~15.6k, and a new
// buffer, slice of patches and slice of strings for each frame came to
// ~400 MB of it (#3493). Nothing read out of a frame points into these:
// gob copies every string and byte slice out of its input, and a patch's
// call is copied into the file's calls.
type frameBuffer struct {
	data []byte
	f    callFrame
}

var frameBuffers = sync.Pool{New: func() any { return new(frameBuffer) }}

// frame is k's frame emptied for the next to be decoded into it. gob
// leaves out a field of the zero value and leaves the field it decodes
// into as it was, so a patch or string is cleared from the last frame,
// not only cut off: a reused slice is decoded into in place.
func (k *frameBuffer) frame() *callFrame {
	clear(k.f.Patches)
	clear(k.f.Strings)
	k.f = callFrame{Strings: k.f.Strings[:0], Patches: k.f.Patches[:0]}
	return &k.f
}

func loadCalls(f file) *callFile {
	in, err := os.Open(callCachePath(f.path))
	if err != nil {
		return nil
	}
	defer in.Close()
	k := frameBuffers.Get().(*frameBuffer)
	defer func() {
		k.frame() // the pool holds no file's strings
		frameBuffers.Put(k)
	}()
	var st *callFile
	var calls []Call
	var big, more bool
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
		data := k.data[:0]
		if cap(data) < int(size) {
			data = make([]byte, int(size))
		}
		data = data[:size]
		k.data = data
		if _, err = io.ReadFull(in, data); err != nil || crc32.ChecksumIEEE(data) != binary.LittleEndian.Uint32(header[4:]) {
			return nil
		}
		frame := k.frame()
		if decodeFrame(data, frame) != nil || frame.State.Path != f.path || frame.State.Agent != f.agent || frame.State.Parser != callParseVersions[f.agent] {
			return nil
		}
		if st != nil && frame.State.Off < st.Off {
			return nil
		}
		big = big || len(frame.Patches) > framePatches
		more = frame.More
		// room for the write's calls at once (its frames' Calls; an older
		// frame's patches, mostly new calls), rather than append's
		// doublings, which a warm start paid ~500 MB for across every
		// cached file
		calls = slices.Grow(calls, max(len(frame.Patches), frame.Calls-len(calls)))
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
	if st == nil || more {
		return nil
	}
	st.Calls, st.Msgs, st.Began = calls, msgs, began
	st.bigFrame = big
	return st
}

// loadKept is the calls kept on disk for f, under f's callLock. A file
// written as one frame larger than framePatches, before frames were cut,
// is written again in frames, once.
func loadKept(f file) *callFile {
	st := restoreCallContinuation(loadCalls(f))
	if st != nil && st.bigFrame {
		st.bigFrame = false
		writeCalls(st, 0, false)
	}
	return st
}

// framePatchBufs holds the patches writeCalls makes a frame of, for the
// next frame and the next file's write: each frame grew its own up to
// framePatches, ~1 GB of a first start's 21 GB history (#2278). A frame
// holds no more than framePatches, so a kept one is at most ~1 MB.
var framePatchBufs = sync.Pool{New: func() any { return new([]callPatch) }}

// frameBytes holds one frame's bytes as writeCalls writes it, for the
// next frame and the next write: a write was held whole before any of it
// went to the file, grown by doublings to its size (~120 MB of a first
// start's writes, and a 52k-call session held 8 MB at once), where one
// frame is at most ~1 MB.
var frameBytes = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func writeCalls(st *callFile, start int, appendOnly bool) {
	state := diskCallState(st)
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
	data := frameBytes.Get().(*bytes.Buffer)
	defer func() {
		if data.Cap() <= 4<<20 {
			data.Reset()
			frameBytes.Put(data)
		}
	}()
	var frame callFrame
	var dict map[string]uint32
	var header [8]byte
	ok := true
	// flush writes the frame to out, its header in front
	flush := func(more bool) {
		frame.More = more
		data.Reset()
		data.Write(header[:])
		if gob.NewEncoder(data).Encode(frame) != nil {
			ok = false
			return
		}
		b := data.Bytes()
		binary.LittleEndian.PutUint32(b, uint32(len(b)-len(header)))
		binary.LittleEndian.PutUint32(b[4:], crc32.ChecksumIEEE(b[len(header):]))
		if _, err := out.Write(b); err != nil {
			ok = false
		}
	}
	patches := framePatchBufs.Get().(*[]callPatch)
	defer func() {
		clear(frame.Patches[:cap(frame.Patches)]) // nothing of st is kept in the pool
		*patches = frame.Patches[:0]
		framePatchBufs.Put(patches)
	}()
	// room for the first frame's patches at once: the pool is empty after
	// a collection, and append's doublings to framePatches were ~150 MB
	// of a first start's writes
	n := len(st.Calls) - start
	for i := 0; i < start; i++ {
		if st.dirty[i] {
			n++
		}
	}
	frame.Patches = slices.Grow((*patches)[:0], min(n, framePatches))
	begin := func() {
		frame = callFrame{State: state, Strings: []string{""}, Calls: len(st.Calls), Patches: frame.Patches[:0]}
		dict = map[string]uint32{"": 0}
	}
	begin()
	add := func(i int) {
		if !ok {
			return
		}
		if len(frame.Patches) == framePatches {
			if flush(true); !ok {
				return
			}
			begin()
		}
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
	if ok {
		flush(false)
	}
	if closeErr := out.Close(); ok && closeErr == nil && !appendOnly {
		_ = os.Rename(out.Name(), callCachePath(st.Path))
	}
}
