package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OpenAI takes a reasoning item sent back to it only with its content
// empty ("Invalid 'input[n].content': array too long", #1411) and cannot
// open a seal that is not its own. A client stores what magpie hands it
// and replays it when the conversation goes back to OpenAI, on magpie or
// off it: an item another vendor wrote with its text in it would stick the
// conversation there (#2855, the reporter's rollout: 25 reasoning items a
// DeepSeek phase had left, each with its text in content parts and a
// 38-character encrypted_content of the vendor's own, against the
// thread's own GPT-phase items, which carry none). So an item that carries
// its text goes to the client as one magpie makes: the text a summary,
// nothing sealed.

// reasoningWithText is whether a Responses reasoning item carries its
// reasoning as content parts (reasoning_text), another vendor's shape:
// OpenAI's own carry summaries.
func reasoningWithText(item map[string]any) bool {
	if t, _ := item["type"].(string); t != "reasoning" {
		return false
	}
	parts, _ := item["content"].([]any)
	return len(parts) > 0
}

// magpieMadeReasoning is the reasoning item magpie makes of one a
// Responses upstream wrote with its text in it, nil of one that carries no
// content, which goes as it came. Its text (else its summaries) becomes
// one summary of magpie's; its content and its seal go, the text kept, so
// an upstream that wants its reasoning back is sent it (withReasoningText).
// Its own id is kept, the events of a stream naming the item by it.
func magpieMadeReasoning(item map[string]any) map[string]any {
	if !reasoningWithText(item) {
		return nil
	}
	// the text, else the summaries: a vendor that writes both may write
	// the same words twice (#2855's later rollout: each item's content
	// the summary over again), and the text is what a vendor that wants
	// its reasoning back reads
	text := func(key, typ string) string {
		var b strings.Builder
		parts, _ := item[key].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if t, _ := pm["type"].(string); typ != "" && t != typ {
				continue
			}
			s, _ := pm["text"].(string)
			b.WriteString(s)
		}
		return b.String()
	}
	said := text("content", "reasoning_text")
	if said == "" {
		said = text("summary", "")
	}
	id, _ := item["id"].(string)
	if id == "" {
		id = "rs_" + newID()
	}
	return map[string]any{"id": id, "type": "reasoning", "status": "completed",
		"summary": []map[string]any{{"type": "summary_text", "text": said}}}
}

// magpieMadeReasoningRaw is the item's bytes, made over when the item
// carries its text; id, when not empty, is the id the item's events have
// already carried, which its done event must keep.
func magpieMadeReasoningRaw(raw json.RawMessage, id string) json.RawMessage {
	var it map[string]any
	if decodeJSON(raw, &it) != nil {
		return raw
	}
	made := magpieMadeReasoning(it)
	if made == nil {
		return raw
	}
	if id != "" {
		made["id"] = id
	}
	b, err := json.Marshal(made)
	if err != nil {
		return raw
	}
	return b
}

// reasoningTidy makes a Responses upstream's reasoning items that carry
// their text in it into the items magpie makes, in a reply relayed as it
// came, its events' sequence numbers and every other line kept as the
// upstream wrote them. A reply that isn't a stream is gone through whole.
type reasoningTidy struct {
	sse bool
	buf []byte
}

func (t *reasoningTidy) write(b []byte) []byte {
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

func (t *reasoningTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := t.event(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *reasoningTidy) lines(b []byte) []byte {
	if !bodyHas(b, `"reasoning"`) {
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
		if !ok || !bodyHas(data, `"reasoning"`) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.event(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// event is an SSE event's data, or a whole reply, with its reasoning items
// that carry their text made into magpie's, and whether one was.
func (t *reasoningTidy) event(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	changed := madeOver(ev["item"])
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, it := range out {
			if madeOver(it) {
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return nil, false
	}
	return nb, true
}

// madeOver makes item, a reasoning item with its text in it, one of
// magpie's, in place, and reports whether it was.
func madeOver(item any) bool {
	im, _ := item.(map[string]any)
	made := magpieMadeReasoning(im)
	if made == nil {
		return false
	}
	for k := range im {
		delete(im, k)
	}
	for k, v := range made {
		im[k] = v
	}
	return true
}
