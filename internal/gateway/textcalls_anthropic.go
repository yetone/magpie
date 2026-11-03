package gateway

import (
	"encoding/json"
	"strings"
)

// anthropicTextCalls does what textCallTidy does in a Messages reply
// relayed as it came (passthrough): a Messages client on a provider that
// speaks Messages itself (MiniMax, Kimi, GLM, DeepSeek and the relays in
// front of Claude), whose model wrote its calls into its text
// (packinggoodsllc_07668 on Discord: Claude Code through a routing group
// was shown <invoke name="ToolSearch">… as text). Only a translated reply
// was read for them before. Text from where a call begins is held, with
// its block's start while nothing of it has gone and its stop; at the
// message_delta the held calls become tool_use blocks after it and the
// reply stops for tool_use. Held text that isn't calls goes on as it came.
// A reply that came whole (no stream) is read as wholeTextCalls reads a
// Chat one.
//
// A reply carrying auto mode's review (safeguard_results, automode.go)
// goes as it came: Anthropic judged the calls it made by their ids, and a
// call made here would have an id it never judged, which Claude Code
// can't match to a verdict (xybio on Discord). Its text goes as text.
type anthropicTextCalls struct {
	names map[string]bool
	sse   bool
	buf   []byte

	idx       int    // the open text block's index
	text      bool   // a text block is being read
	startHeld []byte // its start, while none of its text has gone on
	stopHeld  []byte // its stop, while text of it is held
	pending   string
	held      string
	holding   bool
	native    bool // the reply made a call itself
	top       int  // one past the highest block index seen
}

func (t *anthropicTextCalls) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	out, rest := tidyEvents(t.buf, t.event)
	t.buf = rest
	return out
}

func (t *anthropicTextCalls) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		return t.whole(b)
	}
	var out []byte
	if len(b) > 0 {
		out = t.event(b)
	}
	return append(out, t.release()...)
}

// messagesEvent is a Messages stream event of v.
func messagesEvent(typ string, v any) []byte {
	b, _ := marshalPlain(v)
	return []byte("event: " + typ + "\ndata: " + string(b) + "\n\n")
}

func (t *anthropicTextCalls) textDelta(s string) []byte {
	return messagesEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": t.idx,
		"delta": map[string]any{"type": "text_delta", "text": s}})
}

// event is one whole event, given on, held or made into others.
func (t *anthropicTextCalls) event(ev []byte) []byte {
	data := eventData(ev)
	var m map[string]json.RawMessage
	if len(data) == 0 || decodeJSON(data, &m) != nil || m == nil {
		return ev
	}
	var typ string
	decodeJSON(m["type"], &typ)
	var index int
	decodeJSON(m["index"], &index)
	switch typ {
	case "content_block_start":
		pre := t.release()
		t.top = max(t.top, index+1)
		var cb struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		decodeJSON(m["content_block"], &cb)
		if cb.Type == "tool_use" || cb.Type == "server_tool_use" {
			t.native = true
		}
		if cb.Type == "text" && cb.Text == "" {
			t.idx, t.text, t.startHeld = index, true, append([]byte(nil), ev...)
			return pre
		}
		t.text = false
		return append(pre, ev...)
	case "content_block_delta":
		var d struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		decodeJSON(m["delta"], &d)
		if !t.text || index != t.idx || d.Type != "text_delta" {
			return ev
		}
		s := t.pending + d.Text
		t.pending = ""
		if t.holding {
			t.held += s
			return nil
		}
		if at, tail := textCallAt(s); at >= 0 {
			t.holding, t.held, s = true, s[at:], s[:at]
		} else {
			t.pending, s = s[len(s)-tail:], s[:len(s)-tail]
		}
		t.pending, t.held, s = spaceBack(t.native, t.holding, t.pending, t.held, s)
		if s == "" {
			return nil
		}
		out := t.startHeld
		t.startHeld = nil
		return append(out, t.textDelta(s)...)
	case "content_block_stop":
		if !t.text || index != t.idx {
			return ev
		}
		if t.holding || t.pending != "" {
			t.stopHeld = append([]byte(nil), ev...)
			return nil
		}
		out := t.startHeld
		t.startHeld, t.text = nil, false
		return append(out, ev...)
	case "message_delta":
		var pre []byte
		if t.holding && !t.native && !hasReview(m["delta"]) {
			if calls, rest, ok := parseTextCalls(t.held, t.names); ok {
				t.held, t.holding = "", false
				rest = t.pending + rest
				t.pending = ""
				if rest != "" {
					pre = append(pre, t.startHeld...)
					t.startHeld = nil
					pre = append(pre, t.textDelta(rest)...)
				}
				next := t.top
				if t.startHeld != nil {
					// nothing of the text block went: the calls take its place
					next, t.startHeld, t.stopHeld = t.idx, nil, nil
				} else {
					if t.stopHeld == nil {
						t.stopHeld = messagesEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": t.idx})
					}
					pre = append(pre, t.stopHeld...)
					t.stopHeld = nil
				}
				t.text = false
				for i, c := range calls {
					at := next + i
					pre = append(pre, messagesEvent("content_block_start", map[string]any{"type": "content_block_start", "index": at,
						"content_block": map[string]any{"type": "tool_use", "id": anthropicOutID(textCallID()), "name": c.Name, "input": map[string]any{}}})...)
					pre = append(pre, messagesEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": at,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": string(c.Args)}})...)
					pre = append(pre, messagesEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": at})...)
				}
				t.top = next + len(calls)
				if len(calls) > 0 {
					var delta map[string]json.RawMessage
					if decodeJSON(m["delta"], &delta) == nil && delta != nil {
						delta["stop_reason"] = json.RawMessage(`"tool_use"`)
						m["delta"], _ = marshalPlain(delta)
						return append(append(pre, t.release()...), messagesEvent(typ, m)...)
					}
				}
			}
		} else {
			t.pending, t.held, t.holding = settled(t.native, t.holding, t.pending, t.held)
		}
		return append(append(pre, t.release()...), ev...)
	case "message_stop", "error":
		return append(t.release(), ev...)
	}
	return ev
}

// release hands on as text what is held, in the block it was held from,
// with that block's start and stop when they were held.
func (t *anthropicTextCalls) release() []byte {
	s := t.pending + t.held
	t.pending, t.held, t.holding = "", "", false
	var out []byte
	if s != "" || t.stopHeld != nil {
		out = append(out, t.startHeld...)
		t.startHeld = nil
	}
	if s != "" {
		out = append(out, t.textDelta(s)...)
	}
	if t.stopHeld != nil {
		out = append(out, t.stopHeld...)
		t.stopHeld, t.text = nil, false
	}
	return out
}

// whole reads the calls written into the last text block of a Messages
// reply that came whole: they become tool_use blocks after it and the
// reply stops for tool_use. A reply that made a call itself, or whose
// written blocks aren't all calls of names, goes as it came.
func (t *anthropicTextCalls) whole(b []byte) []byte {
	if !mayWriteCalls(b, t.names) {
		return b
	}
	var reply map[string]json.RawMessage
	if decodeJSON(b, &reply) != nil || reply == nil {
		return b
	}
	if _, ok := reply["safeguard_results"]; ok {
		return b
	}
	var content []map[string]json.RawMessage
	if decodeJSON(reply["content"], &content) != nil || len(content) == 0 {
		return b
	}
	for _, c := range content {
		var typ string
		decodeJSON(c["type"], &typ)
		if typ == "tool_use" || typ == "server_tool_use" {
			return b
		}
	}
	last := content[len(content)-1]
	var typ, s string
	if decodeJSON(last["type"], &typ) != nil || typ != "text" || decodeJSON(last["text"], &s) != nil {
		return b
	}
	at, _ := textCallAt(s)
	if at < 0 {
		return b
	}
	calls, rest, ok := parseTextCalls(s[at:], t.names)
	if !ok {
		return b
	}
	content = content[:len(content)-1]
	if text := strings.TrimRight(s[:at]+rest, " \t\r\n"); text != "" {
		last["text"], _ = marshalPlain(text)
		content = append(content, last)
	}
	for _, c := range calls {
		content = append(content, map[string]json.RawMessage{
			"type": json.RawMessage(`"tool_use"`), "id": mustPlain(anthropicOutID(textCallID())),
			"name": mustPlain(c.Name), "input": c.Args,
		})
	}
	reply["content"], _ = marshalPlain(content)
	if len(calls) > 0 {
		reply["stop_reason"] = json.RawMessage(`"tool_use"`)
	}
	nb, err := marshalPlain(reply)
	if err != nil {
		return b
	}
	return nb
}

// hasReview reports whether a stream's message_delta delta carries auto
// mode's review results.
func hasReview(delta json.RawMessage) bool {
	var d map[string]json.RawMessage
	if decodeJSON(delta, &d) != nil {
		return false
	}
	_, ok := d["safeguard_results"]
	return ok
}

func mustPlain(v any) json.RawMessage {
	b, _ := marshalPlain(v)
	return b
}
