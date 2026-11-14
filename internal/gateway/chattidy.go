package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
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
// And an upstream that sometimes sends a tool call's first fragment with
// no id (OpenCode Zen's free DeepSeek) has the agent keep the call as "",
// which a strict upstream it is sent to later turns away (#2611): such a
// fragment is given an id, and a later fragment's empty one is left out.
//
// And a choice that made calls and ended "stop" ends "tool_calls": told
// it stopped, a client that reads finish_reason ends its turn there with
// the calls not run (喜欢一个人 on Discord, a relay in front of
// deepseek-v4.1-flash), as the translations' collector already has it.
//
// And Gemini's OpenAI-compatible API sends its tool calls with no "index"
// (Linka's frames in gemini_compat_cut_test.go), where the contract has
// one on every fragment. The AI SDK's OpenAI provider, which Cursor's
// Private Inference reads Chat with, turns such a chunk away ("expected
// number, received undefined" at tool_calls[0].index), and the call is
// never made (#3386). A fragment with none is given its call's: a new id,
// or a later place in its delta, is the next call.
//
// And a usage that tells the cache only in another vendor's names
// (Anthropic's cache_read_input_tokens from a relay in front of a model,
// DeepSeek's prompt_cache_hit_tokens, Mistral's num_cached_tokens or
// Moonshot's cached_tokens beside a 0 in prompt_tokens_details) is told in OpenAI's too (openAIUsage): the
// gateway counted the hit, but an agent on pi-ai (DeepSeek Harness), which
// reads prompt_tokens_details.cached_tokens first, showed none.
//
// And a call whose first fragment names no function in a JSON string —
// no name, null, another kind — is dropped, its later fragments with it,
// and a finish of tool_calls with no call left ends stop (#1475): a
// client that checks the shape of every frame refuses the chunk for it
// and the whole reply with it, the turn not retryable and the call
// already in the history, so the session is dead — while the gateway's
// own log says the reply went (AricRedemption). A nameless call runs
// nowhere, so it is no call, as a written block naming no offered tool
// is; a name of another kind on a later fragment is left out, the first
// one's kept. And a finish of tool_calls no delta ever carried a call
// for (#4944, Hermes: a reply of text alone whose upstream ends it
// "tool_calls" all the same) ends stop too — a client that reads
// finish_reason took the turn for one whose calls were dropped and
// re-prompted, three times a reply.
//
// And a legacy function_call that carries nothing — a name and arguments
// both empty — is left out too (#4659): WorkBuddy's stream ends a reply
// of plain text on a frame with "function_call":{"name":"","arguments":""}
// where the contract has null, and a client that checks the shape of
// every frame reads the empty object for a legacy call and refuses the
// whole turn, so every text reply a tools-carrying request got was a
// turn lost (jinccc-ctrl). An empty one runs nowhere, as a nameless call
// is no call; a null goes as it came, and a call whose name or arguments
// hold something is one a client may still be reading.
//
// Lines pass whole, as they came, unless one has any of them.
type chatTidy struct {
	buf     []byte
	ids     streamCallIDs
	indexes streamCallIndexes
	called  map[int]bool    // the choices that made a call
	named   map[int]bool    // the choices with a call whose name a client reads
	seen    map[[2]int]bool // the streamed calls, to know a first fragment
	bad     map[[2]int]bool // the calls naming no function, with their later fragments
}

var (
	emptyReasoning    = []byte(`"reasoning_content":""`)
	emptyFinishReason = []byte(`"finish_reason":""`)
	emptyToolName     = []byte(`"name":""`)
	emptyToolCalls    = []byte(`"tool_calls":[]`)
	contentParts      = []byte(`"content":[`)
	contentPartsSpace = []byte(`"content": [`)
	toolCallsField    = []byte(`"tool_calls"`)
	functionCallField = []byte(`"function_call":{`)
)

// write takes what was read and gives back what to send on: every line
// that is complete, mended.
func (t *chatTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

// flush is what's left once the stream ends.
func (t *chatTidy) flush() []byte {
	out := t.lines(t.buf)
	t.buf = nil
	return out
}

func (t *chatTidy) lines(b []byte) []byte {
	if !t.needs(b) {
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
		out = append(out, t.line(line)...)
	}
	return out
}

// needsTidy reports whether a line carries any of the shapes chatTidy
// mends.
func needsTidy(line []byte) bool {
	return bodyHasBytes(line, emptyReasoning) ||
		bodyHasBytes(line, emptyFinishReason) ||
		bodyHasBytes(line, emptyToolName) ||
		bodyHasBytes(line, emptyToolCalls) ||
		bodyHasBytes(line, contentParts) ||
		bodyHasBytes(line, contentPartsSpace) ||
		bodyHasBytes(line, toolCallsField) ||
		bodyHasBytes(line, functionCallField)
}

// needs is needsTidy, a finish_reason once a choice made a call, and a
// usage that may tell its cache in names OpenAI's clients don't read.
func (t *chatTidy) needs(b []byte) bool {
	return needsTidy(b) || len(t.called) > 0 && bodyHasBytes(b, finishReasonField) || mayMendUsage(b)
}

var (
	usageField        = []byte(`"usage"`)
	cachedTokensField = []byte(`"cached_tokens"`)
	// the names other than OpenAI's that cUsage reads a cache in
	otherCacheFields = [][]byte{[]byte(`"prompt_cache_hit_tokens"`), []byte(`"num_cached_tokens"`),
		[]byte(`"cache_read_input_tokens"`), []byte(`"cache_creation_input_tokens"`)}
)

// mayMendUsage reports whether b has a usage that may tell its cache in
// names other than OpenAI's, which openAIUsage may have to tell again:
// DeepSeek's, Mistral's or Anthropic's, or Moonshot's top-level cached_tokens beside
// prompt_tokens_details' own. A usage in OpenAI's names alone, every
// OpenAI reply, is left unread.
func mayMendUsage(b []byte) bool {
	if !bodyHasBytes(b, usageField) {
		return false
	}
	for _, f := range otherCacheFields {
		if bodyHasBytes(b, f) {
			return true
		}
	}
	return bytes.Count(b, cachedTokensField) > 1
}

// openAIUsage is a Chat usage whose cache cUsage reads in names other than
// OpenAI's (Anthropic's from a relay, DeepSeek's prompt_cache_hit_tokens,
// Mistral's num_cached_tokens, Moonshot's cached_tokens), told in OpenAI's as well: what was read in
// prompt_tokens_details.cached_tokens, what was written in its
// cache_write_tokens (OpenRouter's), and prompt_tokens counting both, as
// OpenAI's does. An agent reads prompt_tokens_details first, pi-ai (DeepSeek
// Harness) even a 0 there over DeepSeek's hit, and the AI SDK nothing else.
// Every field the upstream sent stays; a usage that already says it in
// OpenAI's names, or tells no cache, comes back as it was, and so does one
// that isn't an object.
func openAIUsage(raw json.RawMessage) (json.RawMessage, bool) {
	var cu cUsage
	if decodeJSON(raw, &cu) != nil {
		return raw, false
	}
	u := cu.usage()
	if u.CacheRead == 0 && u.CacheWrite == 0 {
		return raw, false
	}
	in := u.prompt()
	if d := cu.PromptTokensDetails; d != nil && d.CachedTokens == u.CacheRead && d.CacheWriteTokens == u.CacheWrite && cu.PromptTokens == in {
		return raw, false
	}
	var m map[string]json.RawMessage
	if decodeJSON(raw, &m) != nil || m == nil {
		return raw, false
	}
	var details map[string]json.RawMessage
	if decodeJSON(m["prompt_tokens_details"], &details) != nil || details == nil {
		details = map[string]json.RawMessage{}
	}
	details["cached_tokens"], _ = marshalPlain(u.CacheRead)
	if _, had := details["cache_write_tokens"]; had || u.CacheWrite > 0 {
		details["cache_write_tokens"], _ = marshalPlain(u.CacheWrite)
	}
	m["prompt_tokens_details"], _ = marshalPlain(details)
	if cu.PromptTokens != in {
		m["prompt_tokens"], _ = marshalPlain(in)
		if _, had := m["total_tokens"]; had {
			m["total_tokens"], _ = marshalPlain(cu.TotalTokens + in - cu.PromptTokens)
		}
	}
	out, err := marshalPlain(m)
	if err != nil {
		return raw, false
	}
	return out, true
}

var finishReasonField = []byte(`"finish_reason"`)

// tidyLine mends one line as the first of a stream.
func tidyLine(line []byte) []byte { return (&chatTidy{}).line(line) }

func (t *chatTidy) line(line []byte) []byte {
	if !t.needs(line) {
		return line
	}
	body := bytes.TrimRight(line, "\r\n")
	end := line[len(body):]
	data, ok := bytes.CutPrefix(body, []byte("data:"))
	if !ok {
		return line
	}
	var chunk map[string]json.RawMessage
	if decodeJSON(data, &chunk) != nil || chunk == nil {
		return line
	}
	usageMended := false
	if raw, ok := chunk["usage"]; ok {
		chunk["usage"], usageMended = openAIUsage(raw)
	}
	var choices []map[string]json.RawMessage
	if decodeJSON(chunk["choices"], &choices) != nil {
		choices = nil
		if !usageMended {
			return line
		}
	}
	changed := false
	for k, c := range choices {
		if string(c["finish_reason"]) == `""` {
			c["finish_reason"] = json.RawMessage("null")
			changed = true
		}
		choice := k
		decodeJSON(c["index"], &choice)
		var delta map[string]json.RawMessage
		if decodeJSON(c["delta"], &delta) != nil {
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
		}
		if fc, ok := delta["function_call"]; ok {
			if fullFunctionCall(fc) {
				// a legacy call a client may still be reading (#4659)
				// keeps a finish that says the turn ended in a call
				if t.named == nil {
					t.named = map[int]bool{}
				}
				t.named[choice] = true
			} else if emptyFunctionCall(fc) {
				delete(delta, "function_call")
				inDelta = true
			}
		}
		if calls, ok := delta["tool_calls"]; ok {
			if t.indexes == nil {
				t.indexes = streamCallIndexes{}
			}
			if given, chg := t.indexes.give(choice, calls); chg {
				calls, delta["tool_calls"] = given, given
				inDelta = true
			}
			if t.called == nil {
				t.called = map[int]bool{}
			}
			t.called[choice] = true
			if kept, chg := t.nameless(choice, calls); chg {
				if kept == nil {
					delete(delta, "tool_calls")
					// the fragments went with the drop: none is left for the
					// id mend to give an id to, or the dropped call would
					// come back as one with a fresh id and no name (#1475)
					calls = nil
				} else {
					calls, delta["tool_calls"] = kept, kept
				}
				inDelta = true
			}
			if calls != nil {
				if t.ids == nil {
					t.ids = streamCallIDs{}
				}
				if mended, chg := t.ids.mend(choice, calls); chg {
					calls, delta["tool_calls"] = mended, mended
					inDelta = true
				}
				if mended, chg := withoutEmptyNames(calls); chg {
					delta["tool_calls"] = mended
					inDelta = true
				}
			}
		}
		if inDelta {
			c["delta"], _ = json.Marshal(delta)
			changed = true
		}
		switch end := string(bytes.TrimSpace(c["finish_reason"])); end {
		case `"stop"`:
			if t.called[choice] && t.named[choice] {
				c["finish_reason"] = json.RawMessage(`"tool_calls"`)
				changed = true
			}
		case `"tool_calls"`:
			if !t.named[choice] {
				// the call the finish named named no function, or no call
				// was ever made and no delta carried the key (#4944): it
				// ends stop, as a reply that made no call (#1475)
				c["finish_reason"] = json.RawMessage(`"stop"`)
				changed = true
			}
		}
	}
	if !changed && !usageMended {
		return line
	}
	if changed {
		chunk["choices"], _ = json.Marshal(choices)
	}
	nb, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
	return append(append([]byte("data: "), nb...), end...)
}

// emptyFunctionCall reports whether raw is a legacy function_call
// carrying nothing: a name and arguments both empty strings or nulls,
// either absent (#4659). A null is the contract's own nothing and is not
// this; a name or arguments of another kind than a string is not read,
// and the call goes as it came.
func emptyFunctionCall(raw json.RawMessage) bool {
	if string(bytes.TrimSpace(raw)) == "null" {
		return false
	}
	var fc struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	return decodeJSON(raw, &fc) == nil && fc.Name == "" && fc.Arguments == ""
}

// fullFunctionCall reports whether raw is a legacy function_call a client
// may still be reading: a name or arguments that hold something (#4659).
// A null is the contract's own nothing and an unreadable shape is no call
// a client reads.
func fullFunctionCall(raw json.RawMessage) bool {
	if string(bytes.TrimSpace(raw)) == "null" {
		return false
	}
	var fc struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	return decodeJSON(raw, &fc) == nil && (fc.Name != "" || fc.Arguments != "")
}

// withoutEmptyNames drops the empty "name" on a delta's tool-call
// fragments and reports whether any was dropped. A fragment whose
// function carries no name at all is left as it is; one with a real name
// keeps it.
func withoutEmptyNames(raw json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	if decodeJSON(raw, &calls) != nil {
		return raw, false
	}
	changed := false
	for _, call := range calls {
		var fn map[string]json.RawMessage
		if decodeJSON(call["function"], &fn) != nil {
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

// streamCallIndexes is what chatTidy keeps to give a streamed tool call
// the upstream sent no index for one: each choice's open call.
type streamCallIndexes map[int]*openCall

// openCall is a choice's open tool call: its id, the index it goes by,
// and the next index to give.
type openCall struct {
	open        bool
	id          string
	index, next int
	// an upstream index's call: the index it goes by and its id, and the
	// indexes given, for a stream that sends a new call on an index an
	// earlier call has (renumber)
	up   map[int]int
	upID map[int]string
	used map[int]bool
}

// renumber is the index a fragment the upstream sent at n goes by. An
// upstream that turns Responses into Chat itself (OpenDesign's
// amr-link, jiezhengj on Discord) sometimes sends a second parallel call
// on the first one's index, 0,0,1 for three: a client that adds up a
// call's fragments by index (OpenAI's and Vercel's SDKs) wrote the two
// calls' arguments into one, invalid JSON, and the turn ended. A new id
// on an index another call has, and each later fragment at that index,
// goes by the next free one; the calls after it, by one of their own.
// Only a fragment that names its function starts a call: one that
// carries only arguments goes on with its index's call, whatever id it
// has. The calls are numbered 0, 1, 2 in the order they start, whatever
// the upstream numbered them by: one that passes Responses' output_index
// on starts its first call at 1 when a text item took 0 (1,1,2,3,
// hixonchan on X), and a client that keeps calls in an array by index
// broke on it. A stream already numbered from 0 in order goes as it came.
func (o *openCall) renumber(n int, id string, starts bool) int {
	if o.up == nil {
		o.up, o.upID, o.used = map[int]int{}, map[int]string{}, map[int]bool{}
	}
	out, known := o.up[n]
	if !known || starts && id != "" && o.upID[n] != "" && id != o.upID[n] {
		// the calls count from 0 in the order they start, whatever
		// index the upstream numbered them by: a Responses
		// output_index has the reply's text at 0, and the first call
		// at 1 (#4900)
		out = o.next
		o.next++
		o.up[n], o.used[out] = out, true
	}
	if id != "" {
		o.upID[n] = id
	}
	return out
}

// give gives each fragment of a delta's tool_calls with no index (none,
// or null) the index of the call it belongs to, and reports whether it
// gave any. A fragment with an id other than the open call's, or after
// the first in its delta, starts the next call, as chatDecoder reads a
// call with a new id; any other goes on with the open one. So two calls
// in chunks of their own are 0 and 1: given both 0, by their place, OpenAI's
// node SDK merged them into one call. A fragment with an index keeps it,
// unless another call had it first (renumber), and the next one given
// comes after it.
func (s streamCallIndexes) give(choice int, raw json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	if decodeJSON(raw, &calls) != nil {
		return raw, false
	}
	o := s[choice]
	if o == nil {
		o = &openCall{}
		s[choice] = o
	}
	changed := false
	for k, c := range calls {
		id := callID(c["id"])
		if v, ok := c["index"]; ok && string(v) != "null" {
			var n int
			if decodeJSON(v, &n) == nil {
				out := o.renumber(n, id, namesFunction(c["function"]))
				o.open, o.id, o.index = true, cmp.Or(id, o.id), out
				o.next = max(o.next, out+1)
				if out != n {
					c["index"], _ = marshalPlain(out)
					changed = true
				}
			}
			continue
		}
		if !o.open || k > 0 || id != "" && id != o.id {
			o.open, o.id, o.index = true, id, o.next
			o.next++
			if o.used == nil {
				o.up, o.upID, o.used = map[int]int{}, map[int]string{}, map[int]bool{}
			}
			o.used[o.index] = true
		}
		c["index"], _ = marshalPlain(o.index)
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

// namesFunction reports whether a call fragment's function has a name.
func namesFunction(raw json.RawMessage) bool {
	var fn struct {
		Name string `json:"name"`
	}
	return decodeJSON(raw, &fn) == nil && fn.Name != ""
}

// nameless drops the fragments of a streamed call whose first one names
// no function in a JSON string — no name, null, another kind — with the
// later fragments of the same call (#1475): a client that checks the
// shape of every frame refuses the chunk for it and the whole reply with
// it, the turn not retryable and the call already in the history, so the
// session is dead. A nameless call runs nowhere, so it is no call, as a
// written block naming no offered tool is. A name of another kind on a
// later fragment only loses the field, the first one's kept. It reports
// the fragments kept, nil when none, and whether any was dropped.
func (t *chatTidy) nameless(choice int, raw json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	if decodeJSON(raw, &calls) != nil {
		return raw, false
	}
	var kept []map[string]json.RawMessage
	changed := false
	for _, call := range calls {
		idx := 0
		decodeJSON(call["index"], &idx)
		key := [2]int{choice, idx}
		if t.bad[key] {
			changed = true
			continue
		}
		first := !t.seen[key]
		if t.seen == nil {
			t.seen = map[[2]int]bool{}
		}
		t.seen[key] = true
		var fn map[string]json.RawMessage
		var name json.RawMessage
		var has bool
		if decodeJSON(call["function"], &fn) == nil {
			name, has = fn["name"]
		}
		switch {
		case has && aName(name):
			if t.named == nil {
				t.named = map[int]bool{}
			}
			t.named[choice] = true
			kept = append(kept, call)
		case has && !first:
			// it would write itself over the name the first fragment
			// carried, as an empty one would
			delete(fn, "name")
			call["function"], _ = json.Marshal(fn)
			kept = append(kept, call)
			changed = true
		default:
			if !first {
				// a later fragment: the name went with the first
				kept = append(kept, call)
				continue
			}
			// no name the client reads, and a call's name goes with its
			// first fragment: no fragment of this call will name it
			if t.bad == nil {
				t.bad = map[[2]int]bool{}
			}
			t.bad[key] = true
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	if kept == nil {
		return nil, true
	}
	out, err := json.Marshal(kept)
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
	if decodeJSON(raw, &parts) != nil {
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
			if decodeJSON(p.Thinking, &s) == nil {
				kb.WriteString(s)
				continue
			}
			var inner []struct {
				Text string `json:"text"`
			}
			decodeJSON(p.Thinking, &inner)
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
		decodeJSON(m["reasoning_content"], &had)
		m["reasoning_content"], _ = json.Marshal(had + think)
	}
	return true
}

// chatWhole mends a Chat Completions reply that came whole, not streamed:
// a message whose content is typed parts (Mistral's thinking, #483) goes
// on with a string content and its thinking in reasoning_content, and,
// when the request offered tools (names), calls the model wrote into its
// text are made the calls they are, as textCallTidy makes them in a
// stream (#1845), and a call the upstream gave no id is given one, as
// chatTidy gives it in a stream (#2611), and a usage that tells its cache
// in other names is told in OpenAI's too (openAIUsage). It is held until it
// ends; any other reply passes byte for byte.
type chatWhole struct {
	names map[string]bool
	buf   []byte
}

func (t *chatWhole) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	return nil
}

func (t *chatWhole) flush() []byte {
	b := t.buf
	t.buf = nil
	parts := bodyHasBytes(b, contentParts) || bodyHasBytes(b, contentPartsSpace)
	if !parts && !mayWriteCalls(b, t.names) && !bodyHasBytes(b, toolCallsField) && !mayMendUsage(b) {
		return b
	}
	var reply map[string]json.RawMessage
	if decodeJSON(b, &reply) != nil || reply == nil {
		return b
	}
	usageMended := false
	if raw, ok := reply["usage"]; ok {
		reply["usage"], usageMended = openAIUsage(raw)
	}
	var choices []map[string]json.RawMessage
	if decodeJSON(reply["choices"], &choices) != nil {
		choices = nil
		if !usageMended {
			return b
		}
	}
	changed := false
	for _, c := range choices {
		var msg map[string]json.RawMessage
		if decodeJSON(c["message"], &msg) != nil {
			continue
		}
		mended := parts && flattenContent(msg, true)
		// a call the upstream gave no id (#2611)
		mended = wholeCallIDs(msg) || mended
		// a call naming no function runs nowhere and no client can read
		// it (#1475)
		dropped := wholeNameless(msg)
		mended = dropped || mended
		if wrote, called := wholeTextCalls(msg, t.names); wrote {
			if called {
				c["finish_reason"] = json.RawMessage(`"tool_calls"`)
			}
			mended = true
		}
		// calls the upstream made, ended "stop" (chatTidy)
		var native []json.RawMessage
		if string(bytes.TrimSpace(c["finish_reason"])) == `"stop"` && decodeJSON(msg["tool_calls"], &native) == nil && len(native) > 0 {
			c["finish_reason"] = json.RawMessage(`"tool_calls"`)
			changed = true
		}
		// the call the finish named naming no function, it ends stop, as a
		// reply that made no call (#1475)
		if dropped && string(bytes.TrimSpace(c["finish_reason"])) == `"tool_calls"` && !wholeNamedCall(msg) {
			c["finish_reason"] = json.RawMessage(`"stop"`)
			changed = true
		}
		if !mended {
			continue
		}
		c["message"], _ = marshalPlain(msg)
		changed = true
	}
	if !changed && !usageMended {
		return b
	}
	if changed {
		reply["choices"], _ = marshalPlain(choices)
	}
	nb, err := marshalPlain(reply)
	if err != nil {
		return b
	}
	return nb
}

// messageReasoning are the fields a Chat client sends an earlier
// assistant turn's thinking back in: reasoning_content (DeepSeek's, which
// OpenCode and most clients send), reasoning (OpenRouter's) and
// reasoning_details (OpenRouter's structured one). DeepSeek, Kimi and the
// like want it back; Mistral turns each away as extra_forbidden (#494).
var messageReasoning = []string{"reasoning_content", "reasoning", "reasoning_details"}

// reasoningText is the thinking a message carries in messageReasoning:
// reasoning_content, else reasoning, else reasoning_details' text.
func reasoningText(m map[string]json.RawMessage) string {
	for _, f := range messageReasoning[:2] {
		var s string
		if decodeJSON(m[f], &s) == nil && s != "" {
			return s
		}
	}
	var details []struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
	}
	decodeJSON(m["reasoning_details"], &details)
	var b strings.Builder
	for _, d := range details {
		if d.Text != "" {
			b.WriteString(d.Text)
		} else {
			b.WriteString(d.Summary)
		}
	}
	return b.String()
}

// noReasoning is the reasoning_content a tool call goes to an upstream
// that wants it back (chatReplaysReasoning) with, when the client has none
// for it: a turn another model made, or one whose thinking the client
// didn't keep. DeepSeek turns the whole request away when a tool call of
// the turn being continued has none ("The `reasoning_content` in the
// thinking mode must be passed back to the API", #1462). Its Chat API
// takes an empty one, but a relay that leaves empty fields out would send
// none, and its Responses API turns an empty reasoning_text away, so it is
// a space.
const noReasoning = " "

// withReasoningOnToolTurns gives each assistant message of a Chat request
// that has tool calls, or that follows a tool message of its turn, and no
// reasoning_content one: the thinking it sent back in another field
// (reasoning, reasoning_details), else noReasoning. DeepSeek turns away a
// reply after a tool result with none as it does a call (checked against
// api.deepseek.com on 2026-10-12, #1822). Thinking taken from reasoning
// goes in reasoning_content alone (asReasoningContent). A message with
// reasoning_content of its own keeps it, any other reply is left as it is
// (DeepSeek doesn't ask for its thinking), and a body with no tool calls
// comes back byte for byte.
func withReasoningOnToolTurns(body []byte) []byte {
	afterTool := false
	return editMessages(body, []string{"tool_calls"}, func(m map[string]json.RawMessage) bool {
		var role string
		var calls []json.RawMessage
		decodeJSON(m["role"], &role)
		switch role {
		case "tool":
			afterTool = true
		case "user":
			afterTool = false
		}
		if raw, ok := m["tool_calls"]; ok && decodeJSON(raw, &calls) != nil {
			return false
		}
		if role != "assistant" || len(calls) == 0 && !afterTool {
			return false
		}
		var own *string
		if decodeJSON(m["reasoning_content"], &own) == nil && own != nil {
			return false
		}
		think := reasoningText(m)
		if think == "" {
			think = noReasoning
		}
		asReasoningContent(m, think)
		return true
	})
}

// asReasoningContent puts think in a message's reasoning_content and
// leaves its reasoning out: a server that reads the two as one field (a
// self-hosted DeepSeek, as pi sends its thinking back in reasoning) turns
// a message with both away as "duplicate field reasoning", so the thinking
// goes once. reasoning_details stays: it is a list of typed entries, not
// another name for the text, and can hold what the text can't (an
// encrypted entry, a signature); an upstream that refuses it has it taken
// out (reasoningDetailsAsContent, withoutMessageReasoning).
func asReasoningContent(m map[string]json.RawMessage, think string) {
	m["reasoning_content"], _ = marshalPlain(think)
	delete(m, "reasoning")
}

// silentAssistantsOut is withoutSilentAssistants for a Chat request
// relayed as the client sent it (#2599): an agent that speaks Chat keeps
// the turn it cut off as {"role":"assistant","content":""} (or null, with
// reasoning_content alone) and sends it again on every later turn. A body
// without such a turn comes back byte for byte.
func silentAssistantsOut(body []byte) []byte {
	if !bodyHas(body, `"assistant"`) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil || !anySilentAssistant(q["messages"]) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(q["messages"]))
	dec.UseNumber()
	var msgs []map[string]any
	if dec.Decode(&msgs) != nil {
		return body
	}
	msgs, changed := withoutSilentAssistants(msgs)
	if !changed {
		return body
	}
	q["messages"], _ = marshalPlain(msgs)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// anySilentAssistant says whether a Chat request's messages hold a turn
// silentAssistant leaves out, decoding only the assistant turns to see.
// silentAssistantsOut runs on every attempt of every Chat request relayed
// as sent, and almost none has such a turn: decoding every message to
// look made it the gateway's largest allocation after the body itself
// (~4 MB a request for a 1.5 MB conversation, yg6888 on X).
func anySilentAssistant(msgs json.RawMessage) bool {
	if len(msgs) == 0 || msgs[0] != '[' { // valid: decodeJSON checked the body it is in
		return false
	}
	found := false
	eachElement(msgs, func(el []byte) bool {
		if el[0] != '{' {
			return true
		}
		assistant, said := false, false
		eachMember(el, func(key, val []byte) bool {
			switch {
			case jsonStringIs(key, "role"):
				assistant = jsonStringIs(val, "assistant")
			case jsonStringIs(key, "content"):
				said = textSaysSomething(val)
			}
			return true
		})
		if !assistant || said {
			return true // a turn with text in it is never left out
		}
		dec := json.NewDecoder(bytes.NewReader(el))
		dec.UseNumber()
		var m map[string]any
		found = dec.Decode(&m) == nil && silentAssistant(m)
		return !found
	})
	return found
}

// textSaysSomething says whether q, a valid JSON value, is a string with
// an ASCII character in it that isn't a space: text strings.TrimSpace
// leaves. Escapes are passed over, so "\n" and an escaped U+3000 say
// nothing here, and what it can't tell is false, for silentAssistant to
// look at.
func textSaysSomething(q []byte) bool {
	if len(q) < 2 || q[0] != '"' {
		return false
	}
	for i := 1; i < len(q)-1; i++ {
		switch c := q[i]; {
		case c == '\\':
			if q[i+1] == 'u' {
				i += 5
			} else {
				i++
			}
		case c > ' ' && c < 0x7f:
			return true
		}
	}
	return false
}

// jsonStringIs says whether q, a JSON value, is the string s, unquoting
// only a string with an escape in it.
func jsonStringIs(q []byte, s string) bool {
	if len(q) == len(s)+2 && q[0] == '"' && string(q[1:len(q)-1]) == s {
		return true
	}
	if len(q) == 0 || q[0] != '"' || bytes.IndexByte(q, '\\') < 0 {
		return false
	}
	u, ok := unquoteJSON(q)
	return ok && u == s
}

// reasoningDetailsAsContent leaves reasoning_details out of every message
// of a Chat request, its thinking kept in reasoning_content when the
// message has none: Kimi's API turns reasoning_details away in whatever
// shape it comes, OpenRouter's reasoning.text with a stream's index in it
// (as another vendor's turn has it) or without ("the reasoning_details at
// position 2 entry 0 must not contain streaming index", "… has an invalid
// type", #1765), and takes reasoning_content.
func reasoningDetailsAsContent(body []byte) []byte {
	return editMessages(body, []string{"reasoning_details"}, func(m map[string]json.RawMessage) bool {
		if _, ok := m["reasoning_details"]; !ok {
			return false
		}
		var own string
		if decodeJSON(m["reasoning_content"], &own) != nil || own == "" {
			if think := reasoningText(m); think != "" {
				asReasoningContent(m, think)
			}
		}
		delete(m, "reasoning_details")
		return true
	})
}

// withoutMessageReasoning leaves the thinking fields an upstream refused
// out of every message of a Chat request; reasoning_details' thinking
// stays in reasoning_content unless that was refused too.
func withoutMessageReasoning(body []byte, fields []string) []byte {
	if slices.Contains(fields, "reasoning_details") && !slices.Contains(fields, "reasoning_content") {
		body = reasoningDetailsAsContent(body)
	}
	return withoutMessageFields(body, fields...)
}

// editMessages hands each message of a Chat request whose JSON names any
// of fields to edit, and puts back those edit reports it changed; every
// other message keeps its bytes. The body comes back as it was if nothing
// changed. What it decodes shares body's bytes (decodeJSONView): an edit
// replaces a member, never writes into one, and keeps none past its
// return, and a changed body is marshalled anew. A copy of each level
// was ~3.8 MB a request for a 2 MB Chat conversation.
func editMessages(body []byte, fields []string, edit func(m map[string]json.RawMessage) bool) []byte {
	has := false
	for _, f := range fields {
		has = has || bodyHas(body, `"`+f+`"`)
	}
	if !has {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	var msgs []json.RawMessage
	if decodeJSONView(q["messages"], &msgs) != nil {
		return body
	}
	changed := false
	for i, raw := range msgs {
		var m map[string]json.RawMessage
		if decodeJSONView(raw, &m) != nil || !edit(m) {
			continue
		}
		if nb, err := marshalPlain(m); err == nil {
			msgs[i], changed = nb, true
		}
	}
	if !changed {
		return body
	}
	q["messages"], _ = marshalPlain(msgs)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// withoutMessageFields leaves fields out of every message of a Chat
// request.
func withoutMessageFields(body []byte, fields ...string) []byte {
	return editMessages(body, fields, func(m map[string]json.RawMessage) bool {
		changed := false
		for _, f := range fields {
			if _, ok := m[f]; ok {
				delete(m, f)
				changed = true
			}
		}
		return changed
	})
}

// mistralThinking puts an assistant turn's thinking, sent back in
// reasoning_content (or reasoning, reasoning_details) as OpenCode does,
// into the typed thinking part Mistral itself returns it in, ahead of the
// turn's content: Mistral turns those fields away with 422 extra_forbidden
// (#494) and takes the part, so the thinking stays in context. A turn
// with tool calls and no text has the thinking part alone for content.
func mistralThinking(body []byte) []byte {
	return editMessages(body, messageReasoning, func(m map[string]json.RawMessage) bool {
		had := false
		for _, f := range messageReasoning {
			_, ok := m[f]
			had = had || ok
		}
		if !had {
			return false
		}
		think := reasoningText(m)
		for _, f := range messageReasoning {
			delete(m, f)
		}
		var role string
		decodeJSON(m["role"], &role)
		// a whitespace-only thinking is the placeholder a DeepSeek
		// replay puts on a tool turn that had none (#1462): dropped, not
		// sent as a blank thinking part (#4906)
		if strings.TrimSpace(think) == "" || role != "assistant" {
			return true
		}
		thinking, _ := marshalPlain(map[string]any{"type": "thinking",
			"thinking": []map[string]string{{"type": "text", "text": think}}})
		parts := []json.RawMessage{thinking}
		c := bytes.TrimSpace(m["content"])
		var list []json.RawMessage
		var s string
		switch {
		case len(c) > 0 && c[0] == '[' && decodeJSON(c, &list) == nil:
			parts = append(parts, list...)
		case decodeJSON(c, &s) == nil && s != "":
			text, _ := marshalPlain(map[string]string{"type": "text", "text": s})
			parts = append(parts, text)
		}
		m["content"], _ = marshalPlain(parts)
		return true
	})
}

// toolPairsOut is pairToolMessages for a Chat request relayed as the
// client sent it. Kimi's Chat API, like OpenAI's, turns the whole request
// away when its tool exchange doesn't pair: a call with no tool message
// answering it ("an assistant message with 'tool_calls' must be followed
// by tool messages responding to each 'tool_call_id'. The following
// tool_call_ids did not have response messages: …"), and a tool message
// with no call before it ("tool_call_id  is not found"), both checked
// against api.kimi.com/coding on 2026-10-26. A Chat agent's history holds
// such a turn after a fork at an earlier turn, a reply stopped with its
// call out, or a call split over two assistant messages, and every later
// request of the conversation was refused (Paidaixinbao on X, Qoder on
// Kimi Code). The translations had it mended (#554); the relay sent it
// as it came. A turn's calls split over consecutive assistant messages
// join into one first, as joinSplitCalls does for the translations. A
// body whose exchange pairs comes back byte for byte.
func toolPairsOut(body []byte) []byte {
	if !bodyHas(body, `"tool`) {
		return body
	}
	if paired, ok := rawToolsPaired(body); ok && paired {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(q["messages"]))
	dec.UseNumber()
	var msgs []map[string]any
	if dec.Decode(&msgs) != nil {
		return body
	}
	for _, m := range msgs {
		if m == nil {
			return body
		}
		raw, ok := m["tool_calls"]
		if !ok || raw == nil {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			return body
		}
		calls := make([]map[string]any, 0, len(list))
		for _, c := range list {
			cm, ok := c.(map[string]any)
			if !ok {
				return body
			}
			calls = append(calls, cm)
		}
		m["tool_calls"] = calls
	}
	if toolsPaired(msgs) {
		return body
	}
	q["messages"], _ = marshalPlain(pairToolMessages(joinSplitChatCalls(msgs)))
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// chatCalls is a Chat message's tool calls, as toolPairsOut typed them.
func chatCalls(m map[string]any) []map[string]any {
	calls, _ := m["tool_calls"].([]map[string]any)
	return calls
}

// toolsPaired reports whether every assistant message's tool calls are
// answered, each once, by the tool messages right after it, and every
// tool message answers such a call: what Kimi and OpenAI hold a Chat
// request to.
func toolsPaired(msgs []map[string]any) bool {
	var pending map[string]bool
	closed := func() bool {
		for _, open := range pending {
			if open {
				return false
			}
		}
		pending = nil
		return true
	}
	for _, m := range msgs {
		if m["role"] == "tool" {
			id := toolMsgID(m)
			if !pending[id] {
				return false
			}
			pending[id] = false
			continue
		}
		if !closed() {
			return false
		}
		if m["role"] == "assistant" {
			if calls := chatCalls(m); len(calls) > 0 {
				pending = map[string]bool{}
				for _, c := range calls {
					id, _ := c["id"].(string)
					if _, twice := pending[id]; twice {
						return false
					}
					pending[id] = true
				}
			}
		}
	}
	return closed()
}

// rawToolsPaired is toolsPaired read from the body as it came, without
// decoding the conversation into maps: what nearly every request with
// tools needs, since its calls are paired already. ok is false for any
// shape the decoding path would read differently or turn away (a message
// that isn't an object, tool_calls that aren't a list of objects), which
// leaves the answer to that path. A key named twice counts as its last,
// as decoding into a map does.
func rawToolsPaired(body []byte) (paired, ok bool) {
	msgs, found, valid := topMember(body, "messages")
	if !valid || !found || len(msgs) == 0 || msgs[0] != '[' {
		return false, false
	}
	type msg struct {
		role, toolID string
		ids          []string
	}
	str := func(v []byte) string {
		if len(v) == 0 || v[0] != '"' {
			return ""
		}
		s, _ := unquoteJSON(v)
		return s
	}
	var list []msg
	ok = true
	eachElement(msgs, func(el []byte) bool {
		if el[0] != '{' {
			ok = false
			return false
		}
		var m msg
		var calls []byte
		eachMember(el, func(k, v []byte) bool {
			switch {
			case keyIs(k, "role"):
				m.role = str(v)
			case keyIs(k, "tool_call_id"):
				m.toolID = str(v)
			case keyIs(k, "tool_calls"):
				calls = v
			}
			return true
		})
		if len(calls) > 0 && string(calls) != "null" {
			if calls[0] != '[' {
				ok = false
				return false
			}
			m.ids = []string{}
			eachElement(calls, func(c []byte) bool {
				if c[0] != '{' {
					ok = false
					return false
				}
				id := ""
				eachMember(c, func(k, v []byte) bool {
					if keyIs(k, "id") {
						id = str(v)
					}
					return true
				})
				m.ids = append(m.ids, id)
				return true
			})
		}
		list = append(list, m)
		return ok
	})
	if !ok {
		return false, false
	}
	var pending map[string]bool
	closed := func() bool {
		for _, open := range pending {
			if open {
				return false
			}
		}
		pending = nil
		return true
	}
	for _, m := range list {
		if m.role == "tool" {
			if !pending[m.toolID] {
				return false, true
			}
			pending[m.toolID] = false
			continue
		}
		if !closed() {
			return false, true
		}
		if m.role == "assistant" && len(m.ids) > 0 {
			pending = map[string]bool{}
			for _, id := range m.ids {
				if _, twice := pending[id]; twice {
					return false, true
				}
				pending[id] = true
			}
		}
	}
	return closed(), true
}

// joinSplitChatCalls joins a turn's calls split over consecutive
// assistant messages into the first, as joinSplitCalls does for the
// translations (#1275): the second message's calls follow the first's,
// its text and thinking join the first's where both are strings, and
// stand in for the first's when that has none.
func joinSplitChatCalls(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		n := len(out)
		if n == 0 || m["role"] != "assistant" || out[n-1]["role"] != "assistant" || len(chatCalls(out[n-1])) == 0 || len(chatCalls(m)) == 0 {
			out = append(out, m)
			continue
		}
		prev := out[n-1]
		prev["tool_calls"] = append(chatCalls(prev), chatCalls(m)...)
		for _, k := range []string{"content", "reasoning_content"} {
			was, isText := prev[k].(string)
			add, _ := m[k].(string)
			if strings.TrimSpace(add) == "" || prev[k] != nil && !isText {
				continue
			}
			if strings.TrimSpace(was) != "" {
				add = was + "\n\n" + add
			}
			prev[k] = add
		}
	}
	return out
}
