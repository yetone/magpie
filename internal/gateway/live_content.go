package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// The tokens of a reply in flight can only be estimated: the vendor's
// usage comes with the last event. What is counted is its content, the
// text, reasoning and tool names and arguments, not every byte written:
// the framing grows with the number of events, and differs by protocol,
// so it would read more events as more written. Content is counted in the
// agent's protocol, translated replies too, as what the agent received.
//
// Only a stream is counted: a whole JSON reply arrives at once and has
// nothing to show while it is out, and counting it would hold a copy of
// it. An event is decoded and dropped; only its content's length stays.
// What isn't known counts 0.

// liveLineMost is the longest SSE line kept across writes: one longer is
// not counted rather than held.
const liveLineMost = 1 << 20

// liveContentCounter counts a reply's content bytes, event by event.
type liveContentCounter struct {
	proto provider.Protocol
	// known: the first write said whether this is a stream (sse)
	known, sse bool
	// part: a line cut by the end of a write, until its end comes
	part []byte
	// data: the data lines of the event being read
	data []byte
	// skip: the line being read is too long, and is passed over to its end
	skip  bool
	total int64
}

// add reads one write, line by line. A whole write's lines are read in
// place; only a line a write cuts is copied, to be joined with the next.
func (c *liveContentCounter) add(p []byte) {
	if !c.known {
		i := 0
		for i < len(p) && (p[i] == ' ' || p[i] == '\r' || p[i] == '\n' || p[i] == '\t') {
			i++
		}
		if i == len(p) {
			return
		}
		c.known = true
		c.sse = p[i] != '{' && p[i] != '['
	}
	if !c.sse {
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if !c.skip && len(c.part)+len(p) <= liveLineMost {
				c.part = append(c.part, p...)
			} else {
				c.part, c.skip = c.part[:0], true
			}
			return
		}
		line := p[:i]
		p = p[i+1:]
		if c.skip {
			c.skip = false
			continue
		}
		if len(c.part) > 0 {
			if len(c.part)+len(line) > liveLineMost {
				c.part = c.part[:0]
				continue
			}
			c.part = append(c.part, line...)
			line = c.part
		}
		c.line(bytes.TrimSuffix(line, []byte{'\r'}))
		c.part = c.part[:0]
	}
}

// line reads one SSE line: a blank one ends the event.
func (c *liveContentCounter) line(line []byte) {
	if len(line) == 0 {
		c.event()
		return
	}
	rest, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	rest = bytes.TrimPrefix(rest, []byte{' '})
	if len(c.data)+len(rest)+1 > liveLineMost {
		c.data = c.data[:0]
		return
	}
	if len(c.data) > 0 {
		c.data = append(c.data, '\n')
	}
	c.data = append(c.data, rest...)
}

// event counts the event read, and forgets it.
func (c *liveContentCounter) event() {
	if len(c.data) > 0 {
		c.total += int64(liveContentOf(c.proto, bytes.TrimSpace(c.data)))
		c.data = c.data[:0]
	}
}

// flush counts what the stream ended without a blank line after.
func (c *liveContentCounter) flush() {
	if !c.sse {
		return
	}
	if len(c.part) > 0 && !c.skip {
		c.line(bytes.TrimSuffix(c.part, []byte{'\r'}))
	}
	c.part = nil
	c.event()
	c.data = nil
}

// liveContentOf is the content bytes of one event, read by its protocol.
func liveContentOf(proto provider.Protocol, b []byte) int {
	if len(b) == 0 || b[0] != '{' {
		return 0
	}
	switch proto {
	case provider.Chat:
		return liveChatContent(b)
	case provider.Responses:
		return liveResponsesContent(b)
	case provider.Gemini:
		return liveGeminiContent(b)
	default:
		return liveAnthropicContent(b)
	}
}

// liveChatDelta is a Chat delta: its text, reasoning and tool calls.
type liveChatDelta struct {
	Content          json.RawMessage    `json:"content"`
	ReasoningContent string             `json:"reasoning_content"`
	Reasoning        string             `json:"reasoning"`
	ToolCalls        []liveChatToolCall `json:"tool_calls"`
}

type liveChatToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func liveChatContent(b []byte) int {
	var v struct {
		Choices []struct {
			Delta liveChatDelta `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	n := 0
	for _, ch := range v.Choices {
		n += liveChatDeltaLen(ch.Delta)
	}
	return n
}

func liveChatDeltaLen(d liveChatDelta) int {
	n := len(d.ReasoningContent) + len(d.Reasoning) + liveContentLen(d.Content)
	for _, tc := range d.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// liveContentLen is a content field's: a string, or text parts.
func liveContentLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return len(s)
		}
	case '[':
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			n := 0
			for _, p := range parts {
				n += len(p.Text)
			}
			return n
		}
	}
	return 0
}

// liveResponsesContent counts *.delta events only: *.done says the same
// text again.
func liveResponsesContent(b []byte) int {
	var v struct {
		Type  string          `json:"type"`
		Delta json.RawMessage `json:"delta"`
		Item  struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"item"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	if strings.HasSuffix(v.Type, ".delta") {
		return liveContentLen(v.Delta)
	}
	// a function call starts with its name; its arguments come as deltas
	if v.Type == "response.output_item.added" && v.Item.Type == "function_call" {
		return len(v.Item.Name)
	}
	return 0
}

// liveAnthropicContent counts content_block_delta's text, thinking and
// tool input, and a tool_use block's name.
func liveAnthropicContent(b []byte) int {
	var v struct {
		Type  string `json:"type"`
		Delta struct {
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	n := len(v.Delta.Text) + len(v.Delta.Thinking) + len(v.Delta.PartialJSON)
	if v.ContentBlock.Type == "tool_use" {
		n += len(v.ContentBlock.Name)
	}
	return n
}

// liveGeminiContent counts a chunk's parts, each new.
func liveGeminiContent(b []byte) int {
	var v struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	n := 0
	for _, c := range v.Candidates {
		for _, p := range c.Content.Parts {
			n += len(p.Text)
			if p.FunctionCall.Name != "" || len(p.FunctionCall.Args) > 0 {
				n += len(p.FunctionCall.Name) + len(p.FunctionCall.Args)
			}
		}
	}
	return n
}
