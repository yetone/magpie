package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// The ChatGPT backend that doesn't run MultiAgentV2 turns a plain
// agent_message item away with 400 "Unsupported input item: agent_message"
// (cyberestro on X, tweet 2125516377943691637): the turn is asked again
// with each plain agent_message rewritten as the user's message the backend
// takes, its task text kept, so the request no longer dies on the item.
func TestCodexAgentMessageRefusalAskedAgainAsUser(t *testing.T) {
	// the reporter's shape: a prior user turn, then the subagent's task as
	// an agent_message with a From/To and its input_text payload
	body := []byte(`{"model":"inkling-2-mini-q4","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"agent_message","author":"/root","recipient":"/root/research","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\nresearch the failure"}]}` +
		`]}`)
	// the backend's literal refusal (OpenAI's invalid_request_error body)
	msg := []byte(`{"error":{"message":"Unsupported input item: agent_message","type":"invalid_request_error","param":"input","code":null}}`)

	out, ok := withoutUnreadable(body, msg)
	if !ok {
		t.Fatal("the agent_message refusal was not recovered from")
	}
	if strings.Contains(string(out), `"agent_message"`) {
		t.Fatalf("agent_message still in the retried body:\n%s", out)
	}

	var q struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 2 {
		t.Fatalf("input = %+v", q.Input)
	}
	it := q.Input[1]
	if it.Type != "message" || it.Role != "user" {
		t.Fatalf("converted item = %+v", it)
	}
	var sb strings.Builder
	for _, c := range it.Content {
		sb.WriteString(c.Text)
	}
	got := sb.String()
	if !strings.Contains(got, "From /root to /root/research") {
		t.Fatalf("missing routing header, got %q", got)
	}
	if !strings.Contains(got, "Message Type: NEW_TASK\nPayload:\nresearch the failure") {
		t.Fatalf("task text lost, got %q", got)
	}
}

// A sealed agent_message is left as it came: only a ChatGPT account can
// open it, and magpie reads no sealed task, so the refusal recovery does
// not touch it (the sealed-task guard still sees it).
func TestCodexAgentMessageRefusalLeavesSealed(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"agent_message","author":"/root","content":[{"type":"encrypted_content","encrypted_content":"gAAAAASealed=="}]}` +
		`]}`)
	msg := []byte(`{"error":{"message":"Unsupported input item: agent_message","type":"invalid_request_error"}}`)
	if _, ok := withoutUnreadable(body, msg); ok {
		t.Fatal("a sealed agent_message must be left as it came")
	}
}

// A 400 that is not the agent_message refusal does not rewrite the item.
func TestCodexAgentMessageRefusalScopedToItsMessage(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"agent_message","content":[{"type":"input_text","text":"x"}]}` +
		`]}`)
	msg := []byte(`{"error":{"message":"Some other bad request","type":"invalid_request_error"}}`)
	if out, ok := withoutUnreadable(body, msg); ok {
		t.Fatalf("an unrelated 400 rewrote the item:\n%s", out)
	}
}
