package gateway

import (
	"bytes"
	"encoding/json"
)

// Codex's tool search relayed to a Responses API other than the ChatGPT
// backend's. Few of them know a tool_search tool, a tool_search_call or a
// tool_search_output, or the defer_loading of the tools a search found; an
// OpenAI API model before the ones that search turns them away as well. So
// the request goes with the search as a function, its calls and results
// as a function's, and the tools found in the tools; and a call the model
// makes to it comes back to Codex as the tool_search_call Codex runs.

// searchAsFunction is a Responses request with Codex's tool search as a
// plain function, and whether there was one. A request that offers no
// search still has the earlier searches' calls and results in its
// conversation: Codex leaves the tool out once nothing deferred is left
// to find, and its compaction sends no tools at all. Those go as a
// function's too, the tools they found left out, or a vendor that knows
// no tool_search_call turns the whole request away (#1171 tcxp001, the
// compaction on Anyrouter; #1755 FivePink, Xiaomi MiMo).
func searchAsFunction(body []byte) ([]byte, bool) {
	if !bodyHas(body, `"tool_search`) {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body, false
	}
	tools, _ := m["tools"].([]any)
	search := false
	for i, t := range tools {
		if tm, _ := t.(map[string]any); tm["type"] == toolSearch && tm["execution"] == "client" {
			tools[i] = map[string]any{"type": "function", "name": toolSearch, "description": tm["description"], "parameters": tm["parameters"]}
			search = true
		}
	}
	// a tool found is added once; a namespace found again (another search
	// in the same MCP server) adds the tools it didn't have. With no search
	// offered, nothing is added: the request's tools are what Codex offers
	// now, and a compaction's are none.
	have := toolsByName(tools)
	add := func(tm map[string]any) {
		if search {
			tools = addTool(tools, have, tm)
		}
	}
	history := false
	if items, ok := m["input"].([]any); ok {
		for i, it := range items {
			im, _ := it.(map[string]any)
			switch im["type"] {
			case "tool_search_call":
				history = true
				args, _ := json.Marshal(im["arguments"])
				items[i] = map[string]any{"type": "function_call", "call_id": im["call_id"], "name": toolSearch, "arguments": string(args)}
			case "tool_search_output":
				history = true
				found, _ := im["tools"].([]any)
				var named []rTool
				for _, t := range found {
					tm, _ := t.(map[string]any)
					if tm == nil {
						continue
					}
					undefer(tm)
					var rt rTool
					if b, err := json.Marshal(tm); err == nil && json.Unmarshal(b, &rt) == nil {
						named = append(named, rt)
					}
					add(tm)
				}
				items[i] = map[string]any{"type": "function_call_output", "call_id": im["call_id"], "output": searchFound(named)}
			}
		}
	}
	if !search && !history {
		return body, false
	}
	if search {
		m["tools"] = tools
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body, false
	}
	return out, search
}

// undefer drops defer_loading from a tool a search found, and from a
// namespace's tools.
func undefer(tm map[string]any) {
	delete(tm, "defer_loading")
	if nested, ok := tm["tools"].([]any); ok {
		for _, n := range nested {
			if nm, _ := n.(map[string]any); nm != nil {
				delete(nm, "defer_loading")
			}
		}
	}
}

// marshalPlain is json.Marshal leaving <, > and & as they are.
func marshalPlain(v any) ([]byte, error) {
	if b, ok := marshalRaw(v, false); ok {
		return b, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// searchTidy turns, in a relayed Responses stream, the model's calls to the
// tool_search function back into the tool_search_calls Codex runs. Lines
// pass as they came unless one carries such a call.
type searchTidy struct {
	buf []byte
}

func (t *searchTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := searchLines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *searchTidy) flush() []byte {
	out := searchLines(t.buf)
	t.buf = nil
	return out
}

func searchLines(b []byte) []byte {
	if !bodyHas(b, `"tool_search"`) {
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
		out = append(out, searchLine(line)...)
	}
	return out
}

func searchLine(line []byte) []byte {
	if !bodyHas(line, `"tool_search"`) {
		return line
	}
	body := bytes.TrimRight(line, "\r\n")
	end := line[len(body):]
	data, ok := bytes.CutPrefix(body, []byte("data:"))
	if !ok {
		return line
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return line
	}
	changed := false
	if it, _ := ev["item"].(map[string]any); searchCall(it) {
		changed = true
	}
	if res, _ := ev["response"].(map[string]any); res != nil {
		out, _ := res["output"].([]any)
		for _, it := range out {
			if im, _ := it.(map[string]any); searchCall(im) {
				changed = true
			}
		}
	}
	if !changed {
		return line
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return line
	}
	return append(append([]byte("data: "), nb...), end...)
}

// searchCall makes a function_call to tool_search the tool_search_call it
// stands for, and reports whether it was one.
func searchCall(it map[string]any) bool {
	if it == nil || it["type"] != "function_call" || it["name"] != toolSearch || it["namespace"] != nil {
		return false
	}
	args, _ := it["arguments"].(string)
	delete(it, "name")
	it["type"], it["execution"], it["arguments"] = "tool_search_call", "client", parseArgs(args)
	return true
}
