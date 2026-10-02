package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codeModeRequest is a turn as Codex sends it for a model its catalog puts
// in code mode on Responses Lite (gpt-6.1-sol, gpt-6-luna: tool_mode
// code_mode_only, use_responses_lite): exec, a custom tool taking
// JavaScript, through which every other tool is called, and wait, in the
// "functions" namespace; the collaboration tools in their own; an earlier
// exec call and its output.
const codeModeRequest = `{"model":"fake/m1","instructions":"","stream":true,
	"reasoning":{"effort":"high","summary":"auto","context":"all_turns"},
	"input":[
	{"type":"additional_tools","id":"at_1","role":"developer","tools":[
		{"type":"namespace","name":"functions","description":"","tools":[
			{"type":"custom","name":"exec","description":"Run JavaScript.","format":{"type":"grammar","syntax":"lark","definition":"start: plain_source\nplain_source: SOURCE\nSOURCE: /[\\s\\S]+/"}},
			{"type":"function","name":"wait","description":"Wait for an exec cell.","strict":false,"parameters":{"type":"object","properties":{"cell_id":{"type":"string"}}}}]},
		{"type":"namespace","name":"collaboration","description":"Agents","tools":[
			{"type":"function","name":"spawn_agent","description":"Spawn an agent","parameters":{"type":"object"}}]}]},
	{"type":"message","id":"msg_1","role":"developer","content":[{"type":"input_text","text":"You are Codex."}]},
	{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
	{"type":"custom_tool_call","call_id":"c1","name":"exec","namespace":"functions","input":"const r = await tools.exec_command({cmd: \"ls\"});\ntext(r);"},
	{"type":"custom_tool_call_output","call_id":"c1","output":"a.txt"}
]}`

// A model magpie translates for is offered Codex's custom tools as
// functions taking their input (#534: in code mode exec was dropped, and
// the model said it had only the collaboration and wait tools), sees an
// earlier call to one and its output, and its call goes back to Codex as
// the custom_tool_call Codex runs, the input as the model wrote it.
func TestCodexCustomToolsTranslated(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"text(await "}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"tools.read({path: \\\"a.txt\\\"}))\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/responses", codeModeRequest)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the model was offered and shown
	var up struct {
		Tools []struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("%v: %s", err, f.got)
	}
	var names []string
	for _, tl := range up.Tools {
		names = append(names, tl.Function.Name)
	}
	if want := "exec wait collaboration__spawn_agent"; strings.Join(names, " ") != want {
		t.Fatalf("tools %v, want %s\n%s", names, want, f.got)
	}
	exec := up.Tools[0].Function
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(exec.Parameters, &schema) != nil || schema.Properties["input"].Type != "string" || len(schema.Required) != 1 || schema.Required[0] != "input" {
		t.Fatalf("exec's parameters: %s", exec.Parameters)
	}
	if !strings.HasPrefix(exec.Description, "Run JavaScript.") || !strings.Contains(exec.Description, "lark grammar") || !strings.Contains(exec.Description, "plain_source: SOURCE") {
		t.Fatalf("exec's description: %q", exec.Description)
	}
	var called, answered bool
	for _, m := range up.Messages {
		for _, c := range m.ToolCalls {
			if c.ID == "c1" && c.Function.Name == "exec" && customInput(c.Function.Arguments) == "const r = await tools.exec_command({cmd: \"ls\"});\ntext(r);" {
				called = true
			}
		}
		if m.Role == "tool" && m.ToolCallID == "c1" && m.Content == "a.txt" {
			answered = true
		}
	}
	if !called || !answered {
		t.Fatalf("earlier exec call %v, its output %v: %s", called, answered, f.got)
	}

	// what Codex got back
	var done map[string]any
	for _, e := range events(body) {
		switch e["type"] {
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			t.Errorf("a custom tool's call streamed as a function's: %v", e)
		case "response.output_item.done":
			done, _ = e["item"].(map[string]any)
		}
	}
	if done == nil || done["type"] != "custom_tool_call" || done["name"] != "exec" || done["namespace"] != nil ||
		done["call_id"] != "call_2" || done["input"] != `text(await tools.read({path: "a.txt"}))` || done["arguments"] != nil {
		t.Fatalf("call item: %v\n%s", done, body)
	}
}

// A custom tool in a namespace of its own is called back under it, and a
// call whose arguments aren't the input object still hands its text on.
func TestCodexCustomToolNamespaced(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":"hi","tools":[
		{"type":"namespace","name":"mcp__pad","tools":[{"type":"custom","name":"write","description":"Write","format":{"type":"text"}}]},
		{"type":"custom","name":"apply_patch","description":"Patch files"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 2 || r.Tools[0].Name != "mcp__pad__write" || r.Tools[1].Name != "apply_patch" || strings.Contains(r.Tools[0].Description, "grammar") {
		t.Fatalf("tools: %+v", r.Tools)
	}
	item := callTo(map[string]any{"type": "function_call", "call_id": "c", "arguments": `{"input":"x"}`}, "mcp__pad__write", r.Namespaced)
	if item["type"] != "custom_tool_call" || item["name"] != "write" || item["namespace"] != "mcp__pad" || item["input"] != "x" {
		t.Fatalf("namespaced call: %v", item)
	}
	item = callTo(map[string]any{"type": "function_call", "call_id": "c", "arguments": `*** Begin Patch`}, "apply_patch", r.Namespaced)
	if item["type"] != "custom_tool_call" || item["input"] != "*** Begin Patch" || item["namespace"] != nil {
		t.Fatalf("plain call: %v", item)
	}
	item = renderCall(t, r.Namespaced)
	if item["type"] != "custom_tool_call" || item["input"] != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("rendered call: %v", item)
	}
}

// renderCall is the non-streamed reply's item for a call to apply_patch.
func renderCall(t *testing.T, named map[string]nsTool) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** End Patch"})
	out := renderResponses(Result{Parts: []Part{{Kind: ToolCall, ID: "c", Name: "apply_patch", Args: args}}}, "m", named)
	var res struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Output) != 1 {
		t.Fatalf("%v: %s", err, out)
	}
	return res.Output[0]
}
