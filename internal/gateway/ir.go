// Package gateway is the local LLM endpoint agents talk to. It serves the
// wire APIs coding agents speak — OpenAI Chat Completions, OpenAI
// Responses, Anthropic Messages and Google Gemini — and forwards each call to whichever
// provider serves the requested model, translating between the APIs when
// the provider does not speak the one the agent used.
package gateway

import (
	"encoding/json"
	"slices"
	"strings"
)

// The APIs are close cousins; everything below is the shape they have
// in common. A request is parsed into it, and a reply is produced from it.

// Kind is what a part of a message holds.
type Kind string

const (
	Text       Kind = "text"
	Image      Kind = "image"
	ToolCall   Kind = "tool_call"
	ToolResult Kind = "tool_result"
	Thinking   Kind = "thinking"
)

// Part is one block of a message.
type Part struct {
	Kind Kind
	Text string // text, thinking, or a tool result's output

	// image
	MediaType string
	Data      string // base64
	URL       string

	// tool_call
	ID   string
	Name string
	Args json.RawMessage // a JSON object

	// tool_result
	CallID  string
	IsError bool

	// thinking
	Signature string
}

// Message is one turn.
type Message struct {
	Role  string // user | assistant
	Parts []Part
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema of the arguments
}

// Request is a call to a model, whichever API it arrived in.
type Request struct {
	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string // "" | auto | none | required | name:<tool>
	MaxTokens  int
	Temp       *float64
	TopP       *float64
	Stop       []string
	Stream     bool
	Effort     string // low | medium | high | xhigh | max, when the client asked
	Thinking   bool   // the client asked for visible reasoning
	Parallel   *bool  // parallel tool calls allowed
}

// EventKind is what a streamed event carries.
type EventKind int

const (
	KStart     EventKind = iota // MsgID, Model, Usage (input side)
	KText                       // Text
	KThink                      // Text (reasoning)
	KSig                        // Text (thinking signature)
	KToolStart                  // ID, Name
	KToolArgs                   // Text (partial JSON of the arguments)
	KStop                       // Stop
	KUsage                      // Usage
	KError                      // Text
)

// Event is one thing a streaming reply said.
type Event struct {
	Kind  EventKind
	Text  string
	ID    string
	Name  string
	MsgID string
	Model string
	Stop  string // stop | length | tool | filter
	Usage Usage
}

// Usage counts tokens.
type Usage struct {
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
	Reasoning  int    `json:"reasoning"`
	State      string `json:"-"` // provisional | final, for Cursor usage only
}

// prompt is every token the prompt came to, as OpenAI's and Gemini's
// counts have it: Anthropic's leaves out what was read from its cache and
// what was written to it.
func (u Usage) prompt() int {
	return u.Input + u.CacheRead + u.CacheWrite
}

func (u *Usage) add(v Usage) {
	if v.State == "final" {
		u.Input, u.Output = v.Input, v.Output
		u.CacheRead, u.CacheWrite = v.CacheRead, v.CacheWrite
		u.Reasoning, u.State = v.Reasoning, v.State
		return
	}
	if v.Input > 0 {
		u.Input = v.Input
	}
	if v.Output > 0 {
		u.Output = v.Output
	}
	if v.CacheRead > 0 {
		u.CacheRead = v.CacheRead
	}
	if v.CacheWrite > 0 {
		u.CacheWrite = v.CacheWrite
	}
	if v.Reasoning > 0 {
		u.Reasoning = v.Reasoning
	}
	if v.State != "" {
		u.State = v.State
	}
}

// Result is a whole reply, for non-streaming clients.
type Result struct {
	ID    string
	Model string
	Parts []Part
	Stop  string
	Usage Usage
}

// collector assembles a Result from events. Encoders use the same logic to
// know what the reply contained so far.
type collector struct {
	res  Result
	args strings.Builder // arguments of the open tool call
	err  string
}

func (c *collector) last(k Kind) *Part {
	if n := len(c.res.Parts); n > 0 && c.res.Parts[n-1].Kind == k {
		return &c.res.Parts[n-1]
	}
	return nil
}

func (c *collector) closeTool() {
	if p := c.last(ToolCall); p != nil && p.Args == nil {
		s := strings.TrimSpace(c.args.String())
		if s == "" {
			s = "{}"
		}
		p.Args = json.RawMessage(s)
		c.args.Reset()
	}
}

func (c *collector) add(ev Event) {
	switch ev.Kind {
	case KStart:
		c.res.ID, c.res.Model = ev.MsgID, ev.Model
		c.res.Usage.add(ev.Usage)
	case KText:
		if p := c.last(Text); p != nil {
			p.Text += ev.Text
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Text, Text: ev.Text})
		}
	case KThink:
		if p := c.last(Thinking); p != nil {
			p.Text += ev.Text
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Thinking, Text: ev.Text})
		}
	case KSig:
		if p := c.last(Thinking); p != nil {
			p.Signature += ev.Text
		}
	case KToolStart:
		c.closeTool()
		c.res.Parts = append(c.res.Parts, Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name})
	case KToolArgs:
		c.args.WriteString(ev.Text)
	case KStop:
		c.closeTool()
		c.res.Stop = ev.Stop
	case KUsage:
		c.res.Usage.add(ev.Usage)
	case KError:
		c.err = ev.Text
	}
}

func (c *collector) finish() Result {
	c.closeTool()
	if c.res.Stop == "" {
		c.res.Stop = "stop"
		for _, p := range c.res.Parts {
			if p.Kind == ToolCall {
				c.res.Stop = "tool"
			}
		}
	}
	return c.res
}

// hasTool reports whether a result calls any tool.
func hasTool(parts []Part) bool {
	for _, p := range parts {
		if p.Kind == ToolCall {
			return true
		}
	}
	return false
}

// argsOf is a tool call's arguments as a JSON object, never empty.
func argsOf(p Part) json.RawMessage {
	if len(p.Args) == 0 || strings.TrimSpace(string(p.Args)) == "" {
		return json.RawMessage("{}")
	}
	return p.Args
}

// argsString is the same as a string, for the APIs that want one.
func argsString(p Part) string { return string(argsOf(p)) }

// parseArgs turns the string form of arguments into an object; a string
// that is not JSON is wrapped so nothing is lost.
func parseArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) && strings.HasPrefix(s, "{") {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(map[string]string{"input": s})
	return b
}

// text joins the text parts of a message.
func text(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == Text {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// stringOrText reads a JSON value that is either a string or an array of
// {type:"text", text} blocks (Anthropic's system, tool results…).
func stringOrText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, x := range blocks {
			if x.Type == "text" || x.Type == "input_text" || x.Type == "output_text" {
				b.WriteString(x.Text)
			}
		}
		return b.String()
	}
	return ""
}

// effortOf normalises the reasoning effort names the APIs use.
func effortOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal", "none":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(s)
	}
	return ""
}

// effortRank orders the reasoning levels agents and vendors name.
var effortRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// fitEffort is the level of the model's own nearest the one asked for — a
// tie goes up — or the one asked for when the model's aren't known. Codex
// asks "medium" of a model it was given no levels for, and an agent's
// setting can outlive the model it was picked for; GLM-5.3 takes low, high
// and max only.
func fitEffort(want string, levels []string) string {
	if len(levels) == 0 || slices.Contains(levels, want) {
		return want
	}
	at := slices.Index(effortRank, want)
	if at < 0 {
		return want
	}
	best, dist := want, len(effortRank)
	for _, l := range levels {
		i := slices.Index(effortRank, l)
		if i < 0 || l == "none" {
			continue
		}
		d := i - at
		if d < 0 {
			d = -d
		}
		if d < dist || d == dist && i > at {
			best, dist = l, d
		}
	}
	return best
}

// budgetOf is the Anthropic thinking budget for an effort level.
func budgetOf(effort string) int {
	switch effort {
	case "low":
		return 4096
	case "medium":
		return 10000
	case "high":
		return 24000
	case "xhigh", "max":
		return 32000
	}
	return 10000
}

// effortOfBudget goes the other way, for Anthropic clients asking others.
func effortOfBudget(n int) string {
	switch {
	case n <= 0:
		return ""
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	case n <= 24000:
		return "high"
	}
	return "xhigh"
}
