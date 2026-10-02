package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// liteRequest is a request as Codex's Responses Lite sends it: no tools and
// no instructions, its tools as the first input item (its own functions in
// the "functions" namespace), its prompt as a developer message, and a call
// made earlier under that namespace.
const liteRequest = `{"model":"fake/m1","instructions":"","stream":true,"input":[
	{"type":"additional_tools","id":"at_1","role":"developer","tools":[
		{"type":"tool_search","execution":"client","description":"Search tools","parameters":{"type":"object"}},
		{"type":"namespace","name":"functions","description":"Default tools","tools":[
			{"type":"function","name":"shell","description":"Run a command","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"type":"custom","name":"apply_patch","description":"Patch files","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]},
		{"type":"namespace","name":"mcp__probe","description":"Probe","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}]},
	{"type":"message","id":"msg_1","role":"developer","content":[{"type":"input_text","text":"You are Codex."}]},
	{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
	{"type":"function_call","call_id":"c1","name":"shell","namespace":"functions","arguments":"{\"cmd\":\"ls\"}"},
	{"type":"function_call_output","call_id":"c1","output":"a.txt"}
]}`

// A Responses API other than OpenAI's gets Codex's Lite tools in its tools,
// not as an input item it turns the whole request away over (#350).
func TestAdditionalToolsRelayed(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)}
	f.refuse = func(body []byte) (int, string) {
		if strings.Contains(string(body), `"additional_tools"`) {
			return http.StatusUnprocessableEntity, `{"error":"Failed to deserialize the JSON body into the target type: input[0]: unknown item type \"additional_tools\""}`
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	if code, body := post(t, "/v1/responses", liteRequest); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var got struct {
		Input []map[string]any `json:"input"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(f.got, &got); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range got.Tools {
		n, _ := tl["name"].(string)
		names = append(names, tl["type"].(string)+":"+n)
	}
	// tool search as the function magpie serves it; Codex's own tools out
	// of their namespace; an MCP server's namespace as it was
	if want := "function:tool_search function:shell custom:apply_patch namespace:mcp__probe"; strings.Join(names, " ") != want {
		t.Fatalf("tools %v, want %s\n%s", names, want, f.got)
	}
	if len(got.Input) != 4 || got.Input[0]["role"] != "developer" || got.Input[1]["role"] != "user" {
		t.Fatalf("input: %s", f.got)
	}
	if call := got.Input[2]; call["name"] != "shell" || call["namespace"] != nil {
		t.Fatalf("earlier call: %v", call)
	}
}

// A request translated for another API offers the Lite tools as the tools
// they are, and an earlier call to one under its own name.
func TestAdditionalToolsTranslated(t *testing.T) {
	r, err := parseResponses([]byte(liteRequest))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	// apply_patch, a custom tool, is offered as a function taking its input
	if want := "tool_search shell apply_patch mcp__probe__read"; strings.Join(names, " ") != want {
		t.Fatalf("tools %v, want %s", names, want)
	}
	if !r.Namespaced["apply_patch"].Custom {
		t.Fatalf("apply_patch not custom: %+v", r.Namespaced)
	}
	if r.Namespaced["mcp__probe__read"] != (nsTool{Namespace: "mcp__probe", Name: "read"}) {
		t.Fatalf("namespace lost: %+v", r.Namespaced)
	}
	if r.System != "You are Codex." {
		t.Fatalf("system %q", r.System)
	}
	var call *Part
	for _, m := range r.Messages {
		for i, p := range m.Parts {
			if p.Kind == ToolCall {
				call = &m.Parts[i]
			}
		}
	}
	if call == nil || call.Name != "shell" {
		t.Fatalf("earlier call: %+v", call)
	}
}

// Lite tools sent beside tools already there are added once, a namespace
// given again adding the tools it didn't have; an empty additional_tools
// just goes; a request without one is left as it was.
func TestAdditionalToolsMerged(t *testing.T) {
	in := `{"model":"m","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},
		{"type":"namespace","name":"mcp__probe","tools":[{"type":"function","name":"write"}]}],
		"input":[{"type":"additional_tools","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"function","name":"shell","description":"again"}]},
			{"type":"namespace","name":"mcp__probe","tools":[{"type":"function","name":"read"},{"type":"function","name":"write"}]}]},
		{"type":"message","role":"user","content":"hi"}]}`
	var got struct {
		Tools []rTool          `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	out := liftAdditionalTools([]byte(in))
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 2 || got.Tools[0].Description != "" || len(got.Tools[1].Tools) != 2 || got.Tools[1].Tools[1].Name != "read" || len(got.Input) != 1 {
		t.Fatalf("merged: %s", out)
	}
	if again := liftAdditionalTools(out); string(again) != string(out) {
		t.Fatalf("lifted twice: %s", again)
	}

	out = liftAdditionalTools([]byte(`{"model":"m","input":[{"type":"additional_tools","tools":[]},{"type":"message","role":"user","content":"hi"}]}`))
	if strings.Contains(string(out), "additional_tools") || strings.Contains(string(out), `"tools"`) || !strings.Contains(string(out), `"content":"hi"`) {
		t.Fatalf("empty: %s", out)
	}
	plain := `{"model":"m","input":"what are additional_tools?"}`
	if out := liftAdditionalTools([]byte(plain)); string(out) != plain {
		t.Fatalf("plain: %s", out)
	}
}
