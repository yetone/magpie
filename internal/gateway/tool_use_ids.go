package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Claude Code keeps every tool_use id of a session in one Set, and drops a
// block whose id it has already seen; a turn whose only block is dropped
// becomes the placeholder "[Tool use interrupted]" (#4910). An upstream
// that speaks Messages itself and is relayed as it came may number its
// calls' ids by their place in the reply (LiteLLM's bridge: fc_0 on every
// turn), so the id the client saw on one turn comes again on the next, and
// every later call of the session is lost. A reply whose tool_use id is one
// the client already holds — an assistant block of the request the reply
// answers, or one the reply itself said before it — goes out under the
// mark, the upstream's own id and a fresh salt (saltCallID), which nothing
// the client holds names; the answer comes back under the salted id and
// goes to the upstream as the id it gave (saltedCallIDsBack). An id the
// client has seen no more goes as it came, so an upstream whose ids are its
// own is relayed byte for byte.

// saltCallID is id for a Messages client, salted so that no two replies of
// a session make it alike: the mark and the base64url of the id, a NUL and
// the salt, which unsaltCallID turns back into the id alone.
func saltCallID(id, salt string) string {
	return anthropicIDMark + base64.RawURLEncoding.EncodeToString([]byte(id+"\x00"+salt))
}

// unsaltCallID is the id saltCallID was given, and whether it was one. An
// id that wasn't — a client's own, or one another mark holds — is none.
func unsaltCallID(id string) (string, bool) {
	if !strings.HasPrefix(id, anthropicIDMark) || !safeIDChars.MatchString(id) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id[len(anthropicIDMark):])
	if err != nil || !utf8.Valid(raw) {
		return "", false
	}
	at := bytes.IndexByte(raw, 0)
	if at <= 0 || at == len(raw)-1 {
		return "", false // no salt, or no id
	}
	return string(raw[:at]), true
}

// clientToolUseIDs is every tool_use id of a Messages request's assistant
// turns: what the client holds, and would drop a reply's block over saying
// again (#4910).
func clientToolUseIDs(body []byte) map[string]bool {
	if !bytes.Contains(body, toolUseType) {
		return nil
	}
	var q struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if decodeJSON(body, &q) != nil {
		return nil
	}
	var seen map[string]bool
	for _, m := range q.Messages {
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if decodeJSON(m.Content, &blocks) != nil {
			continue // a content of text alone, or a string
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				if seen == nil {
					seen = map[string]bool{}
				}
				seen[b.ID] = true
			}
		}
	}
	return seen
}

// saltedCallIDsBack gives a Messages request the ids the upstream made: an
// id that went out salted comes back as the client holds it, and the
// upstream that made the call gets its own again, on the call and on the
// result answering it. The rest goes as it came.
func saltedCallIDsBack(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"`+anthropicIDMark)) {
		return body
	}
	var reply map[string]json.RawMessage
	if decodeJSON(body, &reply) != nil || reply == nil {
		return body
	}
	var msgs []json.RawMessage
	if decodeJSON(reply["messages"], &msgs) != nil {
		return body
	}
	changed := false
	for i, m := range msgs {
		var one map[string]json.RawMessage
		if decodeJSON(m, &one) != nil || one == nil {
			continue
		}
		var blocks []map[string]json.RawMessage
		if decodeJSON(one["content"], &blocks) != nil {
			continue
		}
		mended := false
		for _, b := range blocks {
			for _, field := range []string{"id", "tool_use_id"} {
				var id string
				if decodeJSON(b[field], &id) != nil || id == "" {
					continue
				}
				raw, ok := unsaltCallID(id)
				if !ok {
					continue
				}
				b[field], _ = marshalPlain(raw)
				mended, changed = true, true
			}
		}
		if mended {
			one["content"], _ = marshalPlain(blocks)
			msgs[i], _ = marshalPlain(one)
		}
	}
	if !changed {
		return body
	}
	reply["messages"], _ = marshalPlain(msgs)
	nb, err := marshalPlain(reply)
	if err != nil {
		return body
	}
	return nb
}

// toolUseType is the kind of block whose id the client holds against a
// reply's own.
var toolUseType = []byte(`"tool_use"`)

// toolUseIDs salts the tool_use ids of a Messages reply relayed as it came:
// one the client already holds, or one this reply itself has said, goes out
// salted, so no block of the session names another's id again (#4910). A
// line that names no tool_use, or an id the client holds no more, passes as
// it came; a reply that isn't a stream is held whole and gone through at
// the end.
type toolUseIDs struct {
	seen map[string]bool // the client's, and this reply's own, so far
	sse  bool
	buf  []byte
}

func (t *toolUseIDs) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	out, rest := tidyEvents(t.buf, t.event)
	t.buf = rest
	return out
}

func (t *toolUseIDs) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		return t.whole(b)
	}
	// what events remain go as they came, the reply may end inside one
	out, rest := tidyEvents(b, t.event)
	return append(out, rest...)
}

func (t *toolUseIDs) event(ev []byte) []byte {
	data := eventData(ev)
	if len(data) == 0 || !bytes.Contains(data, toolUseType) || !gjson.ValidBytes(data) {
		return ev
	}
	e := gjson.ParseBytes(data)
	if e.Get("type").String() != "content_block_start" ||
		e.Get("content_block.type").String() != "tool_use" {
		return ev
	}
	id := e.Get("content_block.id").String()
	if id == "" {
		return ev
	}
	if !t.seen[id] {
		if t.seen == nil {
			t.seen = map[string]bool{}
		}
		t.seen[id] = true
		return ev
	}
	salted := saltCallID(id, newID())
	var m map[string]any
	if decodeNumbers(data, &m) != nil {
		return ev
	}
	if cb, _ := m["content_block"].(map[string]any); cb != nil {
		cb["id"] = salted
	} else {
		return ev
	}
	nd, err := marshalPlain(m)
	if err != nil {
		return ev
	}
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	t.seen[salted] = true
	return withData(ev, nd)
}

// whole is a reply that came whole: the tool_use ids of its content the
// client already holds, or an earlier block of the reply said, go out
// salted.
func (t *toolUseIDs) whole(b []byte) []byte {
	if !bytes.Contains(b, toolUseType) {
		return b
	}
	var reply map[string]json.RawMessage
	if decodeJSON(b, &reply) != nil || reply == nil {
		return b
	}
	var blocks []map[string]json.RawMessage
	if decodeJSON(reply["content"], &blocks) != nil {
		return b
	}
	changed := false
	for _, cb := range blocks {
		var typ, id string
		if decodeJSON(cb["type"], &typ) != nil || typ != "tool_use" {
			continue
		}
		if decodeJSON(cb["id"], &id) != nil || id == "" {
			continue
		}
		if !t.seen[id] {
			if t.seen == nil {
				t.seen = map[string]bool{}
			}
			t.seen[id] = true
			continue
		}
		cb["id"], _ = marshalPlain(saltCallID(id, newID()))
		changed = true
	}
	if !changed {
		return b
	}
	reply["content"], _ = marshalPlain(blocks)
	nb, err := marshalPlain(reply)
	if err != nil {
		return b
	}
	return nb
}
