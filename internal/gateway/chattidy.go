package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
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
// And every delta carries "tool_calls": [], text and thinking too. An
// empty list says nothing, but Qoder (qodercli 1.1.65's stream adapter
// tests delta.tool_calls for being there, not for holding any) closes its
// text block on each one, so each piece of text after it opened a block
// of its own and a reply read a word to a line ("我来 / 看 / 一下 / …");
// the empty list is left out.
//
// And Mistral sends a reasoning model's thinking (GLM on api.mistral.ai)
// as typed parts: "content": [{"type": "thinking", "thinking": [{"type":
// "text", "text": "…"}]}, {"type": "text", "text": "ok"}], thinking and
// text sometimes in one delta. The contract has a string or null there,
// and the AI SDK OpenCode reads Chat with turns the first such chunk away
// ("Invalid … stream event", #483), so the thinking goes to
// reasoning_content, as other upstreams send it, and the text to a string
// content.
//
// Lines pass whole, as they came, unless one has any of them.
type chatTidy struct {
	buf []byte
}

var (
	emptyReasoning    = []byte(`"reasoning_content":""`)
	emptyFinishReason = []byte(`"finish_reason":""`)
	emptyToolName     = []byte(`"name":""`)
	emptyToolCalls    = []byte(`"tool_calls":[]`)
	contentParts      = []byte(`"content":[`)
	contentPartsSpace = []byte(`"content": [`)
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
		bytes.Contains(line, emptyToolName) ||
		bytes.Contains(line, emptyToolCalls) ||
		bytes.Contains(line, contentParts) ||
		bytes.Contains(line, contentPartsSpace)
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
		inDelta := flattenContent(delta, false)
		if string(delta["reasoning_content"]) == `""` {
			delete(delta, "reasoning_content")
			inDelta = true
		}
		if calls, ok := delta["tool_calls"]; ok && string(calls) == "[]" {
			delete(delta, "tool_calls")
			inDelta = true
		} else if ok {
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

// partsText reads a content that is an array of typed parts: its text,
// and the text of its thinking parts. ok is false for any other content,
// a string or null.
func partsText(raw json.RawMessage) (text, think string, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return "", "", false
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking json.RawMessage `json:"thinking"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", "", false
	}
	var tb, kb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			tb.WriteString(p.Text)
		case "thinking":
			// Mistral's is a list of text parts; a plain string is taken too
			var s string
			if json.Unmarshal(p.Thinking, &s) == nil {
				kb.WriteString(s)
				continue
			}
			var inner []struct {
				Text string `json:"text"`
			}
			json.Unmarshal(p.Thinking, &inner)
			for _, in := range inner {
				kb.WriteString(in.Text)
			}
		}
	}
	return tb.String(), kb.String(), true
}

// flattenContent turns a delta's or message's content of typed parts into
// a string content, and its thinking into reasoning_content after any
// already there, and reports whether it did. A delta with no text left
// goes without content; a whole message keeps an empty one.
func flattenContent(m map[string]json.RawMessage, whole bool) bool {
	text, think, ok := partsText(m["content"])
	if !ok {
		return false
	}
	if text != "" || whole {
		m["content"], _ = json.Marshal(text)
	} else {
		delete(m, "content")
	}
	if think != "" {
		var had string
		json.Unmarshal(m["reasoning_content"], &had)
		m["reasoning_content"], _ = json.Marshal(had + think)
	}
	return true
}

// chatWhole mends a Chat Completions reply that came whole, not streamed:
// a message whose content is typed parts (Mistral's thinking, #483) goes
// on with a string content and its thinking in reasoning_content. It is
// held until it ends; any other reply passes byte for byte.
type chatWhole struct {
	buf []byte
}

func (t *chatWhole) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	return nil
}

func (t *chatWhole) flush() []byte {
	b := t.buf
	t.buf = nil
	if !bytes.Contains(b, contentParts) && !bytes.Contains(b, contentPartsSpace) {
		return b
	}
	var reply map[string]json.RawMessage
	if json.Unmarshal(b, &reply) != nil {
		return b
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(reply["choices"], &choices) != nil {
		return b
	}
	changed := false
	for _, c := range choices {
		var msg map[string]json.RawMessage
		if json.Unmarshal(c["message"], &msg) != nil || !flattenContent(msg, true) {
			continue
		}
		c["message"], _ = json.Marshal(msg)
		changed = true
	}
	if !changed {
		return b
	}
	reply["choices"], _ = json.Marshal(choices)
	nb, err := json.Marshal(reply)
	if err != nil {
		return b
	}
	return nb
}
