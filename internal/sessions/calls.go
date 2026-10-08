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
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
)

// Call is one model call as an agent's own session file records it.
type Call struct {
	ResponseID string    `json:"response_id,omitempty"`
	Time       time.Time `json:"t"`
	Agent      string    `json:"agent"`   // claude (Claude Code's CLI), claude-desktop (Desktop's Code tab and Cowork), codex
	Session    string    `json:"session"` // the id the agent sends the gateway as its session header
	Model      string    `json:"model"`   // the model the file names: for Claude, the one the vendor answered
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
	// Millis: how long the call took. Codex's, of a turn that made this call alone, is what
	// it says; the rest are told from the file's times, the call's last line less the line
	// that asked for it (a prompt, a tool's result), and so a little more or less than it was
	Millis int64  `json:"ms,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
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
	Size        int64
	Mod         int64  // unix nanoseconds
	Off         int64  // after the last whole line read
	Head        string // hash of the prefix, to tell one replaced from one grown
	HeadSize    int
	Path        string
	Calls       []Call
	Agent       string
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
	// Codex
	CX *cxRun
}

// cxRun is where a rollout's read stands: what its lines have said so far.
type cxRun struct {
	Meta                              bool // session_meta seen
	Session, Cwd, Model, Effort, Turn string
	Upstream, AccountID, UserID       string
	Usage                             *codexUsageState
	Contexts                          map[string]cxContext
	Turns                             map[string]cxOpen
	LastIn, LastCall                  time.Time // the last thing the model was given, and the last call's end
	LastEnd                           int64     // where the last call's line ended
	Timings                           []cxCallTiming
	LastIndex                         int
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
}

type cxContext struct {
	Model, Effort, Cwd, Upstream string
	LastIn                       time.Time
}

type cxCallUsage = cxUsage

// callDesktopDirs are Claude Desktop's data folders. Tests swap it.
var callDesktopDirs = desktopDataDirs

// DesktopDataDirs are the folders whose session metadata belongs to this index.
func DesktopDataDirs() []string { return callDesktopDirs() }

// Calls reads per-file request shards, separately from the session summaries.
// Only changed files are scanned, and only a bounded number of parses stay in
// memory. Conversation text is read by ContentOf when a detail is opened.
func Calls(since time.Time) []Call { return callsFor(since, "") }

func callsFor(since time.Time, session string) []Call {
	files := callSources()
	pruneCalls(files)
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
		if since.IsZero() || !f.mod.Before(since) {
			capacity += callCounts[f.path]
		}
	}
	callsMu.Unlock()
	if session != "" {
		capacity = 0
	}
	out := make([]Call, 0, capacity)
	for _, f := range files {
		if !since.IsZero() && f.mod.Before(since) {
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
func callFiles() []file {
	var out []file
	seen := map[string]bool{}
	add := func(fs []file) {
		for _, f := range fs {
			if !seen[f.path] {
				seen[f.path] = true
				out = append(out, f)
			}
		}
	}
	add(ccFiles("claude", ClaudeDir()))
	for _, d := range callDesktopDirs() {
		// Cowork keeps a Claude Code folder of its own for each session
		homes, _ := SessionGlob(filepath.Join(d, "local-agent-mode-sessions", "*", "*", "local_*", ".claude"))
		for _, h := range homes {
			add(ccFiles("claude-desktop", h))
		}
	}
	add(codexFiles())
	add(wslFiles("claude", "codex"))
	return out
}

// callSources are callFiles and the agents whose calls are read whole
// rather than line by line: OpenCode's (#680) and ZCode's, whose calls are
// rows of their database or their JSON files; DeepSeek Harness's, whose
// session file is packed in frames and so cannot be read on from the middle
// of one; and WorkBuddy's, whose usage lines repeat a reply's id as it goes
// on. Every agent here needs an entry in wholeCallReaders.
func callSources() []file {
	out := callFiles()
	out = append(out, openCodeCallFiles()...)
	out = append(out, zcodeCallFiles()...)
	out = append(out, dshCallFiles()...)
	return append(out, workbuddyCallFiles()...)
}

// desktopDataDirs are Claude Desktop's folders on this computer that
// hold sessions: its own (%APPDATA%\Claude on Windows, or the MSIX
// package's), the Claude folder magpie writes and Claude-3p, as
// desktopdir finds them.
func desktopDataDirs() []string { return desktopdir.Here().All() }

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
func prepareCalls(f file, old *callFile) *callFile {
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
	if old != nil && !packed(f.path) && f.size >= old.Size && old.Off <= f.size && sameHead(head, old.Head, old.HeadSize) && old.ContentHash != "" && prefixHash(f.path, old.Size) == old.ContentHash {
		st = old.clone()
	} else {
		st = &callFile{Agent: f.agent}
		if f.agent == "codex" {
			st.CX = &cxRun{}
			if m := rolloutName.FindStringSubmatch(filepath.Base(f.path)); m != nil {
				st.CX.Session = m[1]
			}
		} else {
			st.Session = sessionOfPath(f.path)
			st.Msgs = map[string]int{}
		}
	}
	st.dirty = map[int]bool{}
	st.Head, st.HeadSize, st.Path = hashHead(head), len(head), f.path
	return st
}

func (st *callFile) clone() *callFile {
	c := *st
	c.Calls = append([]Call(nil), st.Calls...)
	c.Claude = st.Claude.clone()
	c.Msgs, c.Began, c.Strs = maps.Clone(st.Msgs), maps.Clone(st.Began), maps.Clone(st.Strs)
	if st.CX != nil {
		r := *st.CX
		r.Turns = maps.Clone(r.Turns)
		r.Usage = r.Usage.clone()
		r.Contexts = maps.Clone(r.Contexts)
		r.Timings = append([]cxCallTiming(nil), r.Timings...)
		c.CX = &r
	}
	return &c
}

// Codex tool results need only their timestamp. Compaction metadata must reach
// the usage reader even when its replacement history contains user messages.
func callHead(st *callFile, b []byte) bool {
	if st.Agent != "codex" {
		return true
	}
	compacted := typeAfter(b[:min(len(b), 1024)], cxType) == "compacted"
	if !compacted && (bytes.Contains(b, cxUserMsg) || bytes.Contains(b, cxUserRole) || bytes.Contains(b, cxToolOut) || bytes.Contains(b, cxCustomOut)) {
		codexCallLine(st, b)
		return false
	}
	return bytes.Contains(b, cxMeta) || bytes.Contains(b, cxTurn) || bytes.Contains(b, cxRecord) || bytes.Contains(b, cxCompacted) || bytes.Contains(b, cxCount) || bytes.Contains(b, cxSettings) || bytes.Contains(b, cxStarted) || bytes.Contains(b, cxDone)
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
	if !bytes.Contains(b, ccAssistant) {
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
		case bytes.Contains(b, ccUserLine):
			// what a reply is asked by: a prompt, or a tool's result
			if at := tsAt(b, true); !at.IsZero() {
				st.LastUser = at
			}
		}
		return
	}
	var l ccCall
	if json.Unmarshal(b, &l) != nil || l.Type != "assistant" {
		return
	}
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
	if st.Agent == "claude" && l.Entrypoint == "claude-desktop" {
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
	ccIdentity  = []byte(`"identity":{"modelId":"`)
	ccUserLine  = []byte(`"type":"user"`)
	cxToolOut   = []byte(`"type":"function_call_output"`)
	cxCustomOut = []byte(`"type":"custom_tool_call_output"`)
	cxStarted   = []byte(`"type":"task_started"`)
	cxDone      = []byte(`"type":"task_complete"`)
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
	// what the model is given: the prompt, a tool's output
	compacted := typeAfter(b[:min(len(b), 1024)], cxType) == "compacted"
	if compacted {
		st.codexChanged(r.Usage.compacted(tsAt(b, false), r.Model, cxCompactionIn(b)))
		return
	}
	if bytes.Contains(b, cxUserMsg) || bytes.Contains(b, cxUserRole) || bytes.Contains(b, cxToolOut) || bytes.Contains(b, cxCustomOut) {
		if at := tsAt(b, false); !at.IsZero() {
			r.LastIn = at
			if bytes.Contains(b, cxUserMsg) || bytes.Contains(b, cxUserRole) {
				r.Usage.Pending = 0
			}
			r.saveContext()
		}
		return
	}
	if !(!r.Meta && bytes.Contains(b, cxMeta) || bytes.Contains(b, cxTurn) || bytes.Contains(b, cxRecord) || bytes.Contains(b, cxCompacted) || bytes.Contains(b, cxCount) ||
		bytes.Contains(b, cxSettings) || bytes.Contains(b, cxStarted) || bytes.Contains(b, cxDone)) {
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
		if p.SessionID != "" {
			r.Session = p.SessionID
		} else if p.ID != "" {
			r.Session = p.ID
		}
		r.Session, r.Cwd = st.str(r.Session), st.str(p.Cwd)
		r.Upstream, r.AccountID, r.UserID = st.str(p.Upstream), st.str(p.AccountID), st.str(p.UserID)
		r.Usage.context(r.Session, "", "")
		r.Usage.beginSnapshot(tsAt(b, false), p.ID, p.cxHistorySnapshot)
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
			st.markCall(turn.First)
		}
	case l.Type == "compacted":
		st.codexChanged(r.Usage.compacted(tsAt(b, false), r.Model, p.cxCompaction))
	case l.Type == "token_usage_record":
		if p.Usage != nil {
			st.codexChanged(r.Usage.record(codexContribution{ResponseID: p.ResponseID, Session: p.SessionID, Turn: p.TurnID, Model: r.Model, At: tsAt(b, false), Usage: *p.Usage, Total: p.ThreadUsage}))
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
	c := Call{Time: v.At, Agent: "codex", Session: r.Session, Model: v.Model, Tokens: spent(v.Usage.raw()), ResponseID: v.ResponseID,
		Upstream: context.Upstream, AccountID: r.AccountID, UserID: r.UserID, Reasoning: v.Usage.Reasoning, Effort: context.Effort, Cwd: context.Cwd, File: st.Path, From: r.LastEnd, To: st.End}
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
		input := context.LastIn
		if input.After(v.At) {
			input = time.Time{}
		}
		r.Timings = append(r.Timings, cxCallTiming{input, prev})
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
	c.Millis, c.TTFT = 0, 0
	if !asked.IsZero() && c.Time.After(asked) && c.Time.Sub(asked) < 2*time.Hour {
		c.Millis = c.Time.Sub(asked).Milliseconds()
	}
	turn := r.Turns[r.Usage.Entries[i].Turn]
	if turn.N == 1 && turn.Complete {
		c.Millis, c.TTFT = turn.Duration, turn.FirstToken
	}
	st.markCall(i)

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
	if n <= window*samples {
		if _, err = io.CopyN(h, f, n); err != nil {
			return ""
		}
	} else {
		for i := int64(0); i < samples; i++ {
			off := (n - window) * i / (samples - 1)
			if _, err = io.CopyN(h, io.NewSectionReader(f, off, window), window); err != nil {
				return ""
			}
		}
	}
	return fmt.Sprintf("sample-v1:%x", h.Sum(nil))
}

// CallSource identifies a raw local request log without reading its contents.
type CallSource struct {
	Path, Key, Agent string
	Size             int64
	Modified         time.Time
}

func CallSources() []CallSource {
	fs := callSources()
	pruneCalls(fs)
	out := make([]CallSource, 0, len(fs))
	for _, f := range fs {
		out = append(out, CallSource{f.path, f.key, f.agent, f.size, f.mod})
	}
	return out
}

// ReadCallSource returns immutable calls for one source. Independent files can
// be read concurrently; disk IO never holds callsMu.
func ReadCallSource(s CallSource) []Call {
	st := readCalls(file{path: s.Path, key: s.Key, agent: s.Agent, size: s.Size, mod: s.Modified})
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
