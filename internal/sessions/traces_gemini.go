package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// Gemini moved from rewritten JSON snapshots to append-only JSONL metadata,
// messages, $set/$patch and $rewindTo records. Both stores use the same message
// schema. Message/tool timestamps are event boundaries, not measured latency.
func geminiTraceFiles() []file {
	home, _ := os.UserHomeDir()
	if custom := appdir.Getenv("GEMINI_CLI_HOME"); custom != "" {
		home = custom
	}
	root := filepath.Join(home, ".gemini", "tmp")
	var out []file
	for _, pattern := range []string{"*/chats/session-*.json", "*/chats/session-*.jsonl"} {
		paths, _ := SessionGlob(filepath.Join(root, pattern))
		for _, path := range paths {
			f := file{agent: "gemini", path: path, main: true}
			if stat(&f) {
				out = append(out, f)
			}
		}
	}
	return out
}

type geminiTraceTool struct {
	ID, Name, Status string
	Timestamp        time.Time
	Args, Result     json.RawMessage
}
type geminiTraceMessage struct {
	ID, Type, Model string
	Timestamp       time.Time
	Content         json.RawMessage
	ToolCalls       []geminiTraceTool
	Tokens          *struct{ Input, Output, Cached, Thoughts int }
	prompt          bool
}
type geminiTraceState struct {
	session  string
	order    []string
	messages map[string]*geminiTraceMessage
}

func (s *geminiTraceState) put(m geminiTraceMessage, bodies bool) {
	if m.ID == "" {
		return
	}
	// A recorded functionResponse is a tool-result turn, not a user prompt.
	m.prompt = m.Type == "user"
	var parts []struct{ FunctionResponse json.RawMessage }
	if json.Unmarshal(m.Content, &parts) == nil {
		for _, p := range parts {
			if len(p.FunctionResponse) > 0 {
				m.prompt = false
				break
			}
		}
	}
	if !bodies {
		m.Content = nil
		for i := range m.ToolCalls {
			m.ToolCalls[i].Args = nil
			m.ToolCalls[i].Result = nil
		}
	}
	if s.messages[m.ID] == nil {
		s.order = append(s.order, m.ID)
	}
	s.messages[m.ID] = &m
	// Keep the last user prompt plus a bounded window of recent messages.
	if len(s.order) > 256 {
		lastUser := ""
		for _, id := range s.order {
			if s.messages[id].prompt {
				lastUser = id
			}
		}
		keep := append([]string(nil), s.order[len(s.order)-255:]...)
		if lastUser != "" && !containsTraceID(keep, lastUser) {
			keep = append([]string{lastUser}, keep...)
		}
		retained := map[string]bool{}
		for _, id := range keep {
			retained[id] = true
		}
		for id := range s.messages {
			if !retained[id] {
				delete(s.messages, id)
			}
		}
		s.order = keep
	}
}
func containsTraceID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (s *geminiTraceState) line(b []byte, bodies bool) {
	var o struct {
		SessionID string
		Messages  []geminiTraceMessage
		Set       *struct {
			SessionID string
			Messages  []geminiTraceMessage
		} `json:"$set"`
		Rewind string `json:"$rewindTo"`
		Patch  *struct {
			ID        string
			Content   json.RawMessage
			ToolCalls []struct {
				ID     string
				Result json.RawMessage
			}
			Updates             []json.RawMessage
			RemoveIDs, OrderIDs []string
		} `json:"$patch"`
	}
	if json.Unmarshal(b, &o) != nil {
		return
	}
	if o.SessionID != "" {
		s.session = o.SessionID
	}
	for _, m := range o.Messages {
		s.put(m, bodies)
	}
	if o.Set != nil {
		if o.Set.SessionID != "" {
			s.session = o.Set.SessionID
		}
		if o.Set.Messages != nil {
			s.messages = map[string]*geminiTraceMessage{}
			s.order = nil
			for _, m := range o.Set.Messages {
				s.put(m, bodies)
			}
		}
	}
	if o.Rewind != "" {
		for i, id := range s.order {
			if id == o.Rewind {
				for _, remove := range s.order[i+1:] {
					delete(s.messages, remove)
				}
				s.order = s.order[:i+1]
				break
			}
		}
	}
	if p := o.Patch; p != nil {
		if m := s.messages[p.ID]; m != nil {
			if p.Content != nil && bodies {
				m.Content = p.Content
			}
			for _, tool := range p.ToolCalls {
				for i := range m.ToolCalls {
					if m.ToolCalls[i].ID == tool.ID && bodies {
						m.ToolCalls[i].Result = tool.Result
					}
				}
			}
		}
		for _, u := range p.Updates {
			wrapped, _ := json.Marshal(map[string]json.RawMessage{"$patch": u})
			s.line(wrapped, bodies)
		}
		for _, id := range p.RemoveIDs {
			delete(s.messages, id)
		}
		var keep []string
		for _, id := range s.order {
			if s.messages[id] != nil && !containsTraceID(p.OrderIDs, id) {
				keep = append(keep, id)
			}
		}
		for _, id := range p.OrderIDs {
			if s.messages[id] != nil {
				keep = append(keep, id)
			}
		}
		s.order = keep
	}
	var m geminiTraceMessage
	if json.Unmarshal(b, &m) == nil && m.ID != "" && m.Type != "" {
		s.put(m, bodies)
	}
}

func (r *TraceReader) readGemini(f file, bodies bool, budget *int64) []TraceSpan {
	snap := r.snapshot(f.path)
	cursor := r.files[f.path]
	if snap.gemini == nil {
		snap.gemini = &geminiTraceState{messages: map[string]*geminiTraceMessage{}}
	}
	h, err := os.Open(f.path)
	if err != nil {
		return nil
	}
	defer h.Close()
	info, err := h.Stat()
	if err != nil {
		return nil
	}
	if cursor.info != nil && (!os.SameFile(cursor.info, info) || f.size < cursor.offset) {
		cursor.offset = 0
		snap.gemini = &geminiTraceState{messages: map[string]*geminiTraceMessage{}}
	}
	cursor.info = info
	if strings.HasSuffix(f.path, ".json") {
		// Legacy files are atomically replaced; only parse complete snapshots.
		if f.size > 32<<20 || *budget <= 0 {
			return nil
		}
		b, err := os.ReadFile(f.path)
		if err != nil || !json.Valid(b) {
			return nil
		}
		*budget -= int64(len(b))
		snap.gemini = &geminiTraceState{messages: map[string]*geminiTraceMessage{}}
		snap.gemini.line(b, bodies)
		cursor.offset = f.size
	} else {
		if _, err = h.Seek(cursor.offset, 0); err != nil {
			return nil
		}
		reader := bufio.NewReader(h)
		for *budget > 0 {
			b, err := reader.ReadBytes('\n')
			if err != nil {
				break
			}
			cursor.offset += int64(len(b))
			*budget -= int64(len(b))
			snap.gemini.line(b, bodies)
		}
	}
	if cursor.offset == f.size {
		cursor.size, cursor.mod = f.size, f.mod
	}
	// Wait for the current file batch to settle: text, tokens and toolCalls can
	// be appended separately. Tool completions and earlier responses export now.
	settled := f.mod.Before(time.Now().Add(-2 * time.Second))
	snap.settled = settled
	return r.observations(snap, snap.gemini.spans(bodies, settled))
}

func (s *geminiTraceState) spans(bodies, settled bool) []TraceSpan {
	c := &traceCursor{agent: "gemini", session: s.session}
	var t *traceTurn
	var out []TraceSpan
	finish := func() {
		if t != nil {
			root := c.root(t, t.last, t.failed)
			root.Inferred = true
			out = append(out, root)
		}
	}
	for i, id := range s.order {
		m := s.messages[id]
		if m == nil {
			continue
		}
		if m.prompt {
			finish()
			t = &traceTurn{id: m.ID, start: m.Timestamp, last: m.Timestamp}
			if bodies {
				t.input = string(m.Content)
			}
			continue
		}
		if t == nil {
			continue
		}
		if m.Type == "error" {
			t.failed = true
			t.last = m.Timestamp
			continue
		}
		if m.Type != "gemini" {
			continue
		}
		next := i < len(s.order)-1
		if next || settled {
			span := c.span(t.id, m.ID, "model "+m.Model, "generation", t.last, m.Timestamp)
			span.Model, span.Provider, span.Inferred = m.Model, "google", true
			if u := m.Tokens; u != nil {
				span.Tokens = Tokens{Input: max(0, u.Input-u.Cached), Output: u.Output + u.Thoughts, CacheRead: u.Cached}
				span.Reasoning = u.Thoughts
			}
			if bodies {
				span.Input = t.input
				span.Output = string(m.Content)
				t.output = span.Output
			}
			out = append(out, span)
		}
		if m.Timestamp.After(t.last) {
			t.last = m.Timestamp
		}
		for _, tool := range m.ToolCalls {
			if tool.Status != "success" && tool.Status != "error" && tool.Status != "cancelled" {
				continue
			}
			// Gemini stamps tool state updates. Its result record supplies the end;
			// the owning model message supplies a conservative start boundary.
			span := c.span(t.id, m.ID+":"+tool.ID, tool.Name, "tool", m.Timestamp, tool.Timestamp)
			span.Inferred = true
			span.Error = tool.Status != "success"
			if bodies {
				span.Input = string(tool.Args)
				span.Output = geminiTraceResult(tool.Result, tool.ID, tool.Name)
			}
			out = append(out, span)
			if tool.Timestamp.After(t.last) {
				t.last = tool.Timestamp
			}
			t.failed = t.failed || span.Error
		}
	}
	finish()
	return out
}

// Some Gemini versions attach a whole parallel functionResponse turn to each
// tool. Select that tool's own response, rather than showing its siblings too.
func geminiTraceResult(raw json.RawMessage, id, name string) string {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var selected []json.RawMessage
	for _, part := range parts {
		var p struct{ FunctionResponse *struct{ ID, Name string } }
		if json.Unmarshal(part, &p) != nil {
			continue
		}
		if p.FunctionResponse == nil || p.FunctionResponse.ID == id || p.FunctionResponse.ID == "" && p.FunctionResponse.Name == name {
			selected = append(selected, part)
		}
	}
	if len(selected) == 0 {
		return ""
	}
	b, _ := json.Marshal(selected)
	return string(b)
}
