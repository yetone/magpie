package gateway

import (
	"encoding/json"
	"testing"
)

// TestResponsesToolStrict: a tool sent on to a Responses API says strict, as
// Codex says it. Left out, the ChatGPT backend held MiniMax Code's tools to
// strict mode and refused a path pattern with a lookaround ("regex lookaround
// is not supported", #383); a client asking for strict still gets it.
func TestResponsesToolStrict(t *testing.T) {
	schema := `{"type":"object","properties":{"path":{"type":"string","pattern":"^(?!.*\\.\\.).+$"}}}`
	strictOf := func(r *Request) map[string]any {
		var out struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(buildResponses(r, "gpt-6.1-sol", "chatgpt.com", false), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]any{}
		for _, tool := range out.Tools {
			v, ok := tool["strict"]
			if !ok {
				t.Fatalf("tool %v has no strict", tool["name"])
			}
			got[tool["name"].(string)] = v
		}
		return got
	}

	r, err := parseAnthropic([]byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"read","description":"Read a file","input_schema":` + schema + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strictOf(r); got["read"] != false {
		t.Fatalf("Anthropic tool: strict %v", got)
	}

	r, err = parseResponses([]byte(`{"model":"m","input":"hi","tools":[
		{"type":"function","name":"loose","parameters":` + schema + `},
		{"type":"function","name":"held","strict":true,"parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strictOf(r); got["loose"] != false || got["held"] != true {
		t.Fatalf("Responses tools: strict %v", got)
	}

	r, err = parseChat([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"held","strict":true,"parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strictOf(r); got["held"] != true {
		t.Fatalf("Chat tool: strict %v", got)
	}
}
