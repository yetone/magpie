package gateway

import (
	"encoding/json"
	"testing"
)

// A request built in Responses for GPT-6.2 carries its system prompt first
// in the input: the model reads top-level instructions after the input,
// uncached every turn. Another model's goes on top as before.
func TestResponsesSystemFirstInInputOnGPT62(t *testing.T) {
	r := &Request{System: "You are Claude Code.", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	var m struct {
		Instructions *string          `json:"instructions"`
		Input        []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(buildResponses(r, "gpt-6.2-sol", "api.openai.com", false), &m); err != nil {
		t.Fatal(err)
	}
	if m.Instructions != nil || len(m.Input) != 2 || m.Input[0]["role"] != "developer" || m.Input[1]["role"] != "user" {
		t.Fatalf("gpt-6.2-sol: %+v", m)
	}
	c, _ := m.Input[0]["content"].([]any)
	if p, _ := c[0].(map[string]any); len(c) != 1 || p["text"] != "You are Claude Code." {
		t.Fatalf("gpt-6.2-sol developer item: %+v", m.Input[0])
	}

	m.Instructions, m.Input = nil, nil
	if err := json.Unmarshal(buildResponses(r, "gpt-6-sol", "api.openai.com", false), &m); err != nil {
		t.Fatal(err)
	}
	if m.Instructions == nil || *m.Instructions != "You are Claude Code." || len(m.Input) != 1 {
		t.Fatalf("gpt-6-sol: %+v", m)
	}
}
