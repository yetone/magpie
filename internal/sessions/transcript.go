package sessions

// A session's whole conversation, read from the agent's own file when it is
// asked for: what was said, in order, by the user, the model and its tools.
// The file is only read; magpie keeps no copy.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	"github.com/tidwall/gjson"
)

// Transcript is what was said in a session, in the order it was said.
type Transcript struct {
	Parts    []Part `json:"parts"`
	Cut      bool   `json:"cut,omitempty"` // there was more than is kept here
	Source   string `json:"source,omitempty"`
	Captured int    `json:"captured,omitempty"`
}

// transcriptMax is the characters of all of a transcript's parts.
const transcriptMax = 400_000

// ErrNoTranscript is a session whose conversation magpie can't read: an
// agent that keeps it in a database, or in a form not read here.
var ErrNoTranscript = errors.New("this agent's conversations can't be shown")

// HasTranscript says whether an agent's sessions can be read as a
// transcript.
func HasTranscript(agent string) bool {
	switch agent {
	case "claude", "qoder", "qoder-cn", "codex", "pi", "raft", "cindy", "omp", "cursor", "opencode", "kimi", "agy", "openmausbot":
		return true
	}
	return false
}

// readable says whether a session's conversation can be read: its agent's
// can, and its file is the one they are read from (an agy conversation
// listed from its row alone has none).
func readable(s Session) bool {
	return HasTranscript(s.Agent) && !(s.Agent == "agy" && agyRowOnly(s.Path))
}

func (t *Transcript) add(_ bool, p Part) bool {
	size := 0
	for _, x := range t.Parts {
		size += len(x.Text)
	}
	if size >= transcriptMax {
		t.Cut = true
		return false
	}
	if p, ok := keep(p); ok {
		t.Parts = append(t.Parts, p)
	}
	return true
}

// TranscriptOf reads a session's conversation from its (main) file.
func TranscriptOf(s Session) (Transcript, error) {
	if !Reads(s.Agent) {
		return Transcript{Parts: []Part{}}, ErrNotRead
	}
	out := Transcript{Parts: []Part{}}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return out, ErrNoTranscript
	}
	return out, readTranscript(s, out.add)
}

// readTranscript tells add each part of a session's conversation, in order,
// as its agent's file has it.
func readTranscript(s Session, add func(bool, Part) bool) error {
	return readTranscriptOf(s, nil, add)
}

// readTranscriptOf is readTranscript decoding only the lines keep says yes
// to, where a line stands alone (Claude's, Codex's, Kimi's); Pi's lines are a tree,
// read whole.
func readTranscriptOf(s Session, keep func([]byte) bool, add func(bool, Part) bool) error {
	whole := Call{Agent: s.Agent, File: s.Path, To: math.MaxInt64}
	switch s.Agent {
	case "opencode":
		return openCodeTranscript(s, add)
	case "cursor":
		return cursorTranscript(s.Path, add)
	case "codex":
		return codexContentOf(whole, keep, add)
	case "pi", "raft", "cindy", "omp":
		return piTranscript(s.Path, add)
	case "kimi":
		return kimiTranscript(s.Path, keep, add)
	case "agy":
		return agyTranscript(s.Path, keep, add)
	case "openmausbot":
		return ombTranscript(s, add)
	default:
		if s.BranchOf != "" && keep == nil {
			return branchTranscript(s, add)
		}
		return claudeContentOf(whole, keep, add)
	}
}

// branchTranscript reads a branch (ccFamily) as its conversation went: the
// session it is a branch of from its start up to the last message the two
// share, then what the branch said on its own. A fork made after a
// compaction holds only the summary and what follows it, and opened on
// that summary and one turn (Neo, #feedback). A search reads the branch's
// own file only, so a hit in the shared part is the head's, not each
// branch's again.
func branchTranscript(s Session, add func(bool, Part) bool) error {
	ends := map[string]int64{}
	if _, err := scanThin(s.BranchOf, 0, nil, thinClaude, func(b []byte, _, end int64) bool {
		if u := ccMessageUUID(b); u != "" {
			ends[u] = end
		}
		return true
	}); err != nil {
		// the head unread: the branch alone, as before it was known to be one
		return claudeContentOf(Call{Agent: s.Agent, File: s.Path, To: math.MaxInt64}, nil, add)
	}
	fork := int64(-1)
	if _, err := scanThin(s.Path, 0, nil, thinClaude, func(b []byte, _, _ int64) bool {
		if e, ok := ends[ccMessageUUID(b)]; ok && e > fork {
			fork = e
		}
		return true
	}); err != nil {
		return err
	}
	if fork < 0 {
		return claudeContentOf(Call{Agent: s.Agent, File: s.Path, To: math.MaxInt64}, nil, add)
	}
	more := true
	if err := claudeContentOf(Call{Agent: s.Agent, File: s.BranchOf, To: fork}, nil, func(in bool, p Part) bool {
		more = add(in, p) && more
		return more
	}); err != nil || !more {
		return err
	}
	return claudeContentOf(Call{Agent: s.Agent, File: s.Path, To: math.MaxInt64}, func(b []byte) bool {
		_, shared := ends[ccMessageUUID(b)]
		return !shared
	}, add)
}

// ccMessageUUID is the uuid of a Claude-format user or assistant line, ""
// for any other line.
func ccMessageUUID(b []byte) string {
	if !bytes.Contains(b, ccUser) && !bytes.Contains(b, ccAssistant) {
		return ""
	}
	r := gjson.GetManyBytes(b, "type", "uuid")
	if t := r[0].String(); (t == "user" || t == "assistant") && r[1].Type == gjson.String {
		return r[1].String()
	}
	return ""
}

// piEntry is a line of a Pi (or oh-my-pi) session: an entry of its tree.
type piEntry struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Summary  string `json:"summary"` // compaction, branch_summary
	Message  *struct {
		Role     string          `json:"role"`
		ToolName string          `json:"toolName"`
		Model    string          `json:"model"` // an assistant's: the one it was answered by (#2253)
		Content  json.RawMessage `json:"content"`
	} `json:"message"`
}

var piMessage = []byte(`"type":"message"`)

// piTranscript reads the branch the session is on: a Pi session is a tree,
// each entry naming its parent, and the last entry written is where it
// stands (an earlier branch, left by /tree or a retry, isn't said again).
func piTranscript(path string, add func(bool, Part) bool) error {
	byID := map[string]piEntry{}
	last := ""
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		var e piEntry
		if json.Unmarshal(b, &e) != nil || e.ID == "" || e.Type == "session" || e.Type == "title" {
			return true
		}
		if !bytes.Contains(b, piMessage) && e.Type != "compaction" && e.Type != "branch_summary" {
			// keep the tree whole without holding what isn't shown
			e = piEntry{Type: e.Type, ID: e.ID, ParentID: e.ParentID}
		}
		byID[e.ID], last = e, e.ID
		return true
	})
	if err != nil {
		return err
	}
	var branch []piEntry
	for id, n := last, 0; id != "" && n <= len(byID); n++ {
		e, ok := byID[id]
		if !ok {
			break
		}
		branch = append(branch, e)
		id = e.ParentID
	}
	for i := len(branch) - 1; i >= 0; i-- {
		for _, p := range piParts(branch[i]) {
			if p.Role == "assistant" && branch[i].Message != nil {
				p.Model = branch[i].Message.Model
			}
			if !add(p.Role != "assistant", p) {
				return nil
			}
		}
	}
	return nil
}

// piParts are what an entry said.
func piParts(e piEntry) []Part {
	switch e.Type {
	case "compaction", "branch_summary":
		return []Part{{Role: "user", Kind: "context", Text: e.Summary}}
	case "message":
	default:
		return nil
	}
	if e.Message == nil {
		return nil
	}
	m := e.Message
	var role string
	switch m.Role {
	case "user":
		role = "user"
	case "assistant":
		role = "assistant"
	case "toolResult":
		return []Part{{Role: "tool", Kind: "tool_result", Name: m.ToolName, Text: resultText(m.Content)}}
	default:
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []Part{textPart(role, s)}
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var parts []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, textPart(role, b.Text))
		case "thinking":
			parts = append(parts, Part{Role: role, Kind: "thinking", Text: b.Thinking})
		case "toolCall":
			parts = append(parts, Part{Role: role, Kind: "tool_use", Name: b.Name, Text: pretty(b.Arguments)})
		case "image":
			parts = append(parts, Part{Role: role, Kind: "image", Text: "[image]"})
		}
	}
	return parts
}
