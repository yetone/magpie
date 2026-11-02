package gateway

// PLUGIN-SERVED (see AGENTS.md): Grok ("grok") is a retired built-in
// subscription, served by its plugin alone,
// @magpie-community/opencode-grok-auth (provider.BuiltinRetired). Its
// sign-in, models, requests and usage are the plugin's; what is left here
// reads the accounts signed in to the built-in for the move onto the plugin
// (migrate_side.go), installs and finds the Grok CLI the plugin signs in
// with, and shapes the plugin's request bodies (grokBody). A fix belongs in
// the plugin (github.com/magpie-community/plugins, packages/grok), with the
// mover's min raised in internal/provider/migrate_side.go.

import (
	"bytes"
	"encoding/json"
)

// Codex groups some tools in a namespace (collaboration's spawn_agent,
// codex_app's set_thread_title), which a Grok subscription's backend turns
// a request away over. Its functions go to Grok flat (provider's grokBody,
// collaboration__spawn_agent) as they go to a backend magpie translates
// for, and a call the model makes to one comes back to Codex under its own
// name and namespace (#404).

// namespacedIn is the namespaced functions a Responses request offers or
// has called, by the flat name Grok is given them under.
func namespacedIn(body []byte) map[string]nsTool {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return nil
	}
	var q struct {
		Tools []rTool         `json:"tools"`
		Input json.RawMessage `json:"input"` // a list, or the text alone
	}
	if json.Unmarshal(body, &q) != nil {
		return nil
	}
	var items []struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	json.Unmarshal(q.Input, &items)
	named := map[string]nsTool{}
	add := func(space, name string) {
		if space != "" && space != liteNamespace && name != "" {
			named[flatName(space, name)] = nsTool{Namespace: space, Name: name}
		}
	}
	for _, t := range q.Tools {
		if t.Type != "namespace" {
			continue
		}
		for _, nt := range t.Tools {
			if nt.Type == "function" {
				add(t.Name, nt.Name)
			}
		}
	}
	for _, it := range items {
		add(it.Namespace, it.Name)
	}
	if len(named) == 0 {
		return nil
	}
	return named
}

// nsTidy gives, in a relayed Responses reply, the model's calls to a
// namespaced function their name and namespace back. A stream's lines pass
// as they came unless one carries such a call; a reply that isn't one is
// held whole and gone through at the end.
type nsTidy struct {
	named map[string]nsTool
	// back maps a declared tool's name a ChatGPT account was given in its
	// place to the agent's own (codexRenamedIn)
	back map[string]string
	sse  bool
	buf  []byte
}

func (t *nsTidy) write(b []byte) []byte {
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

func (t *nsTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := t.restore(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *nsTidy) lines(b []byte) []byte {
	if !t.carries(b) {
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
		if !ok || !t.carries(data) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.restore(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// restore is an event, or a whole reply, with its namespaced calls named
// as Codex knows them, and whether there was one.
func (t *nsTidy) restore(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	changed := false
	if it, _ := ev["item"].(map[string]any); t.call(it) {
		changed = true
	}
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, it := range out {
			if im, _ := it.(map[string]any); t.call(im) {
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

// carries reports whether b may hold a call restore names again: a
// function call, or a custom tool's to one renamed.
func (t *nsTidy) carries(b []byte) bool {
	return bytes.Contains(b, []byte(`"function_call"`)) || t.back != nil && bytes.Contains(b, []byte(`"custom_tool_call"`))
}

// call names a function_call to a namespaced function by its name and
// namespace, or a call to a tool renamed for a ChatGPT account by the
// agent's name, and reports whether it was one.
func (t *nsTidy) call(it map[string]any) bool {
	if it == nil || it["namespace"] != nil || it["type"] != "function_call" && it["type"] != "custom_tool_call" {
		return false
	}
	name, _ := it["name"].(string)
	if n, ok := t.back[name]; ok {
		it["name"] = n
		return true
	}
	if it["type"] != "function_call" {
		return false
	}
	if _, ok := t.named[name]; !ok {
		return false
	}
	callTo(it, name, t.named)
	return true
}
