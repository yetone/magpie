package gateway

import (
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// A tool call that names no function runs nowhere, so it is no call. An
// upstream's own bad reply can make one: GLM-5.3 through Command Code gave
// Claude Code a tool_use whose name was "" (#7565, k0ngk0ng). Kept in the
// history, it went out on the next turn as a Responses function_call with
// "name":"" and its result as an output answering it, and Command Code
// turned the whole request away ("function_call is missing name"), every
// retry and every later turn of the session alike. A Chat stream's
// nameless call was already left out of the reply (chattidy.go, #1475);
// the other routes now do the same, on both sides:
//
//   - a translated reply never hands its client a call naming nothing
//     (namelessEncoder, namelessResult), on every API the client speaks;
//   - a history that holds one already goes out without it and without the
//     results answering it: translated, on every API magpie builds
//     (withoutNamelessCalls, in afterParse), and relayed as the client
//     wrote it, on Messages, Responses and Chat (namelessBlocksOut,
//     namelessItemsOut, namelessChatCallsOut). The call and its answer go
//     together, so what is left still pairs.
//
// A nameless call Gemini signed takes the step's thought signature with
// it: Gemini signs the first call of a step, and turns the next request
// away without that signature on it (400 "Function call is missing a
// thought_signature"). So when a nameless call that carries one is left
// out, the signature goes to the named call after it in the same step,
// one that carries none of its own (Kisxndbskx on X: Codex on a relay
// serving Gemini, a nameless signed call before a named one). A call
// with no Gemini signature hands nothing on, so no other vendor's reply
// or history changes.

// withoutNamelessCalls leaves out of r's history every tool call that
// names no function and every result answering one of them by its id. A
// message left with nothing is left out too.
//
// A Gemini signature on a call left out goes to the next named call of
// the same step with none (the assistant messages up to the next other
// message): unsignCalls has put it on the call by then.
func withoutNamelessCalls(r *Request) {
	found := false
	gone := map[string]bool{}
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall && p.Name == "" {
				found = true
				if p.ID != "" {
					gone[p.ID] = true
				}
			}
		}
	}
	if !found {
		return
	}
	msgs := make([]Message, 0, len(r.Messages))
	sig := "" // a left-out call's signature, for the next named call of its step
	for _, m := range r.Messages {
		if m.Role != "assistant" {
			sig = ""
		}
		parts := make([]Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch {
			case p.Kind == ToolCall && p.Name == "":
				if sig == "" {
					sig = p.Signature
				}
				continue
			case p.Kind == ToolResult && p.Standalone == nil && p.CallID != "" && gone[p.CallID]:
				continue
			case p.Kind == ToolCall && sig != "":
				if p.Signature == "" {
					p.Signature = sig
				}
				sig = ""
			}
			parts = append(parts, p)
		}
		if len(parts) == 0 && len(m.Parts) > 0 {
			continue
		}
		m.Parts = parts
		msgs = append(msgs, m)
	}
	r.Messages = msgs
}

// namelessEncoder hands its encoder a reply without the calls in it that
// name no function, nor their arguments. A reply whose only calls were
// such ends "stop": told it stopped for tools with none to run, an agent
// waits on nothing. A Gemini signature a left-out call's id carried goes
// on the id of the named call after it (handedSignature).
type namelessEncoder struct {
	streamEncoder
	dropping        bool   // the open call named nothing: its arguments go nowhere
	named, nameless bool   // whether the reply made a call naming a function, one naming none
	sig             string // a left-out call's Gemini signature, for the next named call
}

func (e *namelessEncoder) event(ev Event) {
	switch ev.Kind {
	case KToolStart:
		if e.dropping = ev.Name == ""; e.dropping {
			e.nameless = true
			if e.sig == "" {
				e.sig = callSignature(ev.ID)
			}
			return
		}
		e.named = true
		if e.sig != "" {
			ev.ID = handedSignature(ev.ID, e.sig)
			e.sig = ""
		}
	case KToolArgs:
		if e.dropping {
			return
		}
	case KStop:
		e.dropping = false
		if ev.Stop == "tool" && e.nameless && !e.named {
			ev.Stop = "stop"
		}
	case KAlive, KUsage, KStart:
		// nothing of the reply's content: the open call stays open
	default:
		e.dropping = false
	}
	e.streamEncoder.event(ev)
}

// namelessResult is a whole reply without the calls in it that name no
// function, ended "stop" when those were all its calls. A Gemini
// signature a left-out call's id carried goes on the id of the named
// call after it (handedSignature).
func namelessResult(res Result) Result {
	dropped, named := false, false
	sig := ""
	var parts []Part
	for _, p := range res.Parts {
		if p.Kind == ToolCall {
			if p.Name == "" {
				dropped = true
				if sig == "" {
					sig = callSignature(p.ID)
				}
				continue
			}
			named = true
			if sig != "" {
				p.ID = handedSignature(p.ID, sig)
				sig = ""
			}
		}
		parts = append(parts, p)
	}
	if !dropped {
		return res
	}
	res.Parts = parts
	if res.Stop == "tool" && !named {
		res.Stop = "stop"
	}
	return res
}

// callSignature is the Gemini thought signature a call's id carries
// (signedID), or "" for an id that carries none, as every other vendor's
// does.
func callSignature(id string) string {
	if !strings.Contains(id, sigMark) {
		return ""
	}
	_, sig := unsignedID(id)
	return sig
}

// handedSignature is the id of a named call given sig, the signature of a
// nameless call before it in the same step: Gemini wants it on the step's
// first call, and the nameless one goes nowhere. An id that carries a
// signature of its own keeps it.
func handedSignature(id, sig string) string {
	if sig == "" || callSignature(id) != "" {
		return id
	}
	return signedID(id, sig)
}

// signatureTail is the end of a call's id that carries its Gemini
// signature (signedID's mark and what follows), to go on another call's
// id as it is; "" for an id that carries none.
func signatureTail(id string) string {
	if !strings.Contains(id, sigMark) {
		return ""
	}
	own, sig := unsignedID(id)
	if sig == "" {
		return ""
	}
	return id[len(own):]
}

// withMember is the JSON object raw with its member key set to the string
// v; raw as it came when it isn't an object.
func withMember(raw json.RawMessage, key, v string) json.RawMessage {
	var whole map[string]json.RawMessage
	if decodeJSON(raw, &whole) != nil || whole == nil {
		return raw
	}
	whole[key], _ = marshalPlain(v)
	out, err := marshalPlain(whole)
	if err != nil {
		return raw
	}
	return out
}

// emptyName finds a name given as "" or null in a body: the only bodies a
// relay decodes to look for a nameless call. One with no name member at all
// is left as it came; every client seen writes the member it was given.
var emptyName = lazyre.New(`"name"\s*:\s*(""|null)`)

// namesNothing says whether a call's name member, raw, names no function:
// none, null, "", or not a string.
func namesNothing(raw json.RawMessage) bool {
	var s string
	return decodeJSON(raw, &s) != nil || s == ""
}

// namelessBlocksOut is a Messages request, relayed as the client wrote it,
// without its tool_use blocks that name nothing and the tool_result blocks
// answering them. A body with none comes back byte for byte, and a message
// with none as it came.
func namelessBlocksOut(body []byte) []byte {
	if !emptyName.Match(body) || !bodyHas(body, `"tool_use"`) {
		return body
	}
	type block struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      json.RawMessage `json:"name"`
		ToolUseID string          `json:"tool_use_id"`
	}
	var msgs []json.RawMessage
	raw, found, ok := topMember(body, "messages")
	if !ok || !found || decodeJSONView(raw, &msgs) != nil {
		return body
	}
	contents := make([][]json.RawMessage, len(msgs))
	gone := map[string]bool{}
	seen := false
	for i, m := range msgs {
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if decodeJSONView(m, &msg) != nil || decodeJSONView(msg.Content, &contents[i]) != nil {
			contents[i] = nil
			continue
		}
		for _, b := range contents[i] {
			var bl block
			if decodeJSONView(b, &bl) == nil && bl.Type == "tool_use" && namesNothing(bl.Name) {
				seen = true
				if bl.ID != "" {
					gone[bl.ID] = true
				}
			}
		}
	}
	if !seen {
		return body
	}
	out := make([]json.RawMessage, 0, len(msgs))
	for i, m := range msgs {
		if contents[i] == nil {
			out = append(out, m)
			continue
		}
		var kept []json.RawMessage
		for _, b := range contents[i] {
			var bl block
			if decodeJSONView(b, &bl) == nil && (bl.Type == "tool_use" && namesNothing(bl.Name) || bl.Type == "tool_result" && bl.ToolUseID != "" && gone[bl.ToolUseID]) {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == len(contents[i]) {
			out = append(out, m)
			continue
		}
		if len(kept) == 0 {
			continue
		}
		var whole map[string]json.RawMessage
		if decodeJSON(m, &whole) != nil {
			return body
		}
		whole["content"], _ = marshalPlain(kept)
		nm, err := marshalPlain(whole)
		if err != nil {
			return body
		}
		out = append(out, nm)
	}
	arr, err := marshalPlain(out)
	if err != nil {
		return body
	}
	return withTopValue(body, "messages", arr)
}

// namelessItemsOut is a Responses request, relayed as the client wrote it,
// without its function_call and custom_tool_call items that name nothing
// and the outputs answering them. A body with none comes back byte for
// byte. The Gemini signature a left-out call's id carried goes on the
// next named call of its step (signatureTail), and on the output
// answering that call, so the two still pair on any upstream.
func namelessItemsOut(body []byte) []byte {
	if !emptyName.Match(body) {
		return body
	}
	type item struct {
		Type   string          `json:"type"`
		Name   json.RawMessage `json:"name"`
		CallID string          `json:"call_id"`
	}
	raw, found, ok := topMember(body, "input")
	var items []json.RawMessage
	if !ok || !found || len(raw) == 0 || raw[0] != '[' || decodeJSONView(raw, &items) != nil {
		return body
	}
	call := func(it item) bool {
		return (it.Type == "function_call" || it.Type == "custom_tool_call") && namesNothing(it.Name)
	}
	gone := map[string]bool{}
	seen := false
	for _, el := range items {
		var it item
		if decodeJSONView(el, &it) == nil && call(it) {
			seen = true
			if it.CallID != "" {
				gone[it.CallID] = true
			}
		}
	}
	if !seen {
		return body
	}
	kept := make([]json.RawMessage, 0, len(items))
	tail := ""                     // a left-out call's signature, for the next named call of its step
	renamed := map[string]string{} // a call given that signature: its id, to the id carrying it
	for _, el := range items {
		var it item
		if decodeJSONView(el, &it) != nil {
			kept = append(kept, el)
			continue
		}
		output := it.Type == "function_call_output" || it.Type == "custom_tool_call_output"
		switch {
		case call(it):
			if tail == "" {
				tail = signatureTail(it.CallID)
			}
			continue
		case output && it.CallID != "" && gone[it.CallID]:
			continue
		case output && renamed[it.CallID] != "":
			el = withMember(el, "call_id", renamed[it.CallID])
		case it.Type == "function_call" || it.Type == "custom_tool_call":
			if tail != "" && it.CallID != "" && callSignature(it.CallID) == "" {
				renamed[it.CallID] = it.CallID + tail
				el = withMember(el, "call_id", it.CallID+tail)
			}
			tail = ""
		case it.Type != "reasoning":
			tail = "" // the step ended with no named call after it
		}
		kept = append(kept, el)
	}
	arr, err := marshalPlain(kept)
	if err != nil {
		return body
	}
	return withTopValue(body, "input", arr)
}

// namelessChatCallsOut is a Chat request, relayed as the client wrote it,
// without its tool calls that name no function and the tool messages
// answering them. An assistant message left with neither calls nor content
// is left out. A body with none comes back byte for byte. The Gemini
// signature a left-out call carried, in its id (signatureTail) or in
// extra_content, goes on the next named call of its message that has
// none, and an id given it on the tool message answering that call.
func namelessChatCallsOut(body []byte) []byte {
	if !emptyName.Match(body) || !bodyHas(body, `"tool_calls"`) {
		return body
	}
	raw, found, ok := topMember(body, "messages")
	var msgs []json.RawMessage
	if !ok || !found || decodeJSONView(raw, &msgs) != nil {
		return body
	}
	type call struct {
		ID       string `json:"id"`
		Function struct {
			Name json.RawMessage `json:"name"`
		} `json:"function"`
		Extra json.RawMessage `json:"extra_content"`
	}
	gone := map[string]bool{}
	seen := false
	for _, m := range msgs {
		var msg struct {
			ToolCalls []call `json:"tool_calls"`
		}
		if decodeJSONView(m, &msg) != nil {
			continue
		}
		for _, c := range msg.ToolCalls {
			if namesNothing(c.Function.Name) {
				seen = true
				if c.ID != "" {
					gone[c.ID] = true
				}
			}
		}
	}
	if !seen {
		return body
	}
	out := make([]json.RawMessage, 0, len(msgs))
	renamed := map[string]string{} // a call given a left-out call's signature: its id, to the id carrying it
	for _, m := range msgs {
		var msg struct {
			Role       string            `json:"role"`
			ToolCallID string            `json:"tool_call_id"`
			ToolCalls  []json.RawMessage `json:"tool_calls"`
		}
		if decodeJSONView(m, &msg) != nil {
			out = append(out, m)
			continue
		}
		if msg.Role == "tool" && msg.ToolCallID != "" && gone[msg.ToolCallID] {
			continue
		}
		if msg.Role == "tool" && renamed[msg.ToolCallID] != "" {
			out = append(out, withMember(m, "tool_call_id", renamed[msg.ToolCallID]))
			continue
		}
		var kept []json.RawMessage
		var tail string           // a left-out call's signature in its id
		var extra json.RawMessage // or in its extra_content
		for _, c := range msg.ToolCalls {
			var cl call
			if decodeJSONView(c, &cl) != nil {
				kept = append(kept, c)
				continue
			}
			if namesNothing(cl.Function.Name) {
				if tail == "" && extra == nil {
					if tail = signatureTail(cl.ID); tail == "" && googleSignature(cl.Extra) != "" {
						extra = cl.Extra
					}
				}
				continue
			}
			if (tail != "" || extra != nil) && callSignature(cl.ID) == "" && googleSignature(cl.Extra) == "" {
				switch {
				case tail != "" && cl.ID != "":
					renamed[cl.ID] = cl.ID + tail
					c = withMember(c, "id", cl.ID+tail)
				case extra != nil:
					var whole map[string]json.RawMessage
					if decodeJSON(c, &whole) == nil && whole != nil {
						whole["extra_content"] = extra
						if nc, err := marshalPlain(whole); err == nil {
							c = nc
						}
					}
				}
			}
			tail, extra = "", nil
			kept = append(kept, c)
		}
		if len(kept) == len(msg.ToolCalls) {
			out = append(out, m)
			continue
		}
		var whole map[string]json.RawMessage
		if decodeJSON(m, &whole) != nil {
			return body
		}
		if len(kept) == 0 {
			delete(whole, "tool_calls")
			if !contentHolds(whole["content"]) {
				continue
			}
		} else {
			whole["tool_calls"], _ = marshalPlain(kept)
		}
		nm, err := marshalPlain(whole)
		if err != nil {
			return body
		}
		out = append(out, nm)
	}
	arr, err := marshalPlain(out)
	if err != nil {
		return body
	}
	return withTopValue(body, "messages", arr)
}

// contentHolds says whether a Chat message's content, raw, holds anything:
// not absent, null, "" or [].
func contentHolds(raw json.RawMessage) bool {
	var s string
	if decodeJSON(raw, &s) == nil {
		return s != ""
	}
	var parts []json.RawMessage
	if decodeJSON(raw, &parts) == nil {
		return len(parts) > 0
	}
	return len(raw) > 0 && string(raw) != "null"
}
