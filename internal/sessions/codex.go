package sessions

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Codex writes a rollout file per session (a session picked up again later
// may go on in another, named with the same thread id): a session_meta line,
// a turn_context per turn naming the model, and token_count events with the
// running total and the last call's usage. Its input counts include the
// cached tokens.

type cxLine struct {
	Type    string `json:"type"`
	Payload struct {
		cxCompaction
		cxHistorySnapshot
		Type        string   `json:"type"`
		SessionID   string   `json:"session_id"`
		TurnID      string   `json:"turn_id"`
		ResponseID  string   `json:"response_id"`
		Usage       *cxUsage `json:"usage"`
		ThreadUsage *cxUsage `json:"thread_token_usage"`
		ID          string   `json:"id"`
		Cwd         string   `json:"cwd"`
		Model       string   `json:"model"`
		Role        string   `json:"role"`
		Message     string   `json:"message"`
		Content     []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Settings struct {
			Model string `json:"model"`
			Cwd   string `json:"cwd"`
		} `json:"thread_settings"`
		Info *struct {
			Total *cxUsage `json:"total_token_usage"`
			Last  *cxUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type cxUsage struct {
	Input      int   `json:"input_tokens"`
	Cached     int   `json:"cached_input_tokens"`
	CacheWrite int   `json:"cache_write_input_tokens"`
	Output     int   `json:"output_tokens"`
	Reasoning  int   `json:"reasoning_output_tokens"`
	Known      uint8 `json:"known,omitempty"`
}

// raw is the usage in Codex's own terms, input with the cache in it.
func (u cxUsage) raw() Tokens {
	return Tokens{Input: u.Input, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
}

// spent is raw usage with the cache taken out of the input: Codex's
// input_tokens is the whole prompt, what was read from the cache and what
// was written to it (cache_write_input_tokens, which a newer Codex keeps)
// among it, so neither is counted twice (#589).
func spent(t Tokens) Tokens {
	t.Input -= t.CacheRead + t.CacheWrite
	if t.Input < 0 {
		t.Input = 0
	}
	return t
}

var (
	cxMeta      = []byte(`"type":"session_meta"`)
	cxTurn      = []byte(`"type":"turn_context"`)
	cxRecord    = []byte(`"type":"token_usage_record"`)
	cxCompacted = []byte(`"type":"compacted"`)
	cxCount     = []byte(`"type":"token_count"`)
	cxSettings  = []byte(`"type":"thread_settings_applied"`)
	cxUserMsg   = []byte(`"type":"user_message"`)
	cxUserRole  = []byte(`"role":"user"`)
	cxRole      = []byte(`"role":"`)
	cxType      = []byte(`"type":"`)
	cxPayload   = []byte(`"payload":{"type":"`)
	cxText      = []byte(`,"text":"`)
)

func codexLine(s *state, b []byte, main bool) {
	if codexHead(s, b, main) {
		codexBody(s, b, main)
	}
}

// codexHead notes the time of a line, from its start, and says whether the
// rest of it is worth reading: from its type and its payload's, which a
// rollout writes first, so irrelevant tool output is never searched.
// Compactions carry usage metadata. A line laid out otherwise is read.
func codexHead(s *state, b []byte, main bool) bool {
	at := tsAt(b[:min(len(b), 256)], false)
	if at.IsZero() {
		at = tsAt(b, false)
	}
	s.saw(at, main)
	h := b[:min(len(b), 1024)]
	top := typeAfter(h, cxType)
	if s.Codex != nil && top == "response_item" {
		payload := typeAfter(h, cxPayload)
		if payload == "message" && bytes.Contains(h, cxUserRole) {
			s.Codex.Pending = 0
		}
	}
	if s.Codex != nil && top == "event_msg" && typeAfter(h, cxPayload) == "user_message" {
		s.Codex.Pending = 0
	}
	switch top {
	case "":
		return true
	case "session_meta":
		return s.ID == ""
	case "turn_context", "token_usage_record", "compacted":
		return true
	case "event_msg", "response_item":
	default:
		return false
	}
	payload := typeAfter(h, cxPayload)
	switch {
	case payload == "":
		return true
	case top == "event_msg":
		return payload == "token_count" || payload == "task_started" || payload == "thread_settings_applied" || payload == "user_message" && s.Title == ""
	case cxCalls[payload]:
		// a tool call, named in its head
		name := typeAfter(h, cxName)
		if payload != "function_call" && payload != "custom_tool_call" || name == "" {
			name = payload
		}
		s.tool(at, name, "")
		return false
	case payload == "message" && typeAfter(h, cxRole) == "assistant":
		if main {
			s.day(dateOf(at)).Replies++
		}
		return false
	default:
		return payload == "message" && (main || s.Title == "") && (!bytes.Contains(h, cxRole) || bytes.Contains(h, cxUserRole))
	}
}

// cxCalls are the kinds of response item that call a tool.
var cxCalls = map[string]bool{"function_call": true, "custom_tool_call": true, "local_shell_call": true, "web_search_call": true, "tool_search_call": true, "image_generation_call": true}

var cxName = []byte(`"name":"`)

// typeAfter is the word in quotes after the first key in b, or "".
func typeAfter(b, key []byte) string {
	i := bytes.Index(b, key)
	if i < 0 {
		return ""
	}
	rest := b[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// Read compaction identity/usage without decoding its potentially multi-MB
// replacement history. The parser needs none of that conversation content.
func cxCompactionIn(b []byte) cxCompaction {
	fields := gjson.GetManyBytes(b, "payload.compaction_response_id", "payload.latest_token_usage_record")
	p := cxCompaction{ResponseID: fields[0].String()}
	if fields[1].Exists() {
		_ = json.Unmarshal([]byte(fields[1].Raw), &p.Record)
	}
	return p
}

// codexBody reads a line codexHead let through.
func codexBody(s *state, b []byte, main bool) {
	at := tsAt(b[:min(len(b), 256)], false)
	if at.IsZero() {
		at = tsAt(b, false)
	}
	if typeAfter(b[:min(len(b), 1024)], cxType) == "compacted" {
		codexApply(s, codexState(s).compacted(at, s.Model, cxCompactionIn(b)))
		return
	}
	// the two kinds of line most read, each told from its head
	if cxFastCount(s, at, b) || cxFastPrompt(s, at, b, main) {
		return
	}
	want := s.ID == "" && bytes.Contains(b, cxMeta) ||
		bytes.Contains(b, cxTurn) || bytes.Contains(b, cxCount) || bytes.Contains(b, cxRecord) || bytes.Contains(b, cxCompacted) || bytes.Contains(b, cxStarted) || bytes.Contains(b, cxSettings) ||
		s.Title == "" && bytes.Contains(b, cxUserMsg) || (main || s.Title == "") && bytes.Contains(b, cxUserRole)
	if !want {
		return
	}
	var l cxLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	p := &l.Payload
	switch {
	case l.Type == "compacted":
		codexApply(s, codexState(s).compacted(at, s.Model, p.cxCompaction))
	case l.Type == "token_usage_record":
		if p.Usage != nil {
			codexApply(s, codexState(s).record(codexContribution{ResponseID: p.ResponseID, Session: p.SessionID, Turn: p.TurnID, Model: s.Model, At: at, Usage: *p.Usage, Total: p.ThreadUsage}))
		}
	case l.Type == "session_meta":
		session := p.SessionID
		if session == "" {
			session = p.ID
		}
		codexState(s).context(session, "", "")
		codexState(s).beginSnapshot(at, p.ID, p.cxHistorySnapshot)
		if s.ID == "" {
			s.ID = p.ID
		}
		if s.Cwd == "" {
			s.Cwd = p.Cwd
		}
	case l.Type == "turn_context":
		codexState(s).context("", p.TurnID, p.Model)
		if p.Model != "" {
			s.Model = p.Model
		}
		if s.Cwd == "" {
			s.Cwd = p.Cwd
		}
	case l.Type == "event_msg" && p.Type == "task_started":
		codexState(s).context("", p.TurnID, "")
		codexState(s).Pending = 0
	case l.Type == "event_msg" && p.Type == "thread_settings_applied":
		if p.Settings.Model != "" {
			s.Model = p.Settings.Model
			codexState(s).context("", "", p.Settings.Model)
		}
	case l.Type == "event_msg" && p.Type == "user_message":
		if s.Title == "" {
			s.Title = cxPrompt(p.Message)
		}
		if s.Title == "" && s.First == "" {
			s.First = untagged(p.Message)
		}
	case l.Type == "response_item" && p.Type == "message" && p.Role == "user":
		typed := false
		for _, c := range p.Content {
			if c.Type == "input_text" || c.Type == "text" {
				t := cxPrompt(c.Text)
				typed = typed || t != ""
				if s.Title == "" {
					s.Title = t
				}
			}
		}
		if typed && main {
			s.day(dateOf(at)).Prompts++
		}
	case l.Type == "response_item" && p.Type == "message" && p.Role == "assistant":
		if main {
			s.day(dateOf(at)).Replies++
		}
	case l.Type == "event_msg" && p.Type == "token_count":
		if p.Info != nil {
			cxCounted(s, at, p.Info.Total, p.Info.Last)
		}
	}
}

// cxFastPrompt reads a prompt's texts out of a user message without decoding
// the images it may carry: a content part's type and text are written in
// that order, and the key can't be in a string, where its quotes would be
// escaped. A message laid out otherwise is left to be read whole.
func cxFastPrompt(s *state, at time.Time, b []byte, main bool) bool {
	h := b[:min(len(b), 1024)]
	if typeAfter(h, cxType) != "response_item" || typeAfter(h, cxPayload) != "message" || typeAfter(h, cxRole) != "user" {
		return false
	}
	found, typed := false, false
	for rest := b; ; {
		i := bytes.Index(rest, cxType)
		if i < 0 {
			if typed && main {
				s.day(dateOf(at)).Prompts++
			}
			// no text at all (an image alone) is read as such too
			return found || !bytes.Contains(b, cxText[1:])
		}
		rest = rest[i+len(cxType):]
		t := ""
		if j := bytes.IndexByte(rest, '"'); j >= 0 {
			t, rest = string(rest[:j]), rest[j+1:]
		}
		if t != "input_text" && t != "text" {
			continue
		}
		if !bytes.HasPrefix(rest, cxText) {
			return false
		}
		found = true
		text := strAt(rest[:len(cxText)+strEnd(rest[len(cxText):])], cxText)
		p := cxPrompt(text)
		typed = typed || p != ""
		if s.Title == "" {
			s.Title = p
		}
	}
}

// strEnd is the length of a JSON string's body and its closing quote.
func strEnd(b []byte) int {
	for j := 0; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(b)
}

// cxCounted uses the same interval reconciliation as the per-call reader.
func cxCounted(s *state, at time.Time, tot, last *cxUsage) {
	codexApply(s, codexState(s).count(at, s.Model, tot, last))
}

var (
	cxTotal = []byte(`"total_token_usage":{`)
	cxLast  = []byte(`"last_token_usage":{`)
	cxInfo  = []byte(`"info":null`)
)

// cxUsageIn reads the usage object after key in b, which holds no other
// object, without decoding the rest of the line.
func cxUsageIn(b, key []byte) (*cxUsage, bool) {
	i := bytes.Index(b, key)
	if i < 0 {
		return nil, false
	}
	obj := b[i+len(key)-1:]
	j := bytes.IndexByte(obj, '}')
	if j < 0 {
		return nil, false
	}
	var u cxUsage
	if json.Unmarshal(obj[:j+1], &u) != nil {
		return nil, false
	}
	return &u, true
}

// cxFastCount counts a token_count line from its usage objects alone; false
// when the line is laid out otherwise and wants decoding.
func cxFastCount(s *state, at time.Time, b []byte) bool {
	if h := b[:min(len(b), 1024)]; typeAfter(h, cxType) != "event_msg" || typeAfter(h, cxPayload) != "token_count" {
		return false
	}
	if bytes.Contains(b[:min(len(b), 1024)], cxInfo) {
		return true
	}
	tot, ok := cxUsageIn(b, cxTotal)
	if !ok {
		return false
	}
	last, _ := cxUsageIn(b, cxLast)
	cxCounted(s, at, tot, last)
	return true
}

// cxPrompt is the words of a prompt, or "" for what Codex put in itself
// (the environment, AGENTS.md, instructions).
func cxPrompt(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") {
		return ""
	}
	return title(t)
}
