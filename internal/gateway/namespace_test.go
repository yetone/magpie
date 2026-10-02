package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex offers some tools inside a namespace — spawn_agent is
// collaboration's — and names the namespace beside the tool in its calls.
const namespacedTools = `"tools":[
	{"type":"function","name":"exec_command","parameters":{"type":"object"}},
	{"type":"namespace","name":"collaboration","description":"Tools in the collaboration namespace.","tools":[
		{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string"}}}},
		{"type":"custom","name":"freeform","description":"no JSON schema"}
	]}
]`

func TestNamespacedToolsOffered(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":"hi",` + namespacedTools + `}`))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); got != "exec_command,collaboration__spawn_agent,collaboration__freeform" {
		t.Fatalf("tools = %s", got)
	}
	if r.Tools[1].Description != "Spawns an agent." || !strings.Contains(string(r.Tools[1].Schema), "message") {
		t.Fatalf("spawn_agent lost its description or schema: %+v", r.Tools[1])
	}
	if q := r.Namespaced["collaboration__spawn_agent"]; q != (nsTool{Namespace: "collaboration", Name: "spawn_agent"}) {
		t.Fatalf("namespaced = %+v", r.Namespaced)
	}
	if _, ok := r.Namespaced["exec_command"]; ok {
		t.Fatal("a top-level tool was put in a namespace")
	}
}

func TestNamespacedCallInHistory(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m",` + namespacedTools + `,"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"spawn_agent","namespace":"collaboration","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var call *Part
	for i := range r.Messages {
		for j := range r.Messages[i].Parts {
			if p := &r.Messages[i].Parts[j]; p.Kind == ToolCall {
				call = p
			}
		}
	}
	if call == nil || call.Name != "collaboration__spawn_agent" {
		t.Fatalf("history call = %+v", call)
	}
}

func TestLongNamespacedNamesFit(t *testing.T) {
	ns := "mcp__" + strings.Repeat("a", 60)
	body := `{"model":"m","input":"hi","tools":[{"type":"namespace","name":"` + ns + `","tools":[
		{"type":"function","name":"` + strings.Repeat("b", 40) + `1"},
		{"type":"function","name":"` + strings.Repeat("b", 40) + `2"}]}]}`
	r, err := parseResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 2 || r.Tools[0].Name == r.Tools[1].Name {
		t.Fatalf("tools = %+v", r.Tools)
	}
	for i, tl := range r.Tools {
		if len(tl.Name) > 64 {
			t.Fatalf("%q is longer than 64", tl.Name)
		}
		if q := r.Namespaced[tl.Name]; q.Namespace != ns || !strings.HasSuffix(q.Name, string(rune('1'+i))) {
			t.Fatalf("%q maps to %+v", tl.Name, q)
		}
	}
}

func namespacedReq() *Request {
	return &Request{Model: "m", Namespaced: map[string]nsTool{"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"}}}
}

func TestNamespacedCallStreamed(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), namespacedReq())
	enc.event(Event{Kind: KToolStart, ID: "c1", Name: "collaboration__spawn_agent"})
	enc.event(Event{Kind: KToolArgs, Text: `{"message":"x"}`})
	enc.event(Event{Kind: KToolStart, ID: "c2", Name: "exec_command"})
	enc.event(Event{Kind: KToolArgs, Text: `{}`})
	enc.finish()
	var done []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil && (m["type"] == "response.output_item.added" || m["type"] == "response.output_item.done") {
			done = append(done, m["item"].(map[string]any))
		}
	}
	if len(done) != 4 {
		t.Fatalf("items = %v", done)
	}
	for _, it := range done {
		switch it["call_id"] {
		case "c1":
			if enc, ok := it["encrypted_function_args"].([]any); !ok || len(enc) != 0 || it["name"] != "spawn_agent" || it["namespace"] != "collaboration" {
				t.Fatalf("namespaced call = %v", it)
			}
		case "c2":
			_, sealed := it["encrypted_function_args"]
			if _, has := it["namespace"]; has || sealed || it["name"] != "exec_command" {
				t.Fatalf("top-level call = %v", it)
			}
		}
	}
}

func TestNamespacedCallRendered(t *testing.T) {
	res := Result{Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "collaboration__spawn_agent", Args: json.RawMessage(`{}`)}}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(render(provider.Responses, res, namespacedReq()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 1 || out.Output[0]["name"] != "spawn_agent" || out.Output[0]["namespace"] != "collaboration" {
		t.Fatalf("output = %v", out.Output)
	}
}
