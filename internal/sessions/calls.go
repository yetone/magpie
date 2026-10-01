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
	"runtime"
	"sort"
	"strings"
	"time"
)

// Call is one model call as an agent's own session file records it.
type Call struct {
	Time    time.Time `json:"t"`
	Agent   string    `json:"agent"`   // claude (Claude Code's CLI), claude-desktop (Desktop's Code tab and Cowork), codex
	Session string    `json:"session"` // the id the agent sends the gateway as its session header
	Model   string    `json:"model"`   // the model the file names: for Claude, the one the vendor answered
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
	Total                             *cxCallUsage // the running total last seen
	Turns                             map[string]cxOpen
	LastIn, LastCall                  time.Time // the last thing the model was given, and the last call's end
	LastEnd                           int64     // where the last call's line ended
}

// cxOpen is a turn still open: how many calls it has made, and where the first is.
type cxOpen struct{ N, First int }

// cxCallUsage is Codex's usage with the reasoning tokens in it, which
// cxUsage leaves out.
type cxCallUsage struct {
	// These fields must be explicit: gob skips an unexported embedded cxUsage,
	// losing the previous cumulative counters when the append shard is reloaded.
	Input      int `json:"input_tokens"`
	Cached     int `json:"cached_input_tokens"`
	CacheWrite int `json:"cache_write_input_tokens"`
	Output     int `json:"output_tokens"`
	Reasoning  int `json:"reasoning_output_tokens"`
}

func (u cxCallUsage) raw() Tokens {
	return Tokens{Input: u.Input, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
}

// callDesktopDirs are Claude Desktop's data folders. Tests swap it.
var callDesktopDirs = desktopDataDirs

// DesktopDataDirs are the folders whose session metadata belongs to this index.
func DesktopDataDirs() []string { return callDesktopDirs() }

// Calls reads per-file request shards, separately from the session summaries.
// Only changed files are scanned, and only a bounded number of parses stay in
// memory. Conversation text is read by ContentOf when a detail is opened.
func Calls(since time.Time) []Call { return callsFor(since, "") }

func callsFor(since time.Time, session string) []Call {
	files := callFiles()
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
	return out
}

// desktopDataDirs are Claude Desktop's Claude and Claude-3p folders on this
// computer, found as desktopDirs in internal/agent's claudedesktop.go does
// (that package is not one to import from here).
func desktopDataDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		d := filepath.Join(home, "Library", "Application Support")
		return []string{filepath.Join(d, "Claude"), filepath.Join(d, "Claude-3p")}
	case "windows":
		d := os.Getenv("LOCALAPPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Local")
		}
		return []string{windowsClaudeDir(d, false), windowsClaudeDir(d, true)}
	}
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" || !filepath.IsAbs(d) {
		d = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(d, "Claude"), filepath.Join(d, "Claude-3p")}
}

// windowsClaudeDir is %LOCALAPPDATA%\Claude (or Claude-3p), else the first
// folder there named Claude… (with -3p in it or not).
func windowsClaudeDir(local string, threep bool) string {
	name := "Claude"
	if threep {
		name = "Claude-3p"
	}
	exact := filepath.Join(local, name)
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	ents, _ := os.ReadDir(local)
	var found []string
	for _, e := range ents {
		if n := e.Name(); e.IsDir() && strings.HasPrefix(n, "Claude") && strings.Contains(n, "-3p") == threep {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return exact
	}
	sort.Strings(found)
	return filepath.Join(local, found[0])
}

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
	head := headOf(f.path)
	var st *callFile
	if old != nil && f.size >= old.Size && old.Off <= f.size && sameHead(head, old.Head, old.HeadSize) && old.ContentHash != "" && prefixHash(f.path, old.Size) == old.ContentHash {
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
	c.Msgs, c.Began, c.Strs = maps.Clone(st.Msgs), maps.Clone(st.Began), maps.Clone(st.Strs)
	if st.CX != nil {
		r := *st.CX
		r.Turns = maps.Clone(r.Turns)
		if r.Total != nil {
			t := *r.Total
			r.Total = &t
		}
		c.CX = &r
	}
	return &c
}

// Codex tool results need only their timestamp, so long output/compaction
// lines can still be skipped by the session index's head reader.
func callHead(st *callFile, b []byte) bool {
	if st.Agent != "codex" {
		return true
	}
	if bytes.Contains(b, cxUserMsg) || bytes.Contains(b, cxUserRole) || bytes.Contains(b, cxToolOut) || bytes.Contains(b, cxCustomOut) {
		codexCallLine(st, b)
		return false
	}
	return bytes.Contains(b, cxMeta) || bytes.Contains(b, cxTurn) || bytes.Contains(b, cxCount) || bytes.Contains(b, cxSettings) || bytes.Contains(b, cxStarted) || bytes.Contains(b, cxDone)
}

// sessionOfPath is the session a Claude Code file belongs to by its name:
// <id>.jsonl, or <id>/subagents/<agent>.jsonl.
func sessionOfPath(p string) string {
	if d := filepath.Dir(p); filepath.Base(d) == "subagents" {
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
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
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
	// it was asked when the last user line or, with none since, the last reply ended
	asked, seen := st.LastUser, false
	if id := l.Message.ID; id != "" {
		if t, ok := st.Began[id]; ok {
			asked, seen = t, true
		}
	}
	if !seen && st.LastAsst.After(asked) {
		asked = st.LastAsst
	}
	// where its asking began: after the last reply's line, its own if it goes on
	c.File, c.From, c.To, c.Msg = st.Path, st.AsstEnd, st.End, l.Message.ID
	defer func() {
		if !at.IsZero() {
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
		c.Tokens = Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		if c.Tokens.zero() {
			return
		}
		if !asked.IsZero() && at.After(asked) {
			c.Millis = at.Sub(asked).Milliseconds()
		}
		if id := l.Message.ID; id != "" && !seen && !asked.IsZero() {
			if st.Began == nil {
				st.Began = map[string]time.Time{}
			}
			st.Began[id] = asked
		}
	}
	if id := l.Message.ID; id != "" {
		if i, ok := st.Msgs[id]; ok {
			c.From = st.Calls[i].From // its asking began with its first line
			st.Calls[i] = c
			if st.dirty != nil {
				st.dirty[i] = true
			}
			return
		}
		st.Msgs[id] = len(st.Calls)
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
		Type       string `json:"type"`
		ID         string `json:"id"`
		SessionID  string `json:"session_id"`
		Upstream   string `json:"model_provider"`
		AccountID  string `json:"creator_account_id"`
		UserID     string `json:"creator_user_id"`
		Cwd        string `json:"cwd"`
		Model      string `json:"model"`
		Effort     ccStr  `json:"effort"`
		TurnID     string `json:"turn_id"`
		Duration   int64  `json:"duration_ms"`
		FirstToken int64  `json:"time_to_first_token_ms"`
		Settings   struct {
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
	// what the model is given: the prompt, a tool's output
	if bytes.Contains(b, cxUserMsg) || bytes.Contains(b, cxUserRole) || bytes.Contains(b, cxToolOut) || bytes.Contains(b, cxCustomOut) {
		if at := tsAt(b, false); !at.IsZero() {
			r.LastIn = at
		}
		return
	}
	if !(!r.Meta && bytes.Contains(b, cxMeta) || bytes.Contains(b, cxTurn) || bytes.Contains(b, cxCount) ||
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
	case l.Type == "event_msg" && p.Type == "task_started":
		if p.TurnID != "" {
			r.Turn = p.TurnID
		}
		// a turn's first call was asked for as it began, whatever else was seen
		if at := tsAt(b, false); !at.IsZero() {
			r.LastIn = at
		}
	case l.Type == "event_msg" && p.Type == "thread_settings_applied":
		if p.Settings.Upstream != "" {
			r.Upstream = st.str(p.Settings.Upstream)
		}
		if p.Settings.Model != "" {
			r.Model = st.str(p.Settings.Model)
		}
	case l.Type == "event_msg" && p.Type == "task_complete":
		if t, ok := r.Turns[p.TurnID]; ok && t.N == 1 {
			st.Calls[t.First].TTFT, st.Calls[t.First].Millis = p.FirstToken, p.Duration
			if st.dirty != nil {
				st.dirty[t.First] = true
			}
		}
		delete(r.Turns, p.TurnID)
	case l.Type == "event_msg" && p.Type == "token_count":
		if p.Info == nil || p.Info.Total == nil {
			return
		}
		// the same totals as codexLine tells a call by
		total := *p.Info.Total
		var d Tokens
		var reasoning int
		switch prev := r.Total; {
		case prev != nil && total.Input >= prev.Input && total.Output >= prev.Output && total.Cached >= prev.Cached:
			// the same total told again adds nothing
			d = total.raw()
			d.sub(prev.raw())
			d.CacheWrite = max(0, d.CacheWrite)
			reasoning = max(0, total.Reasoning-prev.Reasoning)
		case p.Info.Last != nil:
			// the first count in the file, or a total that started over
			d, reasoning = p.Info.Last.raw(), p.Info.Last.Reasoning
		default:
			d, reasoning = total.raw(), total.Reasoning
		}
		r.Total = &total
		t := spent(d)
		at := tsAt(b, false)
		if at.IsZero() || t.zero() && reasoning == 0 {
			return
		}
		if r.Turn != "" {
			if r.Turns == nil {
				r.Turns = map[string]cxOpen{}
			}
			turn := r.Turns[r.Turn]
			if turn.N++; turn.N == 1 {
				turn.First = len(st.Calls)
			}
			r.Turns[r.Turn] = turn
		}
		// it took from what asked for it, or the call before it, to here
		asked := r.LastIn
		if r.LastCall.After(asked) {
			asked = r.LastCall
		}
		var took int64
		if !asked.IsZero() && at.After(asked) && at.Sub(asked) < 2*time.Hour {
			took = at.Sub(asked).Milliseconds()
		}
		r.LastCall = at
		st.Calls = append(st.Calls, Call{Time: at, Agent: "codex", Session: r.Session, Model: r.Model, Tokens: t,
			Upstream: r.Upstream, AccountID: r.AccountID, UserID: r.UserID,
			Reasoning: reasoning, Effort: r.Effort, Cwd: r.Cwd, Millis: took, File: st.Path, From: r.LastEnd, To: st.End})
		r.LastEnd = st.End
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
	fs := callFiles()
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
	if callCache[s.Path] == st {
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
