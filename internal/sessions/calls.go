package sessions

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/desktopdir"
)

// Call is one model call as an agent's own session file records it.
type Call struct {
	ResponseID string    `json:"response_id,omitempty"`
	Time       time.Time `json:"t"`
	Agent      string    `json:"agent"`   // claude (Claude Code's CLI), claude-desktop (Desktop's Code tab and Cowork), codex
	Session    string    `json:"session"` // the id the agent sends the gateway as its session header
	ThreadID   string    `json:"thread_id,omitempty"`
	Model      string    `json:"model"` // the model the file names: for Claude, the one the vendor answered
	// Requested: the model Claude Code was running as (its identity note) when it made
	// the call, as it was asked for: claude-opus-5[1m] for claude-opus-5 with the long context
	Requested string `json:"requested,omitempty"`
	Tokens
	Reasoning int    `json:"reasoning,omitempty"`  // Codex: inside Output already
	Effort    string `json:"effort,omitempty"`     // the thinking effort: Codex's, of the turn; Claude's, of the line
	RequestID string `json:"request_id,omitempty"` // Claude's
	// Codex's session metadata identifies its configured provider and creator.
	// These identify the historical session, not whichever account is signed in now.
	Upstream  string `json:"upstream,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Error     string `json:"error,omitempty"`      // Claude: the API error's kind (rate_limit, server_error …); such a call has no tokens
	ErrorText string `json:"error_text,omitempty"` // its message, the first 400 characters
	// Codex: the turn's own figures, set only when the turn made just this call
	TTFT int64 `json:"ttft_ms,omitempty"`
	// Compaction is Codex's explicit marker that this response compacted the context.
	Compaction bool `json:"compaction,omitempty"`
	// Millis: how long the call took. Codex's, of a turn that made this call alone, is what
	// it says; the rest are told from the file's times, the call's last line less the line
	// that asked for it (a prompt, a tool's result), and so a little more or less than it was
	Millis int64 `json:"ms,omitempty"`
	// Compact: how long a compaction of the context kept the agent waiting, in ms.
	// Codex's is on the compaction's own call (its Millis); Claude Code's is the
	// durationMs its compact_boundary line says, on the first call after it
	Compact int64  `json:"compact_ms,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	// Where the call is in its file, for reading what was said in it (ContentOf): the file,
	// from where what it was asked began — after the reply before it — to where it ended,
	// and for Claude Code the id of its message
	File string `json:"file,omitempty"`
	From int64  `json:"from,omitempty"`
	To   int64  `json:"to,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

// callFile is what one file's read has come to: the calls found in it, and
// what it takes to read on from off.
type callFile struct {
	Claude      *claudeUsageState
	ContentHash string
	dirty       map[int]bool
	// readSize is the size of the file being read (prepareCalls), for
	// growCalls
	readSize int64
	// bigFrame: read from a frame of more than framePatches (loadCalls)
	bigFrame bool
	Size     int64
	Mod      int64  // unix nanoseconds
	Off      int64  // after the last whole line read
	Head     string // hash of the prefix, to tell one replaced from one grown
	HeadSize int
	Path     string
	Calls    []Call
	Agent    string
	// where the line being handled begins and ends, and where the last reply's did
	At, End, AsstEnd int64
	Strs             map[string]string
	// Claude Code: the session by the file's name, and each message's place in
	// calls, as its blocks are lines of their own repeating its usage
	Session string
	Msgs    map[string]int
	// and the model it runs as, when the last user line and the last reply
	// line were, and where each message's asking began: a call took from there
	// to its last line
	Requested          string
	LastUser, LastAsst time.Time
	Began              map[string]time.Time
	// how long a compaction its file told of took, for the call after it
	CompactMs int64
	// Codex
	CX *cxRun
	// Pi and omp
	Pi *piCarry
	// dsh
	Dsh *dshCarry
	// Grok Build
	Grok *grokCallCarry
}

// cxRun is where a rollout's read stands: what its lines have said so far.
type cxRun struct {
	Meta                                        bool // session_meta seen
	Session, ThreadID, Cwd, Model, Effort, Turn string
	Upstream, AccountID, UserID                 string
	Usage                                       *codexUsageState
	Contexts                                    map[string]cxContext
	Turns                                       map[string]cxOpen
	LastIn, LastCall                            time.Time // the last thing the model was given, and the last call's end
	// Answering is set from a call's first output item until its usage line:
	// AskedAt is what the model had last been given when the call began to
	// answer, Answered its last output item. An older rollout writes a call's
	// usage after its own tool's output, so LastIn alone would start the call
	// at that output, and the usage line's time would end it after the tool ran.
	Answering         bool
	AskedAt, Answered time.Time
	AskedTurn         string
	LastEnd           int64 // where the last call's line ended
	Timings           []cxCallTiming
	LastIndex         int
}

// cxOpen is a turn still open: how many calls it has made, and where the first is.
type cxOpen struct {
	N, First             int
	Duration, FirstToken int64
	Complete             bool
}
type cxCallTiming struct {
	Input    time.Time
	Previous int
	End      time.Time // the call's last output item when its tool's output came before its usage line; else zero, and the line's time ends it
}

type cxContext struct {
	Model, Effort, Cwd, Upstream string
	LastIn                       time.Time
}

type cxCallUsage = cxUsage

// callDesktopDirs are Claude Desktop's data folders. Tests swap it.
var callDesktopDirs = desktopDataDirs

// DesktopDataDirs are the folders whose session metadata belongs to this index.
func DesktopDataDirs() []string { return desktopReadDirs() }

// desktopReadDirs are callDesktopDirs, or none when Claude Desktop's
// sessions are not to be read (Reads): its local_*.json and Cowork's
// folders are its session history.
func desktopReadDirs() []string {
	if !Reads("claude-desktop") {
		return nil
	}
	return callDesktopDirs()
}

// Calls reads per-file request shards, separately from the session summaries.
// Only changed files are scanned, and only a bounded number of parses stay in
// memory. Conversation text is read by ContentOf when a detail is opened.
//
// Over a long history a read decodes every shard (#2278: 68k of them, 415
// MB), so callers asking at once share one read of the same files, and only
// one read of every file runs at a time: the Sessions page's requests
// arriving together each held a whole read's calls, and the app grew to
// 85 GB.
func Calls(since time.Time) []Call { return sharedCalls(since, "") }

// AgentCalls is Calls of one agent's session files only: Codex's model and
// effort per thread need Codex's calls, not a read of every agent's.
func AgentCalls(since time.Time, agent string) []Call { return sharedCalls(since, agent) }

type callsKey struct {
	since int64
	zero  bool
	agent string
}

type callsFlight struct {
	done    chan struct{}
	calls   []Call
	waiters int
}

var (
	callsFlights = struct {
		sync.Mutex
		on map[callsKey]*callsFlight
	}{on: map[callsKey]*callsFlight{}}
	// callsReading holds one read of every file at a time.
	callsReading sync.Mutex
	// readingCalls, in tests, is told each time Calls reads the files.
	readingCalls func()
)

func sharedCalls(since time.Time, agent string) []Call {
	key := callsKey{since.UnixNano(), since.IsZero(), agent}
	callsFlights.Lock()
	if fl := callsFlights.on[key]; fl != nil {
		fl.waiters++
		callsFlights.Unlock()
		<-fl.done
		return slices.Clone(fl.calls)
	}
	fl := &callsFlight{done: make(chan struct{})}
	callsFlights.on[key] = fl
	callsFlights.Unlock()
	// Whatever happens to the read, the flight ends and its waiters go on.
	defer func() {
		callsFlights.Lock()
		if callsFlights.on[key] == fl {
			delete(callsFlights.on, key)
		}
		callsFlights.Unlock()
		close(fl.done)
	}()
	func() {
		callsReading.Lock()
		defer callsReading.Unlock()
		if readingCalls != nil {
			readingCalls()
		}
		fl.calls = callsFor(since, "", agent)
	}()
	// none joins once the flight is out of the map, so the count read with
	// that is every waiter
	callsFlights.Lock()
	if callsFlights.on[key] == fl {
		delete(callsFlights.on, key)
	}
	shared := fl.waiters > 0
	callsFlights.Unlock()
	if shared { // the waiters copy fl.calls, so the caller's own is another
		return slices.Clone(fl.calls)
	}
	return fl.calls
}

func callsFor(since time.Time, session, agent string) []Call {
	all := callSources()
	pruneCalls(all)
	files := all
	if agent != "" {
		files = slices.DeleteFunc(slices.Clone(all), func(f file) bool { return f.agent != agent })
	}
	// Earlier files own messages copied into a resumed Claude session.
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.Before(files[j].mod)
		}
		return files[i].path < files[j].path
	})
	seen := map[string]bool{}
	capacity := 0
	callsMu.Lock()
	for _, f := range files {
		if since.IsZero() || !f.mod.Before(modOf(since)) {
			capacity += callCounts[f.path]
		}
	}
	callsMu.Unlock()
	if session != "" {
		capacity = 0
	}
	out := make([]Call, 0, capacity)
	for _, f := range files {
		if !since.IsZero() && f.mod.Before(modOf(since)) {
			continue
		}
		st := readCalls(f)
		for _, c := range st.Calls {
			if c.Msg != "" {
				if seen[c.Msg] {
					continue
				}
				seen[c.Msg] = true
			}
			if (since.IsZero() || !c.Time.Before(since)) && (session == "" || c.Session == session) {
				out = append(out, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		if out[i].File != out[j].File {
			return out[i].File > out[j].File
		}
		return out[i].To > out[j].To
	})
	return out
}

// callFiles are the session files that hold calls: Claude Code's, Cowork's
// and Desktop's own, and Codex's.
func callFiles() []file { return callFilesWith(codexFiles()) }

// callFilesWith is callFiles with codex the rollouts codexFiles found.
func callFilesWith(codex []file) []file { return distinctOf(callFileLists(codex)...) }

// callFileLists are callFilesWith's lists, each path not yet kept once:
// the callers that list more add theirs and keep each path once, over
// all of them (distinctOf), instead of copying callFilesWith's again.
func callFileLists(codex []file) [][]file {
	lists := [][]file{claudeFiles()}
	for _, d := range desktopReadDirs() {
		// Cowork keeps a Claude Code folder of its own for each session
		homes, _ := SessionGlob(filepath.Join(d, "local-agent-mode-sessions", "*", "*", "local_*", ".claude"))
		for _, h := range homes {
			lists = append(lists, ccFiles("claude-desktop", h))
		}
	}
	return append(lists, allowed(codex), wslFiles("claude", "codex"))
}

// callSources are callFiles and the other agents' session files. Most are
// read whole rather than line by line: OpenCode's (#680) and ZCode's, whose
// calls are rows of their database or their JSON files; and WorkBuddy's, whose usage lines repeat a reply's id as it
// goes on, as CodeBuddy Code's do (#1779); and MiniMax Code's, whose messages
// file a later generation of its history may replace (#1605). Kimi Code's
// (#1607), Pi's and omp's (#1951) and Grok Build's (#4423) are lines of a
// file only appended to, read on as callFiles' are; DeepSeek Harness's, a
// file only appended to that may be packed in zstd frames, is read on
// from the frame it was left in (readDshCalls). Every agent here needs an
// entry in wholeCallReaders, lineCallReaders or resumeCallReaders.
func callSources() []file { return callSourcesWith(codexFiles()) }

// callSourcesWith is callSources with codex the rollouts codexFiles found.
func callSourcesWith(codex []file) []file { return callSourcesInto(nil, codex) }

// callSourcesInto is callSourcesWith appended to out.
func callSourcesInto(out []file, codex []file) []file {
	lists := append(callFileLists(codex), openCodeCallFiles(), zcodeCallFiles(), dshCallFiles(),
		minimaxCallFiles(), kimiCallFiles(), wslFiles("kimi"), piCallFiles(), codebuddyCallFiles(), workbuddyAIFiles(),
		agyCallFiles(),                        // #2097
		antigravityCallFiles(),                // #2190
		workbuddyCallFiles(), grokCallFiles()) // #4423
	if out == nil {
		return allowed(distinctOf(lists...))
	}
	return allowed(distinctInto(out, lists...))
}

// desktopDataDirs are Claude Desktop's folders on this computer that
// hold sessions: its own (%APPDATA%\Claude on Windows, or the MSIX
// package's, and the real ones beside a package's), the Claude folder
// magpie writes and Claude-3p, as desktopdir finds them.
func desktopDataDirs() []string { return desktopdir.Everywhere() }

// headLen is how much of a file's start is kept to know it again.
const headLen = 256

// headOf is the start of a file.
func headOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, headLen)
	n, _ := io.ReadFull(f, b)
	return string(b[:n])
}

// prepareCalls clones the continuation so a published index is never changed.
// owned: old was read from disk for this read alone (loadKept), so its
// calls are taken as they are rather than copied for st.
func prepareCalls(f file, old *callFile, owned bool) *callFile {
	// The disk shard stores final calls, not a second copy of every usage
	// observation. Rebuild continuation only when a cold source grows.
	if f.agent == "codex" && old != nil && (old.CX == nil || old.CX.Usage == nil) {
		old = nil
	}
	// Disk shards keep final calls, not message block/tool maps. A changed
	// Claude source reconstructs its transient revision index after restart.
	if (f.agent == "claude" || f.agent == "claude-desktop") && old != nil && old.Claude == nil {
		old = nil
	}

	head := headOf(f.path)
	var st *callFile
	if old != nil && !packed(f.path) && f.size >= old.Size && old.Off <= f.size && sameHead(head, old.Head, old.HeadSize) && old.ContentHash != "" && prefixHash(f.path, old.Off) == old.ContentHash {
		if owned {
			st = old.cloneState()
		} else {
			st = old.clone()
		}
	} else {
		st = &callFile{Agent: f.agent}
		if f.agent == "codex" {
			st.CX = &cxRun{}
			if m := rolloutName.FindStringSubmatch(filepath.Base(f.path)); m != nil {
				st.CX.Session, st.CX.ThreadID = m[1], m[1]
			}
		} else {
			st.Session = sessionOfPath(f.path)
			st.Msgs = map[string]int{}
		}
	}
	st.dirty = map[int]bool{}
	st.Head, st.HeadSize, st.Path = hashHead(head), len(head), f.path
	st.readSize = f.size
	return st
}

// growCalls makes room in st.Calls, when it is full, for the calls the
// rest of the file holds at the rate read so far: append grows a long
// list by a quarter at a time, and a Claude session's calls were copied
// into ~5 lists their size on the way (616 MB of a cold start's reading
// on mote, ~13% of all it allocated). The room is at most twice the
// calls so far, as append's own doubling of a short list.
func growCalls(st *callFile) {
	n := len(st.Calls)
	if n < cap(st.Calls) || n < 256 || st.End <= 0 || st.readSize <= st.End {
		return
	}
	// what the rest is likely to hold, and an eighth more
	want := min(int(int64(n)*st.readSize/st.End)*9/8, 2*n)
	if want > n+n/4+1 {
		st.Calls = slices.Grow(st.Calls, want-n)
	}
}

func (st *callFile) clone() *callFile {
	c := st.cloneState()
	c.Calls = append([]Call(nil), st.Calls...)
	c.Msgs, c.Began, c.Strs = maps.Clone(st.Msgs), maps.Clone(st.Began), maps.Clone(st.Strs)
	return c
}

// cloneState is a copy of st's reading state that shares its calls, and
// their Msgs, Began and Strs. The state may be a kept continuation's
// (restoreCallContinuation), so it is copied; the calls of a file just
// read from disk have no other reader, and copying them on every change
// of a live session was 160 MB of eight Requests polls (of 3.9 GB) on
// real history.
func (st *callFile) cloneState() *callFile {
	c := *st
	c.Claude = st.Claude.clone()
	if st.CX != nil {
		r := *st.CX
		r.Turns = maps.Clone(r.Turns)
		r.Usage = r.Usage.clone()
		r.Contexts = maps.Clone(r.Contexts)
		r.Timings = append([]cxCallTiming(nil), r.Timings...)
		c.CX = &r
	}
	if st.Pi != nil {
		p := *st.Pi
		c.Pi = &p
	}
	if st.Dsh != nil {
		d := *st.Dsh
		if d.Tail != nil {
			t := *d.Tail
			d.Tail = &t
		}
		c.Dsh = &d
	}
	if st.Grok != nil {
		g := *st.Grok
		c.Grok = &g
	}
	return &c
}

// cxAnswerItem is a rollout line that is the model's own output: a call has
// begun to answer. Its head is enough.
func cxAnswerItem(b []byte) bool {
	h := b[:min(len(b), 1024)]
	if typeAfter(h, cxType) != "response_item" {
		return false
	}
	switch typeAfter(h, cxPayload) {
	// magpie_reasoning: a reasoning item of magpie's, written so as Codex let
	// magpie go (PortCodexRolloutsLeft)
	case "reasoning", "magpie_reasoning", "function_call", "custom_tool_call", "web_search_call", "local_shell_call", "tool_search_call", "image_generation_call":
		return true
	case "message":
		return typeAfter(h, cxRole) == "assistant"
	}
	return false
}

// Codex tool results need only their timestamp. Compaction metadata must reach
// the usage reader even when its replacement history contains user messages.
func callHead(st *callFile, b []byte) bool {
	if st.Agent != "codex" {
		return true
	}
	if cxAnswerItem(b) {
		codexCallLine(st, b)
		return false
	}
	compacted := typeAfter(b[:min(len(b), 1024)], cxType) == "compacted"
	m := cxMarksOf(b)
	if !compacted && m&cxInput != 0 {
		codexCallLine(st, b)
		return false
	}
	return m&cxFollowed != 0
}

// cxMarks are the "type" values a rollout line holds anywhere in it that
// codexCallLine follows, and whether an object in it has the user's role.
type cxMarks uint16

const (
	cxMarkUserMsg cxMarks = 1 << iota
	cxMarkUserRole
	cxMarkToolOut
	cxMarkMeta
	cxMarkTurn
	cxMarkRecord
	cxMarkCompacted
	cxMarkCount
	cxMarkSettings
	cxMarkStarted
	cxMarkDone

	// what the model is given: the prompt, a tool's output
	cxInput = cxMarkUserMsg | cxMarkUserRole | cxMarkToolOut
	// the lines codexCallLine reads whole
	cxFollowed = cxMarkMeta | cxMarkTurn | cxMarkRecord | cxMarkCompacted | cxMarkCount | cxMarkSettings | cxMarkStarted | cxMarkDone
)

var (
	cxKeyEnd   = []byte(`":"`)
	cxTypeKey  = []byte(`"type`)
	cxRoleKey  = []byte(`"role`)
	cxUserName = []byte(`user"`)
)

// cxMarksOf reads b's marks in one pass: every `"type":"<v>"` and
// `"role":"user"` that a bytes.Contains of each would find, as one each
// had been (up to twelve passes over a line of megabytes, most of a cold
// usage read's CPU on Windows, #3493). In a JSON string the quotes are
// escaped, so the `":"` ending a key is never inside one.
func cxMarksOf(b []byte) (m cxMarks) {
	for {
		i := bytes.Index(b, cxKeyEnd)
		if i < 0 {
			return m
		}
		key, val := b[:i], b[i+len(cxKeyEnd):]
		b = val
		switch {
		case bytes.HasSuffix(key, cxTypeKey):
			j := bytes.IndexByte(val, '"')
			if j < 0 {
				return m
			}
			switch string(val[:j]) {
			case "user_message":
				m |= cxMarkUserMsg
			case "function_call_output", "custom_tool_call_output":
				m |= cxMarkToolOut
			case "session_meta":
				m |= cxMarkMeta
			case "turn_context":
				m |= cxMarkTurn
			case "token_usage_record":
				m |= cxMarkRecord
			case "compacted":
				m |= cxMarkCompacted
			case "token_count":
				m |= cxMarkCount
			case "thread_settings_applied":
				m |= cxMarkSettings
			case "task_started":
				m |= cxMarkStarted
			case "task_complete":
				m |= cxMarkDone
			}
		case bytes.HasSuffix(key, cxRoleKey) && bytes.HasPrefix(val, cxUserName):
			m |= cxMarkUserRole
		}
	}
}

// sessionOfPath is the session a Claude Code file belongs to by its name:
// <id>.jsonl, <id>/subagents/<agent>.jsonl, or a workflow's
// <id>/subagents/workflows/<run>/<agent>.jsonl.
func sessionOfPath(p string) string {
	d := filepath.Dir(p)
	if w := filepath.Dir(d); filepath.Base(w) == "workflows" && filepath.Base(filepath.Dir(w)) == "subagents" {
		return filepath.Base(filepath.Dir(filepath.Dir(w)))
	}
	if filepath.Base(d) == "subagents" {
		return filepath.Base(filepath.Dir(d))
	}
	return strings.TrimSuffix(filepath.Base(p), ".jsonl")
}

// SubagentOf is the subagent a Claude Code file is the conversation of, by
// its name: <agent> of <id>/subagents/agent-<agent>.jsonl or a workflow's
// <id>/subagents/workflows/<run>/agent-<agent>.jsonl — the agentId its
// lines carry, and the x-claude-code-agent-id Claude Code sends the
// gateway for it. "" for a session's own file, and any other agent's.
func SubagentOf(p string) string {
	d := filepath.Dir(p)
	if filepath.Base(d) != "subagents" && (filepath.Base(filepath.Dir(d)) != "workflows" || filepath.Base(filepath.Dir(filepath.Dir(d))) != "subagents") {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if name == filepath.Base(p) || !strings.HasPrefix(name, "agent-") {
		return ""
	}
	return strings.TrimPrefix(name, "agent-")
}

// SubagentParent is the subagent that started the one whose Claude Code
// file p is: the parentAgentId of its agent-<agent>.meta.json beside it,
// which Claude Code writes for a subagent a subagent started (spawnDepth
// 2 and on), the x-claude-code-parent-agent-id it sends the gateway. ""
// for one the conversation itself started, and for a file that is no
// subagent's. A meta file once read is kept; one not read is read again.
func SubagentParent(p string) string {
	if SubagentOf(p) == "" {
		return ""
	}
	return ccMeta(p).ParentAgentID
}

// str is s kept once per file, as the calls of a file repeat a few.
func (st *callFile) str(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := st.Strs[s]; ok {
		return v
	}
	if st.Strs == nil {
		st.Strs = map[string]string{}
	}
	st.Strs[s] = s
	return s
}

// ccStr is a string field that reads as "" when a file holds something else there.
type ccStr string

func (s *ccStr) UnmarshalJSON(b []byte) error {
	// a string with nothing escaped is its bytes: json.Unmarshal made a
	// decoder of its own for each, six to a reply line (29 MB on a first
	// start's 21 GB history, #2278). The line was checked whole before
	// this is called, and bytes that aren't UTF-8 are left to it, which
	// writes U+FFFD for them.
	if n := len(b); n >= 2 && b[0] == '"' && b[n-1] == '"' && bytes.IndexByte(b[1:n-1], '\\') < 0 && utf8.Valid(b[1:n-1]) {
		*s = ccStr(b[1 : n-1])
		return nil
	}
	var v string
	if json.Unmarshal(b, &v) == nil {
		*s = ccStr(v)
	}
	return nil
}

// ccCall is an assistant line of Claude Code's, less its content.
type ccCall struct {
	UUID       string `json:"uuid"`
	Type       string `json:"type"`
	Time       string `json:"timestamp"`
	SessionID  ccStr  `json:"sessionId"`
	RequestID  ccStr  `json:"requestId"`
	Cwd        ccStr  `json:"cwd"`
	Entrypoint ccStr  `json:"entrypoint"`
	APIError   bool   `json:"isApiErrorMessage"`
	Error      ccStr  `json:"error"`
	// the session's thinking effort, and the turn's own when it asked for
	// another (null when it did not)
	Effort  ccStr `json:"effort"`
	PerTurn ccStr `json:"perTurnEffort"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      int              `json:"input_tokens"`
			Output     int              `json:"output_tokens"`
			CacheRead  int              `json:"cache_read_input_tokens"`
			CacheWrite int              `json:"cache_creation_input_tokens"`
			Creation   *ccCacheCreation `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// claudeCallLine notes the call an assistant line is (a block of) in the
// file: the latest of a message's lines stands for it, as it has the final
// usage. A synthetic line is a call only as an API error.
func claudeCallLine(st *callFile, b []byte) {
	top, reply := ccLineType(b), false
	switch {
	case bytes.Equal(top, ccUserWord):
		ccUserCallLine(st, b)
		return
	case top != nil && !bytes.Equal(top, ccAssistantWord):
		ccOtherCallLine(st, b)
		return
	case top == nil:
		if reply = ccReplyHead(b); !reply && !bytes.Contains(b, ccAssistant) {
			ccOtherCallLine(st, b)
			return
		}
	}
	var l ccCall
	if ccDecode(b, &l) != nil || l.Type != "assistant" {
		if reply && !bytes.Contains(b, ccAssistant) {
			ccOtherCallLine(st, b)
		}
		return
	}
	claudeCall(st, b, &l)
}

var (
	ccType          = []byte(`"type":"`)
	ccUserWord      = []byte("user")
	ccAssistantWord = []byte("assistant")
)

// ccLineType is the type a Claude Code line's head names before any
// object or list opens in it, nil when it names none there. Claude Code
// writes a prompt's, a tool result's and an attachment's type before
// their message, a reply's after it: each line was searched for the
// type of a reply, an identity, a compaction and a prompt, four reads
// of a tool's whole result and ~7 s of a first start's CPU on a 14 GB
// history (#2278).
func ccLineType(b []byte) []byte {
	h := b[:min(len(b), 1024)]
	if len(h) == 0 || h[0] != '{' {
		return nil
	}
	i := bytes.Index(h, ccType)
	if i < 0 || bytes.ContainsAny(h[1:i], "{[") {
		return nil
	}
	rest := h[i+len(ccType):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return nil
	}
	return rest[:j]
}

// ccReplyHead says whether b opens as Claude Code writes a reply: its
// message first, with its model, then its role (claudeLine).
func ccReplyHead(b []byte) bool {
	h := b[:min(len(b), 1024)]
	m, a := bytes.Index(h, ccMessage), bytes.Index(h, ccAttach)
	if m < 0 || a >= 0 && a < m {
		return false
	}
	msg := h[m+len(ccMessage):]
	if !bytes.HasPrefix(msg, ccModel) {
		return false
	}
	i := bytes.Index(msg, ccRole)
	return i >= 0 && bytes.HasPrefix(msg[i+len(ccRole):], []byte(`assistant"`))
}

// ccDecode is json.Unmarshal of b into l with its message's content
// null, when it has one: a reply's usage, ids and model are decoded
// without its text, thinking and tools' input, which encoding/json read
// twice over (checked, then decoded) for fields it doesn't keep. The line
// without its content is made in a buffer kept for the next line, and the
// content is found without gjson.GetBytes's copy of it: a first start's
// read of a 21 GB history made 82 MB of those lines and 27 MB of copies
// of their content, which nothing kept (#2278).
func ccDecode(b []byte, l *ccCall) error {
	_, err := ccDecodeLess(b, l)
	return err
}

// ccDecodeLess is ccDecode into any v: json.Unmarshal(b, v) with the
// message's content null, the content given as the bytes of b that hold
// it (nil when b has none, and v was decoded from b whole). v keeps no
// byte of the line decoded: its strings are copies.
func ccDecodeLess(b []byte, v any) ([]byte, error) {
	if len(b) == 0 {
		return nil, json.Unmarshal(b, v)
	}
	c := gjson.Get(unsafe.String(&b[0], len(b)), "message.content")
	if !c.Exists() || c.Index <= 0 || c.Index+len(c.Raw) > len(b) {
		return nil, json.Unmarshal(b, v)
	}
	p := ccLines.Get().(*[]byte)
	out := append((*p)[:0], b[:c.Index]...)
	out = append(out, "null"...)
	out = append(out, b[c.Index+len(c.Raw):]...)
	err := json.Unmarshal(out, v)
	if cap(out) <= ccLineKept {
		*p = out
		ccLines.Put(p)
	}
	return b[c.Index : c.Index+len(c.Raw) : c.Index+len(c.Raw)], err
}

// ccLines holds the buffers ccDecode makes its lines in; one grown past
// ccLineKept, for a reply line with a long head, is left to the GC.
var ccLines = sync.Pool{New: func() any { return new([]byte) }}

const ccLineKept = 64 << 10

// ccOtherCallLine reads a line of Claude Code's that is not a reply.
func ccOtherCallLine(st *callFile, b []byte) {
	switch {
	case bytes.Contains(b, ccIdentity):
		// a note of the model it runs as, on the first turn and when it is changed
		var n struct {
			Attachment struct {
				Identity struct {
					ModelID string `json:"modelId"`
				} `json:"identity"`
			} `json:"attachment"`
		}
		if json.Unmarshal(b, &n) == nil && n.Attachment.Identity.ModelID != "" {
			st.Requested = st.str(n.Attachment.Identity.ModelID)
		}
	case bytes.Contains(b, ccCompact):
		// {"type":"system","subtype":"compact_boundary","compactMetadata":{"durationMs":48913,…}}
		var n struct {
			Meta struct {
				DurationMs int64 `json:"durationMs"`
			} `json:"compactMetadata"`
		}
		if json.Unmarshal(b, &n) == nil && n.Meta.DurationMs > 0 {
			st.CompactMs = n.Meta.DurationMs
		}
	case bytes.Contains(b, ccUserLine):
		ccUserCallLine(st, b)
	}
}

// ccUserCallLine notes what a reply is asked by: a prompt, or a tool's
// result.
func ccUserCallLine(st *callFile, b []byte) {
	if at := tsAt(b, true); !at.IsZero() {
		st.LastUser = at
	}
}

// claudeCall is claudeCallLine of a reply's line b, l.
func claudeCall(st *callFile, b []byte, l *ccCall) {
	at, _ := time.Parse(time.RFC3339Nano, l.Time)
	model := l.Message.Model
	if at.IsZero() || model == "<synthetic>" && !l.APIError {
		return
	}
	c := Call{Time: at, Agent: st.Agent, Session: st.str(string(l.SessionID)), Cwd: st.str(string(l.Cwd)), RequestID: string(l.RequestID)}
	c.Requested = st.Requested
	m := ccUsageState(&st.Claude).message(l.Message.ID, string(l.RequestID))
	defer st.Claude.finish(m)
	// Materialized rows already carry the last known usage beyond the recent
	// replay window. Reuse the original row lookup when that history expires.
	if m.Call < 0 && l.Message.ID != "" && len(st.Claude.Messages[m.key]) == 1 {
		if i, ok := st.Msgs[l.Message.ID]; ok {
			old := st.Calls[i]
			if m.Request == "" || old.RequestID == "" || m.Request == old.RequestID {
				if m.Request == "" && old.RequestID != "" {
					m = st.Claude.message(l.Message.ID, old.RequestID)
				}
				m.Call, m.Began = i, st.Began[l.Message.ID]
				m.Usage = &claudeUsageVersion{At: old.Time, Model: old.Model, Tokens: old.Tokens}
				m.Updated = old.Time
			}
		}
	}
	c.RequestID = m.Request
	blockID := "uuid:" + l.UUID
	if l.UUID == "" {
		// Older writers lack a block UUID. Content distinguishes a new
		// text/tool block from a replay without treating its timestamp as ID.
		var content struct {
			Message struct{ Content json.RawMessage }
		}
		if json.Unmarshal(b, &content) == nil {
			blockID = fmt.Sprintf("content:%x", sha256.Sum256(content.Message.Content))
		}
	}
	newBlock := !m.Blocks[blockID]
	if blockID != "" {
		if m.Blocks == nil {
			m.Blocks = map[string]bool{}
		}
		m.Blocks[blockID] = true
	}
	// it was asked when the last user line or, with none since, the last reply ended
	asked, seen := st.LastUser, false
	if m.Call >= 0 {
		asked, seen = m.Began, true
	}
	if !seen && st.LastAsst.After(asked) {
		asked = st.LastAsst
	}
	// where its asking began: after the last reply's line, its own if it goes on
	c.File, c.From, c.To, c.Msg = st.Path, st.AsstEnd, st.End, l.Message.ID
	defer func() {
		if at.After(st.LastAsst) {
			st.LastAsst = at
		}
		st.AsstEnd = st.End
	}()
	if e := cmp.Or(string(l.PerTurn), string(l.Effort)); e != "" {
		c.Effort = st.str(e)
	}
	if c.Session == "" {
		c.Session = st.Session
	}
	if st.Agent == "claude" && desktopEntrypoint(string(l.Entrypoint)) {
		c.Agent = "claude-desktop"
	}
	if l.APIError {
		c.Error = st.str(string(l.Error))
		if c.Error == "" {
			c.Error = "unknown"
		}
		var m struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		json.Unmarshal(b, &m)
		c.ErrorText = firstChars(ccText(m.Message.Content), 400)
	} else {
		u := l.Message.Usage
		if u == nil {
			return
		}
		c.Model = st.str(model)
		c.Tokens = ccTokens(u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Creation)
		if c.Tokens.zero() && m.Usage == nil {
			return
		}
		if !asked.IsZero() && at.After(asked) {
			c.Millis = at.Sub(asked).Milliseconds()
		}
	}
	if _, changed := m.accept(at, c.Model, c.Tokens); !changed {
		// A missing request ID can become known without changing usage.
		if i := m.Call; i >= 0 {
			previous := &st.Calls[i]
			changed := previous.RequestID != m.Request
			previous.RequestID = m.Request
			// A new block can finish a reply with unchanged usage. Retain
			// its duration/content boundary without moving usage across days.
			if newBlock && m.Usage.Model == c.Model && m.Usage.Tokens == c.Tokens &&
				at.After(previous.Time) && dateOf(at) == dateOf(previous.Time) {
				previous.Time, previous.Millis, previous.To = at, c.Millis, c.To
				changed = true
			}
			if changed && st.dirty != nil {
				st.dirty[i] = true
			}
		}
		return
	}
	if i := m.Call; i >= 0 {
		c.From = st.Calls[i].From // its asking began with its first line
		st.Calls[i] = c
		if st.dirty != nil {
			st.dirty[i] = true
		}
		return
	}
	m.Call, m.Began = len(st.Calls), asked
	if c.Msg != "" {
		if st.Msgs == nil {
			st.Msgs = map[string]int{}
		}
		if st.Began == nil {
			st.Began = map[string]time.Time{}
		}
		st.Msgs[c.Msg], st.Began[c.Msg] = m.Call, asked
	}
	if c.Error == "" {
		c.Compact, st.CompactMs = st.CompactMs, 0
	}
	growCalls(st)
	st.Calls = append(st.Calls, c)
}

// firstChars is the first n characters of s.
func firstChars(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var (
	ccIdentity = []byte(`"identity":{"modelId":"`)
	ccUserLine = []byte(`"type":"user"`)
	ccCompact  = []byte(`"subtype":"compact_boundary"`)
	cxStarted  = []byte(`"type":"task_started"`)
)

// cxCall is a line of a Codex rollout that says something about calls.
type cxCall struct {
	Type    string `json:"type"`
	Payload struct {
		cxCompaction
		cxHistorySnapshot
		Type        string   `json:"type"`
		ID          string   `json:"id"`
		SessionID   string   `json:"session_id"`
		ThreadID    string   `json:"thread_id"`
		ForkedFrom  string   `json:"forked_from_id"`
		Upstream    string   `json:"model_provider"`
		AccountID   string   `json:"creator_account_id"`
		UserID      string   `json:"creator_user_id"`
		Cwd         string   `json:"cwd"`
		Model       string   `json:"model"`
		Effort      ccStr    `json:"effort"`
		TurnID      string   `json:"turn_id"`
		ResponseID  string   `json:"response_id"`
		Usage       *cxUsage `json:"usage"`
		ThreadUsage *cxUsage `json:"thread_token_usage"`
		Duration    int64    `json:"duration_ms"`
		FirstToken  int64    `json:"time_to_first_token_ms"`
		Settings    struct {
			Model    string `json:"model"`
			Upstream string `json:"model_provider"`
		} `json:"thread_settings"`
		Info *struct {
			Total *cxCallUsage `json:"total_token_usage"`
			Last  *cxCallUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// codexCallLine follows a rollout: the model, effort and turn in force, and
// a call wherever the running total grew.
func codexCallLine(st *callFile, b []byte) {
	r := st.CX
	if r.Usage == nil {
		r.Usage = &codexUsageState{}
	}
	if cxAnswerItem(b) {
		if !r.Answering {
			r.Answering, r.AskedAt, r.AskedTurn = true, r.LastIn, r.Turn
		}
		if at := tsAt(b, false); !at.IsZero() {
			r.Answered = at
		}
		return
	}
	// what the model is given: the prompt, a tool's output
	compacted := typeAfter(b[:min(len(b), 1024)], cxType) == "compacted"
	if compacted {
		st.codexChanged(r.Usage.compacted(tsAt(b, false), r.Model, cxCompactionIn(b)))
		return
	}
	m := cxMarksOf(b)
	if m&cxInput != 0 {
		if at := tsAt(b, false); !at.IsZero() {
			r.LastIn = at
			if m&(cxMarkUserMsg|cxMarkUserRole) != 0 {
				r.Usage.Pending = 0
			}
			r.saveContext()
		}
		return
	}
	if r.Meta {
		m &^= cxMarkMeta
	}
	if m&cxFollowed == 0 {
		return
	}
	var l cxCall
	if json.Unmarshal(b, &l) != nil {
		return
	}
	p := &l.Payload
	switch {
	case l.Type == "session_meta":
		if r.Meta {
			return
		}
		r.Meta = true
		if p.ID != "" {
			r.ThreadID = st.str(p.ID)
		}
		if p.SessionID != "" {
			r.Session = p.SessionID
		} else if p.ID != "" {
			r.Session = p.ID
		}
		r.Session, r.Cwd = st.str(r.Session), st.str(p.Cwd)
		r.Upstream, r.AccountID, r.UserID = st.str(p.Upstream), st.str(p.AccountID), st.str(p.UserID)
		r.Usage.context(r.Session, "", "")
		r.Usage.beginSnapshot(tsAt(b, false), p.ID, p.cxHistorySnapshot)
		r.Usage.forkedFrom(st.Path, p.ForkedFrom)
	case l.Type == "turn_context":
		if p.Upstream != "" {
			r.Upstream = st.str(p.Upstream)
		}
		if p.Model != "" {
			r.Model = st.str(p.Model)
		}
		r.Effort = st.str(string(p.Effort))
		if p.Cwd != "" {
			r.Cwd = st.str(p.Cwd)
		}
		if p.TurnID != "" {
			r.Turn = p.TurnID
		}
		r.saveContext()
	case l.Type == "event_msg" && p.Type == "task_started":
		if p.TurnID != "" {
			r.Turn = p.TurnID
		}
		// a turn's first call was asked for as it began, whatever else was seen
		if at := tsAt(b, false); !at.IsZero() {
			r.LastIn = at
		}
		r.Answering = false
		r.saveContext()
	case l.Type == "event_msg" && p.Type == "thread_settings_applied":
		if p.Settings.Upstream != "" {
			r.Upstream = st.str(p.Settings.Upstream)
		}
		if p.Settings.Model != "" {
			r.Model = st.str(p.Settings.Model)
		}
		r.saveContext()
	case l.Type == "event_msg" && p.Type == "task_complete":
		if r.Turns == nil {
			r.Turns = map[string]cxOpen{}
		}
		turn := r.Turns[p.TurnID]
		turn.Complete, turn.Duration, turn.FirstToken = true, p.Duration, p.FirstToken
		r.Turns[p.TurnID] = turn
		if turn.N == 1 {
			st.Calls[turn.First].TTFT, st.Calls[turn.First].Millis = p.FirstToken, p.Duration
			st.codexCompact(turn.First)
			st.markCall(turn.First)
		}
	case l.Type == "compacted":
		r.Answering = false
		st.codexChanged(r.Usage.compacted(tsAt(b, false), r.Model, p.cxCompaction))
	case l.Type == "token_usage_record":
		if p.Usage != nil {
			st.codexChanged(r.Usage.record(codexContribution{ResponseID: p.ResponseID, Session: p.SessionID, ThreadID: p.ThreadID, Turn: p.TurnID, Model: r.Model, At: tsAt(b, false), Usage: *p.Usage, Total: p.ThreadUsage}))
		}
	case l.Type == "event_msg" && p.Type == "token_count":
		if p.Info != nil {
			st.codexChanged(r.Usage.count(tsAt(b, false), r.Model, p.Info.Total, p.Info.Last))
		}
	}

}

func (r *cxRun) saveContext() {
	if r.Contexts == nil {
		r.Contexts = map[string]cxContext{}
	}
	r.Contexts[r.Turn] = cxContext{r.Model, r.Effort, r.Cwd, r.Upstream, r.LastIn}
	r.Usage.context(r.Session, r.Turn, r.Model)
}

func (st *callFile) markCall(i int) {
	if st.dirty != nil {
		st.dirty[i] = true
	}
}

func (st *callFile) codexChanged(change *codexChange) {
	if change == nil {
		return
	}
	r, v, i := st.CX, change.Value, change.Index
	context, ok := r.Contexts[v.Turn]
	if !ok {
		context = cxContext{r.Model, r.Effort, r.Cwd, r.Upstream, r.LastIn}
	}
	threadID := v.ThreadID
	if threadID == "" && !v.ThreadIDConflict {
		threadID = r.ThreadID
	}
	c := Call{Time: v.At, Agent: "codex", Session: r.Session, ThreadID: threadID, Model: v.Model, Tokens: spent(v.Usage.raw()), ResponseID: v.ResponseID,
		Upstream: context.Upstream, AccountID: r.AccountID, UserID: r.UserID, Reasoning: v.Usage.Reasoning, Effort: context.Effort, Compaction: v.Compaction, Cwd: context.Cwd, File: st.Path, From: r.LastEnd, To: st.End}
	if c.Session == "" {
		c.Session = r.Session
	}

	if change.Old != nil {
		// The response may arrive after the next call's content. Upgrading its
		// identity/time must not expand its physical content range into that call.
		c.From, c.To = st.Calls[i].From, min(st.Calls[i].To, st.End)
		st.Calls[i] = c
	} else {
		prev := -1
		if i > 0 && !r.LastCall.After(v.At) {
			prev = r.LastIndex
		} else {
			for j, old := range st.Calls {
				if old.Time.Before(v.At) && (prev < 0 || old.Time.After(st.Calls[prev].Time)) {
					prev = j
				}
			}
		}
		input, end := context.LastIn, time.Time{}
		if r.Answering && r.AskedTurn == v.Turn && !r.AskedAt.IsZero() {
			input = r.AskedAt
			// its own tool's output came between its answer and this line
			if r.LastIn.After(r.Answered) && !r.Answered.After(v.At) {
				end = r.Answered
			}
		}
		if input.After(v.At) {
			input = time.Time{}
		}
		r.Answering = false
		r.Timings = append(r.Timings, cxCallTiming{input, prev, end})
		st.Calls = append(st.Calls, c)
		if r.Turns == nil {
			r.Turns = map[string]cxOpen{}
		}
		turn := r.Turns[v.Turn]
		if turn.N == 0 {
			turn.First = i
		}
		turn.N++
		r.Turns[v.Turn] = turn
		if turn.N == 2 {
			st.codexTiming(turn.First)
		}
		if st.End > r.LastEnd {
			r.LastEnd = st.End
		}
	}
	if i == r.LastIndex || v.At.After(r.LastCall) {
		r.LastCall, r.LastIndex = v.At, i
	}
	st.codexTiming(i)
	if change.Old != nil && !change.Old.At.Equal(v.At) {
		for j, timing := range r.Timings {
			if timing.Previous == i {
				st.codexTiming(j)
			}
		}
	}
}

func (st *callFile) codexTiming(i int) {
	r := st.CX
	c, timing := &st.Calls[i], r.Timings[i]
	asked := timing.Input
	if timing.Previous >= 0 && st.Calls[timing.Previous].Time.After(asked) {
		asked = st.Calls[timing.Previous].Time
	}
	end := c.Time
	if !timing.End.IsZero() {
		end = timing.End
	}
	c.Millis, c.TTFT = 0, 0
	if !asked.IsZero() && end.After(asked) && end.Sub(asked) < 2*time.Hour {
		c.Millis = end.Sub(asked).Milliseconds()
	}
	turn := r.Turns[r.Usage.Entries[i].Turn]
	if turn.N == 1 && turn.Complete {
		c.Millis, c.TTFT = turn.Duration, turn.FirstToken
	}
	st.codexCompact(i)
	st.markCall(i)

}

// codexCompact notes a compaction's call's time as the wait it made: Codex
// compacts by a call of its own to the model.
func (st *callFile) codexCompact(i int) {
	c := &st.Calls[i]
	c.Compact = 0
	if e := st.CX.Usage.Entries; i < len(e) && e[i].Compaction {
		c.Compact = c.Millis
	}
}

func hashHead(head string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(head))) }
func sameHead(head, hash string, size int) bool {
	return size >= 0 && len(head) >= size && hashHead(head[:size]) == hash
}

// prefixHash checks distributed windows of the indexed prefix. Small files
// are checked in full. The version tag invalidates earlier full-file hashes.
// Rewrites outside the sampled windows can go undetected; appends cost at most
// 64 KiB of reads per fingerprint regardless of the session's length.
func prefixHash(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	const window, samples = int64(4096), int64(16)
	// io.CopyN made a 32 KB buffer each call, and each of the 16 samples
	// of a long file: 0.8 GB of a cold start's first 2 minutes.
	buf := hashBufs.Get().(*[window * samples]byte)
	defer hashBufs.Put(buf)
	if n <= window*samples {
		if _, err = io.ReadFull(f, buf[:n]); err != nil {
			return ""
		}
		h.Write(buf[:n])
	} else {
		for i := int64(0); i < samples; i++ {
			off := (n - window) * i / (samples - 1)
			// A full window may come back with io.EOF at the file's end.
			if k, _ := f.ReadAt(buf[:window], off); int64(k) < window {
				return ""
			}
			h.Write(buf[:window])
		}
	}
	return fmt.Sprintf("sample-v1:%x", h.Sum(nil))
}

var hashBufs = sync.Pool{New: func() any { return new([64 << 10]byte) }}

// CallSource identifies a raw local request log without reading its contents.
type CallSource struct {
	Path, Key, Agent string
	Size             int64
	Modified         time.Time
}

func CallSources() []CallSource { return callSourcesOf(callSources()) }

// CallSourcesAndCodexIdentities is CallSources and CodexIdentities from one
// walk of the Codex folders. The Requests page asks for both on every
// rebuild, and the walk lstat()s each rollout: twice over 12k rollouts was
// ~150ms of each rebuild.
func CallSourcesAndCodexIdentities() ([]CallSource, map[string]CodexIdentity) {
	codex := codexFiles()
	// the listing is let go once its sources are taken: made new on each
	// Requests and effort ask, it was ~100 MB of a minute of polls (#2278)
	fs := callSourcesInto(listingBuf(), codex)
	sources := callSourcesOf(fs)
	putListingBuf(fs)
	return sources, codexIdentitiesOf(codex)
}

// listingBufs are listings a read takes and lets go of before it ends,
// kept on a shelf: a sync.Pool, emptied by the collections a poll makes
// about twice a second, had none for most reads (#2278).
var listingBufs shelf[[]file]

func listingBuf() []file {
	if b, ok := listingBufs.take(nil); ok {
		return b[:0]
	}
	return nil
}

func putListingBuf(fs []file) {
	clear(fs[:cap(fs)]) // its files' strings go with the listing
	listingBufs.give(fs[:0], func(h, fs []file) bool { return cap(h) < cap(fs) })
}

func callSourcesOf(fs []file) []CallSource {
	pruneCalls(fs)
	out := make([]CallSource, 0, len(fs))
	for _, f := range fs {
		out = append(out, CallSource{f.path, f.key, f.agent, f.size, f.mod.Time()})
	}
	return out
}

// ReadCallSource returns immutable calls for one source. Independent files can
// be read concurrently; disk IO never holds callsMu.
func ReadCallSource(s CallSource) []Call {
	if !Reads(s.Agent) {
		return nil
	}
	st := readCalls(file{path: s.Path, key: s.Key, agent: s.Agent, size: s.Size, mod: modOf(s.Modified)})
	// Requests retains its own compact snapshot. Keeping the expanded parser's
	// calls as well doubles residency, especially for long active sessions. The
	// append-frame shard still carries the continuation for the next read.
	callsMu.Lock()
	keepCallContinuation(st)
	// Index eviction can replace the cached snapshot with a shallow copy.
	// Release its rows too, but never evict a newer parse from another reader.
	cached := callCache[s.Path]
	if cached != nil && cached.Size == st.Size && cached.Mod == st.Mod && cached.Off == st.Off && cached.ContentHash == st.ContentHash {
		delete(callCache, s.Path)
		for i, p := range callOrder {
			if p == s.Path {
				callOrder = append(callOrder[:i], callOrder[i+1:]...)
				break
			}
		}
	}
	callsMu.Unlock()
	return st.Calls
}

// desktopEntrypoint is a Claude Code transcript's entrypoint when Claude
// Desktop ran it for its Code tab: "claude-desktop", or
// "claude-desktop-3p" in its third-party inference mode (Desktop 2.7032's
// nK: type "3p"), the mode a gateway such as magpie's is set in.
func desktopEntrypoint(e string) bool {
	return e == "claude-desktop" || e == "claude-desktop-3p"
}
