package sessions

// Agent trace readers are separate from the usage/listing cache: they retain
// just an incremental cursor and the active interaction, never session titles
// or completed conversations. They don't send data themselves.
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TraceSpan is a recorded agent operation, model response, or tool execution.
type TraceSpan struct {
	Agent, Session, Turn, ID, Parent, Name, Kind, Model, Provider, Served string
	Start, End                                                            time.Time
	Input, Output                                                         string
	Tokens                                                                Tokens
	Reasoning                                                             int
	Error, Inferred                                                       bool
	Update                                                                bool
	Pending                                                               bool // ancestry only; export the root when the interaction finishes
}

type traceTurn struct {
	id                   string
	start, last          time.Time
	input, output, model string
	failed, completed    bool
	finishAfter          time.Time
}

// Allow a timing extension to append its final event across reader polls.
// Plain Pi still finishes on the next regular (two-second) poll.
const piTimingGrace = time.Second

type traceCursor struct {
	info                     os.FileInfo
	offset                   int64
	size                     int64
	mod                      time.Time
	agent, session, provider string
	scope                    string
	turns                    map[string]*traceTurn
	current                  string
	tools                    map[string]TraceSpan
	parents                  map[string]string
	claudeMessage            *claudeTraceMessage
}

// TraceReader follows supported local agent stores modified since activation.
// Existing lines establish ancestry, but events older than Since aren't emitted.
// Deterministic IDs make resumed rollouts and copied session branches idempotent.
type TraceReader struct {
	visible    []TraceSession
	identities *TraceSessionIndex
	Since      time.Time
	files      map[string]*traceCursor
	snapshots  map[string]*traceSnapshot
	ocStamp    [4]int64
	ocPending  bool
	ocInfo     os.FileInfo
	ocNext     int
}

func NewTraceReader(since time.Time) *TraceReader {
	return &TraceReader{Since: since, identities: new(TraceSessionIndex), files: map[string]*traceCursor{}, snapshots: map[string]*traceSnapshot{}}
}

// TraceAgent lists formats with an interaction and tool-aware trace adapter.
func TraceAgent(agent string) bool {
	switch agent {
	case "codex", "pi", "omp", "claude", "claude-desktop", "opencode", "gemini":
		return true
	}
	return false
}

// Claude child transcripts are discovered alongside their parent stores.
func traceLineFiles() []file {
	files := append(codexFiles(), piFiles()...)
	claude := func(agent, dir string) { files = append(files, ccFiles(agent, dir)...) }
	claude("claude", ClaudeDir())
	for _, dir := range callDesktopDirs() {
		homes, _ := SessionGlob(filepath.Join(dir, "local-agent-mode-sessions", "*", "*", "local_*", ".claude"))
		for _, home := range homes {
			claude("claude-desktop", home)
		}
	}
	seen := map[string]bool{}
	for _, root := range ompSessionRoots() {
		paths, _ := SessionGlob(filepath.Join(root, "*", "*.jsonl"))
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true
			f := file{agent: "omp", path: path, main: true}
			if stat(&f) {
				files = append(files, f)
			}
		}
	}
	return append(files, geminiTraceFiles()...)
}

func TraceID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}
func traceSpanID(parts ...string) string { return TraceID(parts...)[:16] }
func traceTime(raw json.RawMessage, millis bool) time.Time {
	var n int64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		if millis {
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	return time.Time{}
}
func (c *traceCursor) span(turn, id, name, kind string, start, end time.Time) TraceSpan {
	return TraceSpan{Agent: c.agent, Session: c.session, Turn: turn, ID: traceSpanID(c.agent, c.session, turn, id), Parent: traceSpanID(c.agent, c.session, turn, "root"), Name: name, Kind: kind, Start: start, End: end}
}
func (c *traceCursor) root(t *traceTurn, end time.Time, failed bool) TraceSpan {
	s := c.span(t.id, "root", c.agent+" interaction", "span", t.start, end)
	s.Parent = ""
	s.Input = t.input
	s.Output = t.output
	s.Error = failed
	return s
}
func (c *traceCursor) turn(id string, at time.Time) *traceTurn {
	t := c.turns[id]
	if t == nil {
		if len(c.turns) >= 32 {
			var oldest string
			var at time.Time
			for key, turn := range c.turns {
				if oldest == "" || turn.last.Before(at) {
					oldest, at = key, turn.last
				}
			}
			delete(c.turns, oldest)
		}
		t = &traceTurn{id: id, start: at, last: at}
		c.turns[id] = t
	}
	return t
}

// Poll reads appended complete lines only. It limits watched files to the most
// recent sessions; a partial last line remains for the following poll.
func (r *TraceReader) Poll(bodies bool) []TraceSpan {
	files := traceRecentFiles(traceLineFiles())
	r.visible = r.identities.poll(files)
	var result []TraceSpan
	budget := int64(8 << 20)
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.path] = true
		// Filesystem timestamps can be coarse or lag the write (Linux VFS).
		// Use mtime only to prune clearly old stores; event timestamps below
		// still enforce the exact activation boundary and prevent history replay.
		if budget <= 0 || strings.HasSuffix(f.path, zstSuffix) || f.mod.Before(r.Since.Add(-2*time.Second)) {
			continue
		}
		c := r.files[f.path]
		if c == nil || f.size < c.offset {
			c = newTraceCursor(f)
			r.files[f.path] = c
		}
		if c.size == f.size && c.mod.Equal(f.mod) {
			for _, s := range c.finishPi(time.Now()) {
				if !s.End.Before(r.Since) {
					result = append(result, s)
				}
			}
			if f.mod.Before(time.Now().Add(-2 * time.Second)) {
				if f.agent == "gemini" {
					snap := r.snapshot(f.path)
					if snap.gemini != nil && !snap.settled {
						result = append(result, r.observations(snap, snap.gemini.spans(bodies, true))...)
						snap.settled = true
					}
				}
				for _, s := range c.finishClaude(false) {
					if !s.End.Before(r.Since) {
						result = append(result, s)
					}
				}
			}
			continue
		}
		if f.agent == "gemini" {
			result = append(result, r.readGemini(f, bodies, &budget)...)
			continue
		}
		h, err := os.Open(f.path)
		if err != nil {
			continue
		}
		info, err := h.Stat()
		if err != nil {
			h.Close()
			continue
		}
		if c.info != nil && !os.SameFile(c.info, info) {
			c = newTraceCursor(f)
			r.files[f.path] = c
		}
		c.info = info
		if _, err = h.Seek(c.offset, io.SeekStart); err != nil {
			h.Close()
			continue
		}
		b := bufio.NewReader(h)
		// A large file is processed in bounded slices so one session cannot monopolize polling.
		read := int64(0)
		for read < budget {
			line, err := b.ReadBytes('\n')
			if err != nil {
				break
			}
			c.offset += int64(len(line))
			read += int64(len(line))
			spans := c.line(line, bodies)
			for _, s := range spans {
				if !s.End.Before(r.Since) && !s.Start.IsZero() && !s.End.Before(s.Start) && s.Session != "" {
					result = append(result, s)
				}
			}
		}
		h.Close()
		budget -= read
		if c.offset == f.size {
			// Prefer timing-final, allowing a bounded wait across polls when
			// the extension has not appended it yet.
			for _, s := range c.finishPi(time.Now()) {
				if !s.End.Before(r.Since) {
					result = append(result, s)
				}
			}
			c.size, c.mod = f.size, f.mod
		}
	}
	for path := range r.files {
		if !seen[path] {
			delete(r.files, path)
		}
	}
	result = append(result, r.pollOpenCode(bodies, &budget)...)
	for path := range r.snapshots {
		if !seen[path] && !strings.HasPrefix(path, "opencode:") {
			delete(r.snapshots, path)
		}
	}
	return result
}
func newTraceCursor(f file) *traceCursor {
	c := &traceCursor{agent: f.agent, turns: map[string]*traceTurn{}, tools: map[string]TraceSpan{}, parents: map[string]string{}}
	if f.agent == "claude" || f.agent == "claude-desktop" {
		c.session = sessionOfPath(f.path)
		if !f.main {
			c.scope = strings.TrimSuffix(filepath.Base(f.path), ".jsonl")
		}
	}
	return c
}

func (c *traceCursor) line(line []byte, bodies bool) []TraceSpan {
	if c.agent == "pi" || c.agent == "omp" {
		return c.pi(line, bodies)
	}
	if c.agent == "claude" || c.agent == "claude-desktop" {
		return c.claude(line, bodies)
	}
	return c.codex(line, bodies)
}

// A new turn is evidence that the previous one stopped, even if Codex was
// killed before recording task_complete or turn_aborted. Do not infer this
// from inactivity: a model or tool can legitimately take a long time.
func (c *traceCursor) interruptCodexTurn(next string, at time.Time) []TraceSpan {
	t := c.turns[c.current]
	if t == nil || c.current == next {
		return nil
	}
	end := at
	if t.last.After(end) {
		end = t.last
	}
	root := c.root(t, end, true)
	root.Inferred = true
	spans := []TraceSpan{root}
	for key, call := range c.tools {
		if call.Turn == t.id {
			call.End, call.Error = end, true
			spans = append(spans, call)
			delete(c.tools, key)
		}
	}
	delete(c.turns, t.id)
	return spans
}

func (c *traceCursor) codex(line []byte, bodies bool) []TraceSpan {
	var o struct {
		Type      string
		Timestamp time.Time
		Payload   json.RawMessage
	}
	if json.Unmarshal(line, &o) != nil {
		return nil
	}
	var p struct {
		Type, ID, Model          string
		Name                     string
		CallID                   string `json:"call_id"`
		Input, Arguments, Output json.RawMessage
		TurnID                   string          `json:"turn_id"`
		ResponseID               string          `json:"response_id"`
		ModelProvider            string          `json:"model_provider"`
		StartedAt                json.RawMessage `json:"started_at"`
		CompletedAt              json.RawMessage `json:"completed_at"`
		StartedAtMS              json.RawMessage `json:"started_at_ms"`
		CompletedAtMS            json.RawMessage `json:"completed_at_ms"`
		DurationMS               int64           `json:"duration_ms"`
		LastAgentMessage         string          `json:"last_agent_message"`
		Usage                    struct {
			Input      int `json:"input_tokens"`
			Cached     int `json:"cached_input_tokens"`
			CacheWrite int `json:"cache_write_input_tokens"`
			Output     int `json:"output_tokens"`
			Reasoning  int `json:"reasoning_output_tokens"`
		}
		Item struct {
			Type, ID, Tool, Server, Status string
			Command                        json.RawMessage
			Arguments                      json.RawMessage
			Content                        json.RawMessage
			Output                         json.RawMessage
			Result                         json.RawMessage
			AggregatedOutput               string `json:"aggregated_output"`
			ExitCode                       *int   `json:"exit_code"`
		}
	}
	if o.Type != "session_meta" && o.Type != "turn_context" && o.Type != "event_msg" && o.Type != "token_usage_record" && o.Type != "response_item" {
		return nil
	}
	if json.Unmarshal(o.Payload, &p) != nil {
		return nil
	}
	if o.Type == "session_meta" {
		c.session = p.ID
		c.provider = p.ModelProvider
		return nil
	}
	if o.Type == "turn_context" {
		if p.TurnID == "" {
			return nil
		}
		interrupted := c.interruptCodexTurn(p.TurnID, o.Timestamp)
		c.current = p.TurnID
		t := c.turn(p.TurnID, o.Timestamp)
		t.model = p.Model
		return interrupted
	}
	id := p.TurnID
	if id == "" {
		id = c.current
	}
	if id == "" {
		return nil
	}
	t := c.turn(id, o.Timestamp)
	switch {
	case o.Type == "response_item":
		switch p.Type {
		case "custom_tool_call", "function_call":
			if p.CallID == "" || len(c.tools) >= 256 {
				return nil
			}
			s := c.span(id, p.CallID, p.Name, "tool", o.Timestamp, o.Timestamp)
			s.Inferred = true
			if bodies {
				s.Input = string(p.Arguments)
				if s.Input == "" {
					s.Input = string(p.Input)
				}
			}
			c.tools[p.CallID] = s
		case "custom_tool_call_output", "function_call_output":
			s, ok := c.tools[p.CallID]
			if !ok {
				return nil
			}
			delete(c.tools, p.CallID)
			s.End = o.Timestamp
			if bodies {
				s.Output = string(p.Output)
			}
			if s.End.After(t.last) {
				t.last = s.End
			}
			return []TraceSpan{s}
		}
	case o.Type == "event_msg" && p.Type == "task_started":
		interrupted := c.interruptCodexTurn(id, o.Timestamp)
		c.current = id
		at := traceTime(p.StartedAt, false)
		if !at.IsZero() {
			t.start, t.last = at, at
		}
		root := c.root(t, t.start, false)
		root.Pending = true
		return append(interrupted, root)
	case o.Type == "event_msg" && p.Type == "item_completed":
		start, end := traceTime(p.StartedAtMS, true), traceTime(p.CompletedAtMS, true)
		if start.IsZero() {
			start = o.Timestamp
		}
		if end.IsZero() {
			end = o.Timestamp
		}
		switch p.Item.Type {
		case "UserMessage":
			if bodies {
				t.input = string(p.Item.Content)
			}
			return nil
		case "AgentMessage":
			if bodies {
				t.output = string(p.Item.Content)
			}
			return nil
		case "CommandExecution", "McpToolCall", "FileChange", "ImageView", "ContextCompaction":
			// A recorded MCP completion is authoritative over the matching
			// inferred function_call/output pair, which uses the same call ID.
			if p.Item.Type == "McpToolCall" {
				delete(c.tools, p.Item.ID)
			}
			name := map[string]string{"CommandExecution": "command", "McpToolCall": "mcp tool", "FileChange": "file change", "ImageView": "view image", "ContextCompaction": "compaction"}[p.Item.Type]
			if p.Item.Tool != "" {
				name = p.Item.Tool
			}
			s := c.span(id, p.Item.ID, name, "tool", start, end)
			s.Error = p.Item.Status == "failed" || p.Item.Status == "error" || p.Item.ExitCode != nil && *p.Item.ExitCode != 0
			if bodies {
				s.Input = string(p.Item.Arguments)
				if s.Input == "" {
					s.Input = string(p.Item.Command)
				}
				s.Output = p.Item.AggregatedOutput
				if s.Output == "" {
					s.Output = string(p.Item.Output)
				}
				if s.Output == "" {
					s.Output = string(p.Item.Result)
				}
			}
			// exec may run commands which also have recorded completion events.
			// Keep those operations beneath the enclosing exec, rather than
			// showing two independent executions of the same command.
			if p.Item.Type == "CommandExecution" {
				var parent string
				for _, call := range c.tools {
					if call.Turn == id && call.Name == "exec" {
						if parent != "" {
							parent = ""
							break
						}
						parent = call.ID
					}
				}
				if parent != "" {
					s.Parent = parent
				}
			}
			if end.After(t.last) {
				t.last = end
			}
			return []TraceSpan{s}
		}
	case o.Type == "token_usage_record":
		if p.ResponseID == "" {
			return nil
		}
		s := c.span(id, p.ResponseID, "model "+t.model, "generation", t.last, o.Timestamp)
		s.Model = t.model
		s.Provider = c.provider
		if s.Provider == "" {
			s.Provider = "openai"
		}
		s.Inferred = true
		s.Tokens = Tokens{Input: max(0, p.Usage.Input-p.Usage.Cached-p.Usage.CacheWrite), CacheRead: p.Usage.Cached, CacheWrite: p.Usage.CacheWrite, Output: p.Usage.Output}
		s.Reasoning = p.Usage.Reasoning
		if bodies {
			s.Output = t.output
			s.Input = t.input
		}
		t.last = o.Timestamp
		return []TraceSpan{s}
	case o.Type == "event_msg" && (p.Type == "task_complete" || p.Type == "turn_aborted"):
		end := o.Timestamp
		if end.IsZero() {
			end = traceTime(p.CompletedAt, false)
		}
		if p.DurationMS > 0 {
			t.start = end.Add(-time.Duration(p.DurationMS) * time.Millisecond)
		}
		if bodies && p.LastAgentMessage != "" {
			t.output = p.LastAgentMessage
		}
		s := c.root(t, end, p.Type == "turn_aborted")
		delete(c.turns, id)
		var unfinished []TraceSpan
		for key, call := range c.tools {
			if call.Turn == id {
				call.End, call.Error = end, true
				unfinished = append(unfinished, call)
				delete(c.tools, key)
			}
		}
		return append(unfinished, s)
	}
	return nil
}

func (c *traceCursor) pi(line []byte, bodies bool) []TraceSpan {
	var o struct {
		Type, ID, ParentID, CustomType string
		Timestamp                      time.Time
		Message                        struct {
			Role, ToolCallID, ToolName, Model, Provider, ResponseID, ResponseModel, StopReason string
			Timestamp                                                                          int64
			IsError                                                                            bool
			Content                                                                            json.RawMessage
			Usage                                                                              piUsage
		}
		Data struct{ EndAt, TotalMS int64 }
	}
	if json.Unmarshal(line, &o) != nil {
		return nil
	}
	if o.Type == "session" {
		c.session = o.ID
		return nil
	}
	m := o.Message
	if o.Type == "custom" && o.CustomType == "timing-final" {
		id := c.parents[o.ParentID]
		if id == "" {
			id = c.current
		}
		if t := c.turns[id]; t != nil {
			end := time.UnixMilli(o.Data.EndAt).UTC()
			if o.Data.EndAt <= 0 || end.Before(t.last) || o.Data.TotalMS < 0 {
				return nil
			}
			if o.Data.TotalMS > 0 {
				t.start = end.Add(-time.Duration(o.Data.TotalMS) * time.Millisecond)
			}
			s := c.root(t, end, t.failed)
			delete(c.turns, id)
			return []TraceSpan{s}
		}
		return nil
	}
	if o.Type != "message" {
		if id := c.parents[o.ParentID]; id != "" {
			c.parents[o.ID] = id
			if c.agent == "omp" && o.Type == "model_usage" {
				var usage struct {
					Model, Provider, Purpose string
					Usage                    piUsage
				}
				if json.Unmarshal(line, &usage) == nil {
					s := c.span(id, o.ID, "model "+usage.Model+" ("+usage.Purpose+")", "generation", o.Timestamp, o.Timestamp)
					s.Model, s.Provider, s.Inferred = usage.Model, usage.Provider, true
					s.Tokens = Tokens{usage.Usage.Input, usage.Usage.Output, usage.Usage.CacheRead, usage.Usage.CacheWrite}
					return []TraceSpan{s}
				}
			}
		}
		return nil
	}
	if m.Role == "user" {
		var interrupted []TraceSpan
		if previous := c.turns[c.current]; previous != nil {
			if !previous.completed && c.current != o.ID {
				root := c.root(previous, o.Timestamp, true)
				root.Inferred = true
				interrupted = append(interrupted, root)
				for key, tool := range c.tools {
					if tool.Turn == previous.id {
						tool.End = o.Timestamp
						tool.Error = true
						interrupted = append(interrupted, tool)
						delete(c.tools, key)
					}
				}
			}
			if previous.completed {
				interrupted = append(interrupted, c.root(previous, previous.last, previous.failed))
			}
			delete(c.turns, c.current)
		}
		c.current = o.ID
		c.parents[o.ID] = o.ID
		t := c.turn(o.ID, o.Timestamp)
		if bodies {
			t.input = string(m.Content)
		}
		root := c.root(t, t.start, false)
		root.Pending = true
		return append(interrupted, root)
	}
	id := c.parents[o.ParentID]
	if id == "" {
		id = c.current
	}
	if id == "" {
		return nil
	}
	c.parents[o.ID] = id
	if len(c.parents) > 4096 {
		c.parents = map[string]string{o.ID: id}
	}
	t := c.turn(id, o.Timestamp)
	if m.Role == "toolResult" {
		s, ok := c.tools[m.ToolCallID]
		if !ok {
			return nil
		}
		delete(c.tools, m.ToolCallID)
		s.End = o.Timestamp
		s.Error = m.IsError
		if bodies {
			s.Output = string(m.Content)
		}
		t.last = o.Timestamp
		return []TraceSpan{s}
	}
	if m.Role != "assistant" {
		return nil
	}
	start := time.UnixMilli(m.Timestamp).UTC()
	if m.Timestamp == 0 {
		start = t.last
	}
	name := "model " + m.Model
	key := m.ResponseID
	if key == "" {
		key = o.ID
	}
	s := c.span(id, key, name, "generation", start, o.Timestamp)
	s.Model = m.Model
	s.Provider = m.Provider
	s.Served = m.ResponseModel
	s.Tokens = Tokens{Input: m.Usage.Input, Output: m.Usage.Output, CacheRead: m.Usage.CacheRead, CacheWrite: m.Usage.CacheWrite}
	s.Error = m.StopReason == "error" || m.StopReason == "aborted"
	t.failed = s.Error
	var content []struct {
		Type, ID, Name string
		Arguments      json.RawMessage
	}
	json.Unmarshal(m.Content, &content)
	toolCalls := 0
	for _, part := range content {
		if part.Type == "toolCall" {
			toolCalls++
			if len(c.tools) >= 256 {
				continue
			}
			tool := c.span(id, part.ID, part.Name, "tool", o.Timestamp, o.Timestamp)
			tool.Inferred = true
			if bodies {
				tool.Input = string(part.Arguments)
			}
			c.tools[part.ID] = tool
		}
	}
	if bodies {
		s.Output = string(m.Content)
		s.Input = t.input
	}
	t.last = o.Timestamp
	spans := []TraceSpan{s}
	if toolCalls == 0 && m.StopReason != "toolUse" {
		t.completed = true
		t.finishAfter = time.Now().Add(piTimingGrace)
		if bodies {
			t.output = string(m.Content)
		}
	}
	return spans
}

func (c *traceCursor) finishPi(now time.Time) []TraceSpan {
	if c.agent != "pi" && c.agent != "omp" {
		return nil
	}
	var spans []TraceSpan
	for id, t := range c.turns {
		if t.completed && !now.Before(t.finishAfter) {
			spans = append(spans, c.root(t, t.last, t.failed))
			delete(c.turns, id)
		}
	}
	return spans
}
