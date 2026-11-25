package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A tool loop's earlier steps reach Devin's plugin with the reasoning
// each step had, whatever API the agent spoke (@Anotiaww on X: Codex on
// Devin's SWE models over Responses lost it in the translation to Chat).
// The plugin gives it to swe-2 as each step's thinking (plugins#703),
// and swe-2 thinks on a later step only when the steps before carry
// theirs. A step with none gets none: no placeholder thought.
func TestDevinPluginGetsEarlierReasoning(t *testing.T) {
	up := &cursorUp{}
	movedFake(t, "devin", up)
	s := New()
	const tools = `"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]`
	const chatTools = `"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]`
	for _, a := range []struct{ name, path, body string }{
		// Codex's body as it sends a translated reply's items back: a
		// reasoning item with the summary magpie gave it, id and all,
		// encrypted_content null; the third call has none (another
		// model's step)
		{"responses", "/v1/responses", `{"model":"devin/fake-1","stream":true,"instructions":"You are a coding agent.",` + tools + `,
			"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"store":false,
			"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Read /a, then /b, then /c."}]},
			{"type":"reasoning","id":"rs_6f1c0a","summary":[{"type":"summary_text","text":"Start with /a."}],"encrypted_content":null},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"/a\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"A"},
			{"type":"reasoning","id":"rs_6f1c0b","summary":[{"type":"summary_text","text":"A is read; "},{"type":"summary_text","text":"/b next."}]},
			{"type":"function_call","call_id":"call_2","name":"read_file","arguments":"{\"path\":\"/b\"}"},
			{"type":"function_call_output","call_id":"call_2","output":"B"},
			{"type":"function_call","call_id":"call_3","name":"read_file","arguments":"{\"path\":\"/c\"}"},
			{"type":"function_call_output","call_id":"call_3","output":"C"}]}`},
		// a Chat client asking for one body, which is translated (the
		// plugin is asked streamed) and lost it the same way
		{"chat", "/v1/chat/completions", `{"model":"devin/fake-1",` + chatTools + `,"messages":[
			{"role":"user","content":"Read /a, then /b, then /c."},
			{"role":"assistant","content":null,"reasoning_content":"Start with /a.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/a\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"A"},
			{"role":"assistant","content":null,"reasoning_content":"A is read; /b next.","tool_calls":[{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/b\"}"}}]},
			{"role":"tool","tool_call_id":"call_2","content":"B"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_3","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/c\"}"}}]},
			{"role":"tool","tool_call_id":"call_3","content":"C"}]}`},
		{"anthropic", "/v1/messages", `{"model":"devin/fake-1","max_tokens":1024,"stream":true,"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[
			{"role":"user","content":"Read /a, then /b, then /c."},
			{"role":"assistant","content":[{"type":"thinking","thinking":"Start with /a.","signature":""},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"/a"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"A"}]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"A is read; /b next.","signature":""},{"type":"tool_use","id":"toolu_2","name":"read_file","input":{"path":"/b"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"B"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"read_file","input":{"path":"/c"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"C"}]}]}`},
	} {
		postFrom(t, s, "codex", a.path, a.body)
		var got []any
		for _, m := range up.last(t)["messages"].([]any) {
			if m := m.(map[string]any); m["role"] == "assistant" {
				got = append(got, m["reasoning_content"])
			}
		}
		if want := []any{"Start with /a.", "A is read; /b next.", nil}; !jsonEqual(got, want) {
			t.Errorf("%s: Devin's plugin got reasoning %q, want %q", a.name, got, want)
		}
	}
}

// Devin is given a turn's reasoning without the placeholder a tool turn
// with none gets where the upstream asks for one on each (DeepSeek, and
// the plugins of Command Code and Qoder): swe-2 would be told a thought
// it never had. Every other Chat upstream is built as before.
func TestDevinReasoningWithoutPlaceholder(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "go"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "first"}, {Kind: ToolCall, ID: "c1", Name: "Bash", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "ok"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c2", Name: "Bash", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c2", Text: "ok"}}},
	}}
	reasoning := func(host string) []any {
		var m struct{ Messages []map[string]any }
		if err := json.Unmarshal(buildChat(r, "m", host, false), &m); err != nil {
			t.Fatal(err)
		}
		var out []any
		for _, msg := range m.Messages {
			if msg["role"] == "assistant" {
				out = append(out, msg["reasoning_content"])
			}
		}
		return out
	}
	for host, want := range map[string][]any{
		"devin":                    {"first", nil},
		"api.deepseek.com":         {"first", noReasoning},
		provider.CommandCodePlanID: {"first", noReasoning},
		"qoder":                    {"first", noReasoning},
		"qoder-cn":                 {"first", noReasoning},
		"api.openai.com":           {nil, nil},
		"cursor":                   {nil, nil},
		"api.moonshot.cn":          {nil, nil},
	} {
		if got := reasoning(host); !jsonEqual(got, want) {
			t.Errorf("%s: reasoning_content %q, want %q", host, got, want)
		}
	}
}
