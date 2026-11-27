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
	Parts []Part `json:"parts"`
	Cut   bool   `json:"cut,omitempty"` // there was more than is kept here
	// From is the parts before the first one kept, left out so that the
	// part a search found is shown (TranscriptAt)
	From     int    `json:"from,omitempty"`
	Source   string `json:"source,omitempty"`
	Captured int    `json:"captured,omitempty"`
	// Turns is the user's messages in the whole file, and TurnsBefore
	// those among the From parts left out: what a sheet showing only
	// part of a long session counts its turns against (TurnsOf)
	Turns       int `json:"turns,omitempty"`
	TurnsBefore int `json:"turnsBefore,omitempty"`
	size        int // characters of Parts' text, kept up as they are added
	whole       bool
}

// Next is the number of the message a read of the rest starts at (as
// From, TranscriptAfter's n), when t stops before the session's end; 0
// when it reaches it.
func (t Transcript) Next() int {
	if !t.Cut {
		return 0
	}
	return t.From + len(t.Parts)
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
	case "claude", "qoder", "qoder-cn", "codex", "pi", "raft", "cindy", "omp", "cursor", "opencode", "kimi", "agy", "antigravity", "openmausbot", "catpaw", "dim", "liveagent", "workbuddy", "workbuddy-ai", "codebuddy", "hermes":
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
	if t.size >= transcriptMax {
		t.Cut = true
		return false
	}
	if p, ok := keepAs(p, t.whole); ok {
		t.Parts = append(t.Parts, p)
		t.size += len(p.Text)
	}
	return true
}

// TranscriptOf reads a session's conversation from its (main) file.
func TranscriptOf(s Session) (Transcript, error) { return transcriptOf(s, false) }

// transcriptOf is TranscriptOf, each part whole with whole.
func transcriptOf(s Session, whole bool) (Transcript, error) {
	if !Reads(s.Agent) {
		return Transcript{Parts: []Part{}}, ErrNotRead
	}
	out := Transcript{Parts: []Part{}, whole: whole}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return out, ErrNoTranscript
	}
	return out, readTranscript(s, out.add)
}

// transcriptLead is the parts kept before the one TranscriptAt finds,
// when the ones before it are left out.
const transcriptLead = 6

// TranscriptAt is TranscriptOf for a reader brought to the part that
// holds needle (NeedleOf), the first said one, else the first anywhere,
// as SearchSession's hit: when TranscriptOf would be cut before that
// part, the parts before it are left out (but a few) and From says how
// many were.
func TranscriptAt(s Session, needle string) (Transcript, error) {
	return transcriptAt(s, needle, false)
}

// transcriptAt is TranscriptAt, each part whole with whole.
func transcriptAt(s Session, needle string, whole bool) (Transcript, error) {
	if needle == "" {
		return transcriptOf(s, whole)
	}
	if !Reads(s.Agent) {
		return Transcript{Parts: []Part{}}, ErrNotRead
	}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return Transcript{Parts: []Part{}}, ErrNoTranscript
	}
	// the parts TranscriptOf keeps are counted as it keeps them, and the
	// found one's place among them noted, and the characters before it
	at, saidAt, n, size, sizeAt, saidSize := -1, -1, 0, 0, 0, 0
	if err := readTranscript(s, func(_ bool, p Part) bool {
		p, ok := keepAs(p, whole)
		if !ok {
			return true
		}
		if searched(p) {
			if _, hit := hitIn(p, needle); hit {
				if at < 0 {
					at, sizeAt = n, size
				}
				if said(p) && saidAt < 0 {
					saidAt, saidSize = n, size
				}
			}
		}
		n++
		size += len(p.Text)
		return saidAt < 0
	}); err != nil {
		return Transcript{Parts: []Part{}}, err
	}
	if saidAt >= 0 {
		at, sizeAt = saidAt, saidSize
	}
	if at < 0 || sizeAt < transcriptMax {
		return transcriptOf(s, whole)
	}
	return transcriptAfter(s, max(0, at-transcriptLead), whole)
}

// TranscriptAfter is TranscriptOf from the part after the first n it
// keeps, and From says n: the rest of a transcript that was Cut, read
// when the reader asks for it, so that a long session is never shown
// only up to its first transcriptMax characters (usrname1833 on Discord:
// a long session's sheet stopped before its last turns).
func TranscriptAfter(s Session, n int) (Transcript, error) {
	return transcriptAfter(s, n, false)
}

// transcriptAfter is TranscriptAfter, each part whole with whole: the
// parts are numbered as TranscriptAfter numbers them, since a part is
// left out only when it says nothing, whole or not.
func transcriptAfter(s Session, n int, whole bool) (Transcript, error) {
	if n <= 0 {
		return transcriptOf(s, whole)
	}
	if !Reads(s.Agent) {
		return Transcript{Parts: []Part{}}, ErrNotRead
	}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return Transcript{Parts: []Part{}}, ErrNoTranscript
	}
	out := Transcript{Parts: []Part{}, From: n, whole: whole}
	i := 0
	err := readTranscript(s, func(in bool, p Part) bool {
		if _, ok := keep(p); !ok {
			return true
		}
		i++
		if i <= n {
			return true
		}
		return out.add(in, p)
	})
	return out, err
}

// turn says whether a kept part starts a turn as a sheet counts them: a
// message the user wrote (a [data-turn] in the GUI's drawTalk).
func turn(p Part) bool {
	return p.Role == "user" && (p.Kind == "" || p.Kind == "text")
}

// TurnsOf reads a session's whole file to count its turns, and those
// among the first before parts it keeps: a long session's sheet is read
// a part at a time, and still says how many turns the session has
// (usrname1833 on Discord: "turn i / N" counted only what was read).
func TurnsOf(s Session, before int) (all, inBefore int, err error) {
	if !Reads(s.Agent) {
		return 0, 0, ErrNotRead
	}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return 0, 0, ErrNoTranscript
	}
	i := 0 // the parts kept so far, as keep keeps them
	err = readTranscript(s, func(_ bool, p Part) bool {
		p, ok := keep(p)
		if !ok {
			return true
		}
		if turn(p) {
			all++
			if i < before {
				inBefore++
			}
		}
		i++
		return true
	})
	return all, inBefore, err
}

// Counted is t with Turns and TurnsBefore filled in, when t holds only
// part of the session (it was cut, or starts past From); a whole one's
// turns are its own.
func Counted(s Session, t Transcript) (Transcript, error) {
	if !t.Cut && t.From <= 0 {
		return t, nil
	}
	all, before, err := TurnsOf(s, t.From)
	if err != nil {
		return t, err
	}
	t.Turns, t.TurnsBefore = all, before
	return t, nil
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
	case "antigravity":
		return antigravityTranscriptOf(s.Path, keep, add)
	case "openmausbot":
		return ombTranscript(s, add)
	case "hermes":
		return hermesTranscript(s, add)
	case "catpaw":
		return catpawTranscript(s, add)
	case "dim":
		return dimTranscript(s, add)
	case "liveagent":
		return liveagentTranscript(s.Path, add)
	case "workbuddy", "workbuddy-ai", "codebuddy":
		return wbTranscript(s.Path, add)
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
