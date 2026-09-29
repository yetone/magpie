package gateway

import (
	"bytes"
	"encoding/json"
)

// chatTidy mends a Chat Completions stream relayed as it is. WorkBuddy's
// (copilot.tencent.com) keeps sending "reasoning_content": "" in every
// delta once the model has done thinking; its own CLI ignores an empty one
// after the thinking is over, Cline does too, but Qoder takes each for
// the thinking starting again and each piece of text after it for a new
// message, so a reply read one word to a line. An empty reasoning_content
// says nothing, so it is left out.
//
// Two more of the stream's shapes are mended with it. Every chunk that
// hasn't finished carries "finish_reason": "" where the OpenAI contract
// has null; an empty string is not one of the field's values and a strict
// client (the AI SDK) rejects the chunk for it, so it becomes null. And
// every tool-call argument fragment after the first repeats
// "function": {"name": ""} although the real name went with the first
// fragment; a client that merges a delta by overwriting any field it
// carries (the grok CLI) assembles a tool call named "" and fails it with
// "Tool not found", so the empty duplicate is dropped and the real name
// is only sent once.
//
// Lines pass whole, as they came, unless one has any of them.
type chatTidy struct {
	buf []byte
}

var (
	emptyReasoning    = []byte(`"reasoning_content":""`)
	emptyFinishReason = []byte(`"finish_reason":""`)
	emptyToolName     = []byte(`"name":""`)
)

// write takes what was read and gives back what to send on: every line
// that is complete, mended.
func (t *chatTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := tidyLines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

// flush is what's left once the stream ends.
func (t *chatTidy) flush() []byte {
	out := tidyLines(t.buf)
	t.buf = nil
	return out
}

func tidyLines(b []byte) []byte {
	if !needsTidy(b) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		out = append(out, tidyLine(line)...)
	}
	return out
}

// needsTidy reports whether a line carries any of the shapes chatTidy
// mends.
func needsTidy(line []byte) bool {
	return bytes.Contains(line, emptyReasoning) ||
		bytes.Contains(line, emptyFinishReason) ||
		bytes.Contains(line, emptyToolName)
}

func tidyLine(line []byte) []byte {
	if !needsTidy(line) {
		return line
	}
	body := bytes.TrimRight(line, "\r\n")
	end := line[len(body):]
	data, ok := bytes.CutPrefix(body, []byte("data:"))
	if !ok {
		return line
	}
	var chunk map[string]json.RawMessage
	if json.Unmarshal(data, &chunk) != nil {
		return line
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(chunk["choices"], &choices) != nil {
		return line
	}
	changed := false
	for _, c := range choices {
		if string(c["finish_reason"]) == `""` {
			c["finish_reason"] = json.RawMessage("null")
			changed = true
		}
		var delta map[string]json.RawMessage
		if json.Unmarshal(c["delta"], &delta) != nil {
			continue
		}
		inDelta := false
		if string(delta["reasoning_content"]) == `""` {
			delete(delta, "reasoning_content")
			inDelta = true
		}
		if calls, ok := delta["tool_calls"]; ok {
			if mended, chg := withoutEmptyNames(calls); chg {
				delta["tool_calls"] = mended
				inDelta = true
			}
		}
		if inDelta {
			c["delta"], _ = json.Marshal(delta)
			changed = true
		}
	}
	if !changed {
		return line
	}
	chunk["choices"], _ = json.Marshal(choices)
	nb, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
	return append(append([]byte("data: "), nb...), end...)
}

// withoutEmptyNames drops the empty "name" on a delta's tool-call
// fragments and reports whether any was dropped. A fragment whose
// function carries no name at all is left as it is; one with a real name
// keeps it.
func withoutEmptyNames(raw json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	if json.Unmarshal(raw, &calls) != nil {
		return raw, false
	}
	changed := false
	for _, call := range calls {
		var fn map[string]json.RawMessage
		if json.Unmarshal(call["function"], &fn) != nil {
			continue
		}
		if string(fn["name"]) != `""` {
			continue
		}
		delete(fn, "name")
		call["function"], _ = json.Marshal(fn)
		changed = true
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(calls)
	if err != nil {
		return raw, false
	}
	return out, true
}
