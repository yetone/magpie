package provider

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

import (
	"bytes"
	"encoding/json"
)

// Zed's cloud reads an OpenAI model's request into its own Responses types
// (zed-industries/zed, crates/open_ai/src/responses.rs) before it goes on to
// OpenAI, and turns the whole request away over anything they don't name:
// "failed to parse OpenAI Responses API request: unknown variant
// `developer`, expected one of `user`, `assistant`, `system`, `tool`" on
// Codex's developer messages. What they take:
//
//   - input items tagged by type: message, function_call,
//     function_call_output, custom_tool_call, custom_tool_call_output,
//     reasoning, compaction, compaction_trigger;
//   - a message's role: user, assistant, system, tool; its content a list;
//   - a reasoning item's content a list, if there (serde's default is for
//     a missing field, not a null one);
//   - tools of type function or custom;
//   - include: reasoning.encrypted_content alone;
//   - reasoning.summary: auto, concise, detailed.
//
// ZedBody is a Responses request fitted to those, Codex's above all; a
// request in another API (one with no input list) goes as it came.
func ZedBody(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"input"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	input, ok := m["input"].([]any)
	if !ok {
		return body
	}
	if _, chat := m["messages"]; chat {
		return body
	}
	dirty := false
	kept := input[:0:0]
	for _, it := range input {
		im, ok := it.(map[string]any)
		if !ok {
			kept = append(kept, it)
			continue
		}
		ty, _ := im["type"].(string)
		if ty == "" {
			if _, ok := im["role"]; ok {
				// the shorthand message, {role, content}: Zed's items are
				// tagged
				im["type"], ty, dirty = "message", "message", true
			}
		}
		if flatCall(im) {
			dirty = true
		}
		switch ty {
		case "message":
			// Codex's developer messages (its permissions, the model
			// switched, the context changed) say what a system message
			// says, and stay where they stand
			if im["role"] == "developer" {
				im["role"], dirty = "system", true
			}
			if s, ok := im["content"].(string); ok {
				part := "input_text"
				if im["role"] == "assistant" {
					part = "output_text"
				}
				im["content"], dirty = []any{map[string]any{"type": part, "text": s}}, true
			}
		case "agent_message":
			// a task handed to a subagent, or one agent's message to
			// another: the user's turn for the agent that receives it
			im = map[string]any{"type": "message", "role": "user", "content": im["content"]}
			dirty = true
		case "reasoning":
			if c, ok := im["content"]; ok && c == nil {
				delete(im, "content")
				dirty = true
			}
		case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "compaction", "compaction_trigger":
		default:
			// an item Zed has no type for (a web search an OpenAI model
			// ran, an item_reference) goes; it would take the request with it
			dirty = true
			continue
		}
		kept = append(kept, im)
	}
	m["input"] = kept
	if tools, ok := m["tools"].([]any); ok {
		var out []any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			switch ty, _ := tm["type"].(string); ty {
			case "function", "custom":
				out = append(out, t)
			case "namespace":
				dirty = true
				// its functions flat, as Grok's backend is given them; a
				// call back to one is named for Codex again
				out = append(out, grokFlat(tm)...)
			default:
				dirty = true
			}
		}
		if len(out) == 0 {
			delete(m, "tools")
			delete(m, "tool_choice")
		} else {
			m["tools"] = out
		}
	}
	if tc, ok := m["tool_choice"].(map[string]any); ok {
		// Zed's tool_choice names a function as Chat Completions does
		// ({type, function: {name}}) and turns the Responses one away: a
		// tool is still called, the model picking which
		m["tool_choice"], dirty = "auto", true
		if tc["type"] == "function" || tc["type"] == "custom" {
			m["tool_choice"] = "required"
		}
	}
	if inc, ok := m["include"].([]any); ok {
		var out []any
		for _, v := range inc {
			if v == "reasoning.encrypted_content" {
				out = append(out, v)
			} else {
				dirty = true
			}
		}
		if len(out) == 0 {
			delete(m, "include")
		} else {
			m["include"] = out
		}
	}
	if r, ok := m["reasoning"].(map[string]any); ok {
		if s, ok := r["summary"]; ok && s != "auto" && s != "concise" && s != "detailed" {
			delete(r, "summary")
			dirty = true
		}
	}
	if !dirty {
		return body
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}
