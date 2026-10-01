package sessions

// What was said in a call — what the agent was given and what came back —
// read from the session file the call is in when it is asked for, from the place
// the call has in it (Call.File, From, To); magpie keeps no copy.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Part is one thing said in a call.
type Part struct {
	Role string `json:"role"`           // user, assistant or tool
	Kind string `json:"kind"`           // text, thinking, tool_use, tool_result, context or image
	Name string `json:"name,omitempty"` // the tool's, of its call
	Text string `json:"text"`
	Cut  int    `json:"cut,omitempty"` // the characters left off its end
}

// Content is what a call was given and what it said, in the order of the file.
type Content struct {
	Input  []Part `json:"input"`
	Output []Part `json:"output"`
	Cut    bool   `json:"cut,omitempty"` // there was more than is kept here
}

const (
	partMax    = 8000    // characters of one part
	contentMax = 160_000 // of all of them
)

var errNoPlace = errors.New("the call has no place in a file")

// add keeps a part, cut to what one may be, unless the content is full.
func (c *Content) add(input bool, p Part) {
	p.Text = strings.TrimSpace(p.Text)
	if p.Text == "" {
		return
	}
	size := 0
	for _, x := range c.Input {
		size += len(x.Text)
	}
	for _, x := range c.Output {
		size += len(x.Text)
	}
	if size >= contentMax {
		c.Cut = true
		return
	}
	if n := utf8.RuneCountInString(p.Text); n > partMax {
		r := []rune(p.Text)
		p.Text, p.Cut = string(r[:partMax]), n-partMax
	}
	if input {
		c.Input = append(c.Input, p)
	} else {
		c.Output = append(c.Output, p)
	}
}

// ContentOf reads what was said in a call from its file.
func ContentOf(c Call) (Content, error) {
	out := Content{Input: []Part{}, Output: []Part{}}
	if c.File == "" {
		return out, errNoPlace
	}
	var err error
	if c.Agent == "codex" {
		err = codexContent(c, &out)
	} else {
		err = claudeContent(c, &out)
	}
	return out, err
}

// FindCall is the call of a session made between two times that ended
// nearest at (a call's Time is its last line's), if the session files have
// one. A gateway request's window can hold the next calls of a quick tool
// loop too; the newest of them would be another request's.
func FindCall(session string, from, to, at time.Time) (Call, bool) {
	if session == "" {
		return Call{}, false
	}
	var best Call
	found := false
	dist := func(t time.Time) time.Duration { return max(t.Sub(at), at.Sub(t)) }
	for _, c := range callsFor(from.Add(-time.Minute), session) {
		if c.Session == session && !c.Time.Before(from) && !c.Time.After(to) && (!found || dist(c.Time) < dist(best.Time)) {
			best, found = c, true
		}
	}
	return best, found
}

// ---- Claude Code

type ccBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
}

// claudeContent reads the lines from where the call was asked to where it ended:
// the user lines — a prompt, a tool's result — are what it was given, and the
// lines of its own message what it said.
func claudeContent(c Call, out *Content) error {
	_, err := scanAt(c.File, c.From, nil, func(b []byte, start, end int64) bool {
		if start >= c.To {
			return false
		}
		user := bytes.Contains(b, ccUserLine)
		asst := bytes.Contains(b, ccAssistant)
		if !user && !asst {
			return true
		}
		var l struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &l) != nil {
			return true
		}
		switch {
		case l.Type == "user" && !l.IsMeta:
			for _, p := range claudeParts(l.Message.Content, "user") {
				out.add(true, p)
			}
		case l.Type == "assistant" && (c.Msg == "" || l.Message.ID == c.Msg):
			for _, p := range claudeParts(l.Message.Content, "assistant") {
				out.add(false, p)
			}
		}
		return true
	})
	return err
}

// claudeParts are the parts of a message's content, which is words, or blocks.
func claudeParts(raw json.RawMessage, role string) []Part {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []Part{textPart(role, text)}
	}
	var blocks []ccBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var parts []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, textPart(role, b.Text))
		case "thinking":
			parts = append(parts, Part{Role: role, Kind: "thinking", Text: b.Thinking})
		case "tool_use":
			parts = append(parts, Part{Role: role, Kind: "tool_use", Name: b.Name, Text: pretty(b.Input)})
		case "tool_result":
			parts = append(parts, Part{Role: "tool", Kind: "tool_result", Text: resultText(b.Content)})
		case "image":
			parts = append(parts, Part{Role: role, Kind: "image", Text: "[image]"})
		}
	}
	return parts
}

// textPart is words said, or, of what the agent put in itself (a reminder, a
// command's note), its context.
func textPart(role, text string) Part {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") {
		return Part{Role: role, Kind: "context", Text: text}
	}
	return Part{Role: role, Kind: "text", Text: text}
}

// resultText is what a tool answered: words, or blocks of them.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, b.Text)
		case "image":
			out = append(out, "[image]")
		}
	}
	return strings.Join(out, "\n")
}

// pretty is JSON laid out for reading, when it is short.
func pretty(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 4000 {
		return string(raw)
	}
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}

// ---- Codex

var cxItem = []byte(`"type":"response_item"`)

// codexContent reads the items between the call before and this one: what the
// model was given (the prompt, a tool's output) and what it said (words,
// reasoning, a call of a tool).
func codexContent(c Call, out *Content) error {
	_, err := scanAt(c.File, c.From, nil, func(b []byte, start, end int64) bool {
		if start >= c.To {
			return false
		}
		if !bytes.Contains(b, cxItem) {
			return true
		}
		var l struct {
			Payload struct {
				Type      string          `json:"type"`
				Role      string          `json:"role"`
				Name      string          `json:"name"`
				Arguments string          `json:"arguments"`
				Input     string          `json:"input"`
				Output    json.RawMessage `json:"output"`
				Content   []struct {
					Text string `json:"text"`
				} `json:"content"`
				Summary []struct {
					Text string `json:"text"`
				} `json:"summary"`
				Action struct {
					Command []string `json:"command"`
				} `json:"action"`
			} `json:"payload"`
		}
		if json.Unmarshal(b, &l) != nil {
			return true
		}
		p := &l.Payload
		words := func() string {
			var s []string
			for _, x := range p.Content {
				s = append(s, x.Text)
			}
			return strings.Join(s, "\n")
		}
		switch p.Type {
		case "message":
			switch p.Role {
			case "user":
				out.add(true, textPart("user", words()))
			case "assistant":
				out.add(false, textPart("assistant", words()))
			}
		case "reasoning":
			var s []string
			for _, x := range p.Summary {
				s = append(s, x.Text)
			}
			out.add(false, Part{Role: "assistant", Kind: "thinking", Text: strings.Join(s, "\n\n")})
		case "function_call":
			out.add(false, Part{Role: "assistant", Kind: "tool_use", Name: p.Name, Text: pretty(json.RawMessage(p.Arguments))})
		case "custom_tool_call":
			out.add(false, Part{Role: "assistant", Kind: "tool_use", Name: p.Name, Text: p.Input})
		case "local_shell_call":
			out.add(false, Part{Role: "assistant", Kind: "tool_use", Name: "shell", Text: strings.Join(p.Action.Command, " ")})
		case "function_call_output", "custom_tool_call_output":
			out.add(true, Part{Role: "tool", Kind: "tool_result", Text: codexOutput(p.Output)})
		}
		return true
	})
	return err
}

// codexOutput is what a tool answered: words, or an object with them in.
func codexOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Content string `json:"content"`
		Output  string `json:"output"`
	}
	if json.Unmarshal(raw, &o) == nil && (o.Content != "" || o.Output != "") {
		if o.Content != "" {
			return o.Content
		}
		return o.Output
	}
	return string(raw)
}
