package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's tool search relayed to a Responses API other than the ChatGPT
// backend's. Few of them know a tool_search tool, a tool_search_call or a
// tool_search_output, or the defer_loading of the tools a search found; an
// OpenAI API model before the ones that search turns them away as well. So
// the request goes with the search as a function, its calls and results
// as a function's, and the tools found in the tools; and a call the model
// makes to it comes back to Codex as the tool_search_call Codex runs.

// DeepSeek's own Responses API runs a client's tool search as the client
// sends it for deepseek-flash (V4.1 Flash): the tool_search tool with
// execution "client", its tool_search_call and tool_search_output items,
// and the found tools' defer_loading, which it leaves out of the leading
// prompt and loads after the history, so the cached prefix is kept
// (api-docs.deepseek.com, Responses API compatibility and its tool search
// guide). deepseek-v4-pro ignores the tool and its items, and its Chat
// API has no deferred loading, so those, and every other host serving
// DeepSeek's models, get the search as a function. A refusal that names
// it is asked again so, and remembered under clientSearchRefused.

// clientSearchRefused is a client's tool search as the client sent it,
// turned away by an upstream it went to as it was (searchesAsSent), as
// remembered in unfit.
const clientSearchRefused = "tool_search_call"

// searchesAsSent is whether p's Responses API runs a client's tool search
// for model as the client sends it: DeepSeek's own API, for
// deepseek-flash. The ChatGPT backend does too, and is told apart by its
// account where this is asked.
func searchesAsSent(p provider.Provider, model string) bool {
	if provider.HostOf(p.Base(provider.Responses)) != deepseekHost {
		return false
	}
	base := strings.ToLower(model[strings.LastIndexByte(model, '/')+1:])
	return base == "deepseek-flash" || strings.HasPrefix(base, "deepseek-v4.1-flash")
}

// searchRefusal is an upstream's 400 or 422 that names the tool search.
func searchRefusal(status int, errBody []byte) bool {
	return badRequest(status) && bodyHas(errBody, toolSearch)
}

// searchAsFunction is a Responses request with Codex's tool search as a
// plain function, and whether there was one. A request that offers no
// search still has the earlier searches' calls and results in its
// conversation: Codex leaves the tool out once nothing deferred is left
// to find, and its compaction sends no tools at all. Those go as a
// function's too, the tools they found left out, or a vendor that knows
// no tool_search_call turns the whole request away (#1171 tcxp001, the
// compaction on Anyrouter; #1755 FivePink, Xiaomi MiMo).
func searchAsFunction(body []byte) ([]byte, bool) {
	return searchAsFunctionIn(body, false)
}

// searchAsFunctionIn is searchAsFunction, and with inPlace the tools each
// search found go in an additional_tools item right after its result
// instead of in the tools: the tools and every earlier item stay as the
// turn before sent them, so the cached prefix is kept, as the ChatGPT
// backend keeps it for Codex's own search. Only for an upstream that takes
// the item (loadsInPlace); one that turns it away is asked again with them
// in the tools (loadedUp).
func searchAsFunctionIn(body []byte, inPlace bool) ([]byte, bool) {
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
	placed := map[string]map[string]bool{} // in an additional_tools item already: a tool's name, a namespace's tools
	var loaded []any                       // the found tools of the result being read, inPlace
	add := func(tm map[string]any) {
		switch {
		case !search:
		case inPlace:
			if fresh := unplaced(tm, have, placed); fresh != nil {
				loaded = append(loaded, fresh)
			}
		default:
			tools = addTool(tools, have, tm)
		}
	}
	history := false
	if items, ok := m["input"].([]any); ok {
		out := items[:0:0]
		for _, it := range items {
			im, _ := it.(map[string]any)
			switch im["type"] {
			case "tool_search_call":
				history = true
				args, _ := json.Marshal(im["arguments"])
				it = map[string]any{"type": "function_call", "call_id": im["call_id"], "name": toolSearch, "arguments": string(args)}
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
				it = map[string]any{"type": "function_call_output", "call_id": im["call_id"], "output": searchFound(named)}
			}
			out = append(out, it)
			if len(loaded) > 0 {
				// as buildResponses places what a result loaded
				// (Request.LoadedInPlace)
				out = append(out, map[string]any{"type": additionalToolsItem, "role": "developer", "tools": loaded})
				loaded = nil
			}
		}
		m["input"] = out
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

// unplaced is the part of tm, a tool a search found, that neither the
// request's tools nor an earlier additional_tools item has: tm itself, a
// namespace with only the tools of it not offered yet, or nil when there
// is none. placed records what it gives.
func unplaced(tm map[string]any, have map[string]map[string]any, placed map[string]map[string]bool) map[string]any {
	k := toolKey(tm)
	old, done := have[k], placed[k]
	if tm["type"] != "namespace" {
		if old != nil || done != nil {
			return nil
		}
		placed[k] = map[string]bool{}
		return tm
	}
	if old != nil && old["type"] != "namespace" {
		return nil
	}
	if done == nil {
		done = map[string]bool{}
		placed[k] = done
	}
	nested, _ := tm["tools"].([]any)
	var kept []any
	for _, nt := range nested {
		nm, _ := nt.(map[string]any)
		if nm == nil {
			continue
		}
		name, _ := nm["name"].(string)
		if done[name] {
			continue
		}
		if old != nil {
			if was, _ := old["tools"].([]any); containsNamed(was, nm["name"]) {
				continue
			}
		}
		done[name] = true
		kept = append(kept, nm)
	}
	if len(kept) == 0 {
		return nil
	}
	if len(kept) == len(nested) {
		return tm
	}
	ns := make(map[string]any, len(tm))
	for key, v := range tm {
		ns[key] = v
	}
	ns["tools"] = kept
	return ns
}

// loadedUp is a relayed body with the tools searchAsFunctionIn put in
// additional_tools items moved to its tools, as searchAsFunction puts
// them, for an upstream that turned the item away; ok is false when it
// has none. An item with an id is the client's own (Responses Lite's
// tools), and stays.
func loadedUp(body []byte) ([]byte, bool) {
	if !bodyHas(body, `"`+additionalToolsItem+`"`) {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body, false
	}
	items, _ := m["input"].([]any)
	tools, _ := m["tools"].([]any)
	have := toolsByName(tools)
	moved := false
	out := items[:0:0]
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im["type"] != additionalToolsItem || im["id"] != nil {
			out = append(out, it)
			continue
		}
		moved = true
		loaded, _ := im["tools"].([]any)
		for _, t := range loaded {
			if tm, _ := t.(map[string]any); tm != nil {
				tools = addTool(tools, have, tm)
			}
		}
	}
	if !moved {
		return body, false
	}
	m["input"], m["tools"] = out, tools
	b, err := marshalPlain(m)
	if err != nil {
		return body, false
	}
	return b, true
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
