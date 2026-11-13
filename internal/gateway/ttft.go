package gateway

// Time to first token (#196): how long a streamed reply took to begin —
// the first of its text, reasoning or a tool call — and to show its first
// text, so how fast a model writes can be told from how long it thought
// or waited. Read off the stream as it goes to the agent, in the agent's
// protocol, whichever the vendor spoke: one place for every way a reply
// comes, translated or not. A reply that isn't streamed isn't timed.

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// firstMost is the most of a stream read for its first tokens: a reply
// that is all tool calls has no text to wait for.
const firstMost = 8 << 20

// firstToken times a stream's first content and first text from start,
// and when the rest of its content came (flowMs).
type firstToken struct {
	onFirst                  func(int64)
	start                    time.Time
	first                    time.Duration // to the first content; 0 until it comes
	text                     time.Duration // to the first text
	reasoning, lastReasoning time.Duration
	hidden                   bool // redacted thinking cannot be timed by its visible portion
	off                      bool // not a stream, or read as far as it is
	began                    bool
	read                     int
	pend                     []byte
	// flow: how many bytes of content events had come by each time, one
	// mark a millisecond, from the first content on
	flow []flowMark
	sum  int
}

// flowMark is how many bytes of content events had come by at.
type flowMark struct {
	at  time.Duration
	sum int
}

// see reads what was written of the reply.
func (f *firstToken) see(b []byte) {
	if f.off || len(b) == 0 {
		return
	}
	if !f.began {
		// a JSON reply is no stream
		if t := bytes.TrimLeft(b, " \t\r\n"); len(t) == 0 {
			return
		} else if t[0] == '{' || t[0] == '[' {
			f.off = true
			return
		}
		f.began = true
	}
	if f.read += len(b); f.read > firstMost {
		f.off, f.pend = true, nil
		return
	}
	f.pend = append(f.pend, b...)
	d := max(time.Since(f.start), time.Millisecond)
	for !f.off {
		end := eventEnd(f.pend)
		if end < 0 {
			break
		}
		ev := f.pend[:end]
		f.pend = f.pend[end:]
		if f.text > 0 {
			// the first text is known: what is left to tell is when the
			// rest came, which a quicker look tells
			if flowContent(ev) {
				f.mark(d, len(ev))
			}
			continue
		}
		content, text, reasoning, hidden := timingKind(ev)
		f.hidden = f.hidden || hidden
		if reasoning {
			if f.reasoning == 0 {
				f.reasoning = d
			}
			f.lastReasoning = d
		}
		if !content && !text {
			continue
		}
		if f.first == 0 {
			f.first = d
			if f.onFirst != nil {
				f.onFirst(d.Milliseconds())
			}
		}
		f.mark(d, len(ev))
		if text {
			f.text = d
		}
	}
	if len(f.pend) == 0 {
		f.pend = nil
	}
}

// flowMost is the most marks kept: a reply that took longer than this many
// distinct milliseconds to come has its flow untold (flowMs).
const flowMost = 1 << 16

// mark counts n bytes of content as come by d.
func (f *firstToken) mark(d time.Duration, n int) {
	f.sum += n
	if k := len(f.flow); k > 0 && f.flow[k-1].at == d {
		f.flow[k-1].sum = f.sum
		return
	}
	if len(f.flow) >= flowMost {
		f.off, f.pend, f.flow = true, nil, nil
		return
	}
	f.flow = append(f.flow, flowMark{d, f.sum})
}

// flowContent: a server-sent event of a reply that carries content, told by
// a look rather than a parse: a delta of any protocol, or Gemini's parts.
// The end of a reply (Responses' response.completed, which says the whole
// reply again) carries none.
func flowContent(ev []byte) bool {
	return bodyHas(ev, "delta") || bodyHas(ev, `"parts"`)
}

// flowMs is how long the content of a reply took to come from its decode's
// start from (its first content, or first text, as ms): the time its middle
// 80% of content bytes took, scaled to the whole, so a reply that came
// evenly gets its whole window, and one that came in a burst — the vendor
// wrote it all at once after its first content (John on Discord: 1,367
// tok/s from Kimi Code) — gets next to none, and tells no speed
// (usage.DecodeOf). At least 1 when told; 0 when it can't be: no content
// after from, or a stream read too far to keep (flowMost, firstMost).
func (f *firstToken) flowMs(from int64) int64 {
	if from <= 0 || len(f.flow) == 0 || f.read > firstMost {
		return 0
	}
	base := 0
	for _, m := range f.flow {
		if m.at.Milliseconds() >= from {
			break
		}
		base = m.sum
	}
	total := f.sum - base
	if total <= 0 {
		return 0
	}
	at := func(share int) time.Duration {
		for _, m := range f.flow {
			if (m.sum-base)*100 >= total*share && m.at.Milliseconds() >= from {
				return m.at
			}
		}
		return f.flow[len(f.flow)-1].at
	}
	span := at(90) - at(10)
	return max(span.Milliseconds()*10/8, 1)
}

// ms are the two as milliseconds from start, 0 for none.
func (f *firstToken) ms() (first, text int64) {
	return f.first.Milliseconds(), f.text.Milliseconds()
}

// firstKind says whether a server-sent event of a reply, in any protocol an
// agent speaks, carries content — text, reasoning or a tool call — and
// whether that is text the reader sees.
func firstKind(ev []byte) (content, text bool) {
	content, text, _, _ = timingKind(ev)
	return
}

func timingKind(ev []byte) (content, text, reasoning, hidden bool) {
	var name string
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		switch {
		case bytes.HasPrefix(ln, []byte("event:")):
			name = string(bytes.TrimSpace(ln[6:]))
		case bytes.HasPrefix(ln, []byte("data:")):
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	// only deltas, a tool call's start and Gemini's parts carry any: the
	// rest go by unread
	if len(data) == 0 || !(bodyHas(data, "delta") || bodyHas(data, `"parts"`) ||
		bodyHas(data, "content_block_start") || bodyHas(data, "output_item.added") || bodyHas(data, "encrypted_content")) {
		return false, false, false, hidden
	}
	var v struct {
		Type         string          `json:"type"`
		Delta        json.RawMessage `json:"delta"` // Anthropic's an object, Responses' a string
		ContentBlock *struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Item *struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"item"`
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				Refusal          json.RawMessage `json:"refusal"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				Reasoning        json.RawMessage `json:"reasoning"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
				FunctionCall     json.RawMessage `json:"function_call"`
			} `json:"delta"`
		} `json:"choices"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string          `json:"text"`
					Thought      bool            `json:"thought"`
					FunctionCall json.RawMessage `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(data, &v) != nil {
		return false, false, false, hidden
	}
	typ := v.Type
	if typ == "" {
		typ = name
	}
	if v.Item != nil && v.Item.EncryptedContent != "" {
		hidden = true
	}
	if v.ContentBlock != nil && v.ContentBlock.Type == "redacted_thinking" {
		hidden = true
	}
	switch typ {
	case "content_block_delta": // Anthropic
		var d struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		}
		json.Unmarshal(v.Delta, &d)
		switch {
		case d.Text != "":
			return true, true, reasoning, hidden
		case d.Thinking != "":
			return true, false, true, false
		case d.PartialJSON != "":
			return true, false, reasoning, hidden
		}
		return false, false, false, hidden
	case "content_block_start":
		if b := v.ContentBlock; b != nil && (b.Type == "tool_use" || b.Type == "server_tool_use") {
			return true, false, reasoning, hidden
		}
		return false, false, false, hidden
	case "response.output_text.delta", "response.refusal.delta": // Responses
		return filled(v.Delta), filled(v.Delta), false, false
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return filled(v.Delta), false, (typ == "response.reasoning_text.delta" || typ == "response.reasoning_summary_text.delta") && filled(v.Delta), false
	case "response.output_item.added":
		if it := v.Item; it != nil && (it.Type == "function_call" || it.Type == "custom_tool_call") {
			return true, false, reasoning, hidden
		}
		return false, false, false, hidden
	}
	for _, c := range v.Choices { // Chat
		d := c.Delta
		reasoning = reasoning || filled(d.ReasoningContent) || filled(d.Reasoning)
		if filled(d.Content) || filled(d.Refusal) {
			return true, true, reasoning, hidden
		}
		if filled(d.ReasoningContent) || filled(d.Reasoning) || filled(d.ToolCalls) || filled(d.FunctionCall) {
			content = true
		}
	}
	for _, c := range v.Candidates { // Gemini
		for _, p := range c.Content.Parts {
			switch {
			case p.Text != "" && !p.Thought:
				return true, true, reasoning, hidden
			case p.Text != "", filled(p.FunctionCall):
				content = true
				reasoning = reasoning || p.Thought && p.Text != ""
			}
		}
	}
	return content, false, reasoning, hidden
}

// filled is a JSON value with something in it.
func filled(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", `""`, "[]", "{}":
		return false
	}
	return true
}

// flowFor is flowMs from where a reply's speed is counted from, as
// usage.DecodeOf counts it: its first text when it reasoned, else its
// first content.
func (f *firstToken) flowFor(reasoning int) int64 {
	if reasoning > 0 && f.text > 0 {
		return f.flowMs(f.text.Milliseconds())
	}
	return f.flowMs(f.first.Milliseconds())
}

// hidesReasoning excludes sealed thinking and Responses summaries even when
// the upstream omits a separate reasoning-token count. It also marks a
// reply whose reasoning streamed while counted, its usage's reasoning
// tokens, is 0 (#4545): that count is unknown, not none. Anthropic's usage
// never splits thinking out of output_tokens (DeepSeek's Anthropic endpoint
// streamed 33 thinking deltas and said only output_tokens 35), and a Chat
// vendor may send reasoning_content with no completion_tokens_details. The
// record's Reasoning stays the vendor's 0, never an estimate.
func (f *firstToken) hidesReasoning(proto provider.Protocol, counted int) bool {
	return f.hidden || f.reasoning > 0 && (counted <= 0 || proto == provider.Responses || proto == provider.Gemini || proto == provider.CodeAssist)
}

// reasoningMs requires a paced visible channel. Responses and Gemini summaries
// do not measure their full reasoning count; translation keeps that restriction.
func (f *firstToken) reasoningMs(proto provider.Protocol) int64 {
	if proto != provider.Chat && proto != provider.Anthropic || f.hidden || f.off ||
		f.reasoning == 0 || f.reasoning != f.first || f.lastReasoning-f.reasoning < 100*time.Millisecond {
		return 0
	}
	return f.reasoning.Milliseconds()
}
