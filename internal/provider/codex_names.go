package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"unsafe"

	"github.com/tidwall/gjson"
)

// The ChatGPT backend checks every tool name a request carries, a call's
// in the input as much as a tool's in tools, against ^[a-zA-Z0-9_.-]+\z,
// and turns the whole request away when one fails: 400 "Invalid
// 'input[83].name': string does not match pattern" (wing3556 on Discord,
// on a compaction). A conversation can hold a call by a name no tool has:
// a model another provider served wrote one with a space, a colon or a
// word of its own script in it, and the agent kept the call in its
// history beside the "no such tool" it answered. Each request that
// replays it, a compaction's above all, failed on Codex every time. Such a
// name goes as CodexToolName makes it; a valid one goes as it is, so a
// conversation that worked is sent byte for byte as before.

// codexNameOK is a name the ChatGPT backend takes.
var codexNameOK = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// CodexToolName is name as the ChatGPT backend takes it: as it is when
// it is valid, else each character the backend refuses as "_", cut to
// 55, and a hash of the whole after it (as FlatName cuts a long name), so
// a name is made the same on every request and two names that differ
// never come out alike. An agent's declared tool so renamed is called
// back by its own name (CodexToolNamesBack).
func CodexToolName(name string) string {
	if codexNameOK.MatchString(name) {
		return name
	}
	b := []byte(name)
	out := make([]byte, 0, len(b)+9)
	for _, c := range b {
		if c == '_' || c == '.' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) > 55 {
		out = out[:55]
	}
	sum := sha256.Sum256(b)
	return string(out) + "_" + hex.EncodeToString(sum[:4])
}

// CodexToolNamesBack maps each name of declared that CodexToolName
// changes back to the name the agent declared; nil when none changes.
func CodexToolNamesBack(declared []string) map[string]string {
	var back map[string]string
	for _, n := range declared {
		if c := CodexToolName(n); c != n {
			if back == nil {
				back = map[string]string{}
			}
			back[c] = n
		}
	}
	return back
}

// CodexToolNames is body, a Responses request for the ChatGPT backend,
// with every tool name in it as CodexToolName makes it, or body byte for
// byte when each one is valid already.
func CodexToolNames(body []byte) []byte {
	if !codexNamesBad(body) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil || !codexToolNames(m) {
		return body
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// codexNamesBad reports whether body names a tool the backend refuses,
// read where codexToolNames reads it, without decoding the conversation.
func codexNamesBad(body []byte) bool {
	bad := false
	name := func(v gjson.Result) {
		if n := v.Get("name"); n.Type == gjson.String && !codexNameOK.MatchString(n.String()) {
			bad = true
		}
	}
	var tools func(ts gjson.Result)
	tools = func(ts gjson.Result) {
		ts.ForEach(func(_, t gjson.Result) bool {
			switch t.Get("type").String() {
			case "function", "custom":
				name(t)
			case "namespace":
				tools(t.Get("tools"))
			}
			return !bad
		})
	}
	// read in place: GetManyBytes copies each value it finds, the input
	// whole, which is the body's size (hundreds of MB with Codex's images)
	top := gjson.GetMany(unsafe.String(unsafe.SliceData(body), len(body)), "tools", "input", "tool_choice")
	tools(top[0])
	top[1].ForEach(func(_, it gjson.Result) bool {
		switch it.Get("type").String() {
		case "function_call", "custom_tool_call":
			name(it)
		case "additional_tools", "tool_search_output":
			tools(it.Get("tools"))
		}
		return !bad
	})
	if tc := top[2]; tc.IsObject() {
		switch tc.Get("type").String() {
		case "function", "custom":
			name(tc)
		case "allowed_tools":
			tools(tc.Get("tools"))
		}
	}
	return bad
}

// codexToolNames makes every tool name in m, a Responses request, one the
// backend takes: a function's or a custom tool's in tools (a namespace's
// own among them), in an additional_tools item or a tool search's output,
// a call's in the input, and the one tool_choice names. It says whether
// it changed one.
func codexToolNames(m map[string]any) bool {
	changed := false
	fix := func(o map[string]any) {
		if n, ok := o["name"].(string); ok {
			if c := CodexToolName(n); c != n {
				o["name"] = c
				changed = true
			}
		}
	}
	var tools func(ts []any)
	tools = func(ts []any) {
		for _, t := range ts {
			tm, _ := t.(map[string]any)
			switch tm["type"] {
			case "function", "custom":
				fix(tm)
			case "namespace":
				nested, _ := tm["tools"].([]any)
				tools(nested)
			}
		}
	}
	ts, _ := m["tools"].([]any)
	tools(ts)
	input, _ := m["input"].([]any)
	for _, raw := range input {
		it, _ := raw.(map[string]any)
		switch it["type"] {
		case "function_call", "custom_tool_call":
			fix(it)
		case "additional_tools", "tool_search_output":
			nested, _ := it["tools"].([]any)
			tools(nested)
		}
	}
	if tc, _ := m["tool_choice"].(map[string]any); tc != nil {
		switch tc["type"] {
		case "function", "custom":
			fix(tc)
		case "allowed_tools":
			nested, _ := tc["tools"].([]any)
			tools(nested)
		}
	}
	return changed
}
