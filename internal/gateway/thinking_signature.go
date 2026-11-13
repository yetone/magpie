package gateway

import (
	"bytes"
	"encoding/json"
)

// A Messages thinking block always says its signature. Anthropic's own API
// writes one on every thinking block: `"signature": ""` where a stream's
// block starts (its signature_delta comes before the block's stop), and
// the whole signature in a reply that came whole. Clients read the field as
// required: Grok Build's ContentBlock::Thinking has two fields, thinking
// and signature, and serde turns the whole reply away without the second,
// "Couldn't read the response: serialization error: missing field
// `signature`" (uk.jent on Discord, minimax-code-global/MiniMax-M3.1-Flash).
//
// A thinking block magpie writes for a Messages client therefore carries
// one: the encoder's (anthropicEncoder.thinking, renderAnthropic), the
// whole-to-stream path's (whole_stream.go), and, through sigFieldTidy, a
// block another vendor's Messages endpoint wrote without one, relayed as it
// came. The signature magpie adds is always empty, never made up: an empty
// signature is no signature to every path that sends history on.
// anthropicBody leaves an unsigned thinking block out for Anthropic and
// keeps its text only where the upstream wants reasoning back (DeepSeek,
// which takes the empty field, as aBlock already writes it); a relay sends
// it back to the vendor that wrote it, as the client keeps it.

var thinkingType = []byte(`"thinking"`)

// sigFieldTidy gives each thinking block of a Messages reply relayed as it
// came an empty signature where it has none: a stream's content_block_start
// lines, or a whole reply's content. A line that names no thinking, or that
// has a signature already (Anthropic's), passes as it came; a reply that
// isn't a stream is held whole and gone through at the end.
type sigFieldTidy struct {
	sse bool
	buf []byte
}

func (t *sigFieldTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *sigFieldTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := signedContent(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *sigFieldTidy) lines(b []byte) []byte {
	if !bodyHasBytes(b, thinkingType) {
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
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !bodyHasBytes(data, thinkingType) || !bodyHas(data, "content_block_start") {
			out = append(out, line...)
			continue
		}
		nb, ok := signedStart(bytes.TrimSpace(data))
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// signedStart is a content_block_start event opening a thinking block with
// no signature, given an empty one, and whether it was such an event.
func signedStart(data []byte) ([]byte, bool) {
	var ev map[string]json.RawMessage
	if json.Unmarshal(data, &ev) != nil {
		return nil, false
	}
	var typ string
	if json.Unmarshal(ev["type"], &typ) != nil || typ != "content_block_start" {
		return nil, false
	}
	block, ok := unsignedThinking(ev["content_block"])
	if !ok {
		return nil, false
	}
	ev["content_block"] = block
	nb, err := marshalPlain(ev)
	return nb, err == nil
}

// signedContent is a whole Messages reply whose thinking blocks without a
// signature are given an empty one, and whether it had such a block.
func signedContent(b []byte) ([]byte, bool) {
	if !bodyHasBytes(b, thinkingType) {
		return nil, false
	}
	var reply map[string]json.RawMessage
	if json.Unmarshal(b, &reply) != nil {
		return nil, false
	}
	var content []json.RawMessage
	if json.Unmarshal(reply["content"], &content) != nil {
		return nil, false
	}
	changed := false
	for i, c := range content {
		if nb, ok := unsignedThinking(c); ok {
			content[i], changed = nb, true
		}
	}
	if !changed {
		return nil, false
	}
	cb, err := marshalPlain(content)
	if err != nil {
		return nil, false
	}
	reply["content"] = cb
	nb, err := marshalPlain(reply)
	return nb, err == nil
}

// unsignedThinking is a thinking block that has no signature, given an
// empty one, and whether it was one. A signature that isn't a string (a
// null) is none too.
func unsignedThinking(raw json.RawMessage) (json.RawMessage, bool) {
	if !bodyHasBytes(raw, thinkingType) {
		return nil, false
	}
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil {
		return nil, false
	}
	var typ string
	if json.Unmarshal(block["type"], &typ) != nil || typ != "thinking" {
		return nil, false
	}
	if s := bytes.TrimSpace(block["signature"]); len(s) > 0 && s[0] == '"' {
		return nil, false
	}
	block["signature"] = json.RawMessage(`""`)
	if _, ok := block["thinking"]; !ok {
		block["thinking"] = json.RawMessage(`""`)
	}
	nb, err := marshalPlain(block)
	return nb, err == nil
}
