package gateway

import (
	"encoding/json"
	"strings"
)

// toolBlocksPaired is pairToolBlocks for a Messages request relayed as the
// client sent it (passthrough, count_tokens), as toolPairsOut is
// pairToolMessages for a Chat one. Anthropic, and the relays and plugins
// serving Claude over Messages, turn the whole request away when its tool
// exchange doesn't pair: a call the next message doesn't answer
// ("messages.N: `tool_use` ids were found without `tool_result` blocks
// immediately after: toolu_…"), an answer to no call of the message
// before ("unexpected `tool_use_id` found in `tool_result` blocks"), and
// results after other content of their message. Claude Code and Claude
// Desktop show either as "400 due to tool use concurrency issues", and
// every later request of the conversation carries the same history, so
// the conversation was stuck (miwanzi on Discord: several conversations
// at once through magpie). A reply cut after a call's block was told, a
// stop while parallel calls ran, or a client that lost a result leaves
// such a history; the translations had it mended (pairToolBlocks), the
// relay sent it as it came.
//
// A body whose exchange pairs comes back byte for byte: nothing is decoded
// into maps and nothing is marshalled again, since a Claude request's
// bytes are its prompt cache and its client's. A mended body keeps every
// message it didn't have to change as it came; one it did keeps its other
// members and blocks as they came, with a missing call's synthetic error
// result first, an answer to no call as the text it said, and a second
// answer to one call left out.
func toolBlocksPaired(body []byte) []byte {
	if !bodyHas(body, `"tool_use"`) && !bodyHas(body, `"tool_result"`) {
		return body
	}
	msgs, found, ok := topMember(body, "messages")
	if !ok || !found || len(msgs) == 0 || msgs[0] != '[' {
		return body
	}
	mended, changed := pairedMessages(msgs)
	if !changed {
		return body
	}
	return withTopValue(body, "messages", mended)
}

// pairedMessages is messages, a valid JSON array, with its tool exchange
// mended, and whether anything had to be.
func pairedMessages(msgs []byte) ([]byte, bool) {
	var out []json.RawMessage
	changed := false
	var pending []string          // the calls of the assistant message in hand
	answered := map[string]bool{} // which of them a result answered
	open := func(id string) bool {
		for _, p := range pending {
			if p == id {
				return true
			}
		}
		return false
	}
	unanswered := func() []json.RawMessage {
		var blocks []json.RawMessage
		for _, id := range pending {
			if !answered[id] {
				blocks = append(blocks, syntheticResult(id))
			}
		}
		return blocks
	}
	valid := true
	eachElement(msgs, func(el []byte) bool {
		if el[0] != '{' {
			valid = false
			return false
		}
		var role string
		var content []byte
		eachMember(el, func(k, v []byte) bool {
			switch {
			case keyIs(k, "role"):
				if s, ok := stringBytes(v); ok {
					role = string(s)
				}
			case keyIs(k, "content"):
				content = v
			}
			return true
		})
		if role == "assistant" {
			if blocks := unanswered(); len(blocks) > 0 {
				out = append(out, resultsMessage(blocks))
				changed = true
			}
			pending, answered = nil, map[string]bool{}
			if len(content) > 0 && content[0] == '[' {
				eachElement(content, func(b []byte) bool {
					if typ, id, _ := blockIDs(b); typ == "tool_use" {
						pending = append(pending, id)
					}
					return true
				})
			}
			out = append(out, el)
			return true
		}
		// a user turn: the results answering the calls in hand go first,
		// each once; anything else follows as it came
		var kept, rest []json.RawMessage
		touched := false
		switch {
		case len(content) > 0 && content[0] == '[':
			eachElement(content, func(b []byte) bool {
				typ, _, useID := blockIDs(b)
				if typ != "tool_result" {
					rest = append(rest, b)
					return true
				}
				switch {
				case !open(useID):
					// answers no call of the message before: one that
					// closed already, or never was
					rest = append(rest, rawResultSaid(b)...)
					touched = true
				case answered[useID]:
					touched = true // a second answer: the first stands
				default:
					answered[useID] = true
					if len(rest) > 0 {
						touched = true // after other content: moved first
					}
					kept = append(kept, b)
				}
				return true
			})
		case len(content) > 0 && content[0] == '"':
			rest = []json.RawMessage{content}
		}
		missing := unanswered()
		if len(missing) > 0 {
			touched = true
		}
		pending, answered = nil, map[string]bool{}
		if !touched {
			out = append(out, el)
			return true
		}
		changed = true
		if len(rest) == 1 && rest[0][0] == '"' {
			// a turn of words alone: its words as a text block after the
			// results it was missing
			rest = textBlocks(rest[0])
		}
		blocks := append(append(kept, missing...), rest...)
		list := rawList(blocks)
		if content == nil {
			out = append(out, resultsMessage(blocks))
		} else {
			out = append(out, withTopValue(el, "content", list))
		}
		return true
	})
	if !valid {
		return nil, false
	}
	if blocks := unanswered(); len(blocks) > 0 {
		out = append(out, resultsMessage(blocks))
		changed = true
	}
	if !changed {
		return nil, false
	}
	return rawList(out), true
}

// rawList is a JSON array of els, each as it is.
func rawList(els []json.RawMessage) []byte {
	n := 2
	for _, el := range els {
		n += len(el) + 1
	}
	out := make([]byte, 0, n)
	out = append(out, '[')
	for i, el := range els {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, el...)
	}
	return append(out, ']')
}

// blockIDs is a content block's type, id and tool_use_id, read from its
// top-level members alone.
func blockIDs(b []byte) (typ, id, useID string) {
	if len(b) == 0 || b[0] != '{' {
		return "", "", ""
	}
	eachMember(b, func(k, v []byte) bool {
		s, ok := stringBytes(v)
		if !ok {
			return true
		}
		switch {
		case keyIs(k, "type"):
			typ = string(s)
		case keyIs(k, "id"):
			id = string(s)
		case keyIs(k, "tool_use_id"):
			useID = string(s)
		}
		return true
	})
	return typ, id, useID
}

// syntheticResult is a call's missing result, as pairToolBlocks makes it.
func syntheticResult(id string) json.RawMessage {
	b, _ := marshalPlain(map[string]any{"type": "tool_result", "tool_use_id": id, "content": interruptedTool, "is_error": true})
	return b
}

// resultsMessage is a user message of blocks.
func resultsMessage(blocks []json.RawMessage) json.RawMessage {
	return json.RawMessage(append(append([]byte(`{"role":"user","content":`), rawList(blocks)...), '}'))
}

// textBlocks is a string's words as a text block, or a stand-in for words
// there weren't: Anthropic refuses an empty text block.
func textBlocks(str []byte) []json.RawMessage {
	var s string
	if json.Unmarshal(str, &s) != nil || s == "" {
		s = "Tool result received."
	}
	b, _ := marshalPlain(map[string]string{"type": "text", "text": s})
	return []json.RawMessage{b}
}

// rawResultSaid is resultSaid of a raw tool_result block: its text as a
// text block, its blocks as they came.
func rawResultSaid(b []byte) []json.RawMessage {
	c, _ := memberOf(b, "content")
	if len(c) > 0 && c[0] == '[' {
		if els := elementsOf(c); len(els) > 0 {
			return els
		}
	}
	if len(c) > 0 && c[0] == '"' {
		var s string
		if json.Unmarshal(c, &s) == nil && strings.TrimSpace(s) != "" {
			return textBlocks(c)
		}
	}
	return textBlocks(nil)
}

// withTopValue is obj, a valid JSON object, with the value of its last
// member named key put as val, every other byte as it came. An obj with
// no such member comes back as it is.
func withTopValue(obj []byte, key string, val []byte) []byte {
	start := skipJSONSpace(obj, 0)
	raw := trimJSON(obj)
	if len(raw) == 0 || raw[0] != '{' {
		return obj
	}
	v0, v1 := -1, -1
	eachMemberAt(raw, func(k0, k1, s, e int) bool {
		if keyIs(raw[k0:k1], key) {
			v0, v1 = s, e
		}
		return true
	})
	if v0 < 0 {
		return obj
	}
	out := make([]byte, 0, len(obj)-(v1-v0)+len(val))
	out = append(out, obj[:start+v0]...)
	out = append(out, val...)
	return append(out, obj[start+v1:]...)
}
