package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// Google turns the whole request away over a function name that starts with
// a digit or a dash, and over a property with no name (fwxkogaia on X).
func TestCodeAssistToolNamesGoogleTakes(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"":{"type":"string"},"q":{"type":"string"}},"required":["","q"]}`)
	r := &Request{Model: "gemini-4-carbon", Tools: []Tool{{Name: "1pass_get", Schema: schema}, {Name: "-dash", Schema: schema}, {Name: "ok", Schema: schema}},
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
			{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "1pass_get", Args: json.RawMessage(`{}`)}}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "x"}}},
		}}
	out := string(buildCodeAssistSent(r, "gemini-4-carbon", "antigravity"))
	for _, bad := range []string{`"name":"1pass_get"`, `"name":"-dash"`, `"":`} {
		if strings.Contains(out, bad) {
			t.Errorf("request still has %s: %s", bad, out)
		}
	}
	if n := strings.Count(out, `"name":"_x_1pass_get"`); n != 3 {
		t.Errorf("_x_1pass_get in declaration, call and result: %d, want 3: %s", n, out)
	}
	if got := toolOfCall("_x_1pass_get"); got != "1pass_get" {
		t.Errorf("decoded %q", got)
	}
	if got := toolOfCall("default_api:_x_-dash"); got != "-dash" {
		t.Errorf("decoded %q", got)
	}
}

// A reply that answered nothing is asked again in place, so the request
// is asked again once, not as often as an outage (fwxkogaia on X).
func TestEmptyReplyAskedAgainOnce(t *testing.T) {
	body := []byte(`{"error":{"message":"antigravity: ` + emptyReply + `"}}`)
	if _, ok := passing(502, nil, body, 0); !ok {
		t.Error("an empty reply is asked again once")
	}
	if _, ok := passing(502, nil, body, 1); ok {
		t.Error("an empty reply is not asked again a second time over")
	}
	if _, ok := passing(502, nil, []byte(`bad gateway`), 1); !ok {
		t.Error("a busy vendor is still asked again")
	}
}
