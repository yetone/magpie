package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A run started anew for a conversation with replies in it is told it in
// one message: the turns answered already are marked as such, with what
// was sent in them, apart from the turn to answer now (#1365). Told as one
// stretch of Human:/Assistant: text, every image in it read as just sent.
func TestClaudePromptMarksEarlierTurns(t *testing.T) {
	img := Part{Kind: Image, MediaType: "image/png", Data: "iVBORw0KGgo="}
	req := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "look at this"}, img}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "a cat"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "and this one"}, img}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "ok"}, img}},
	}}
	blocks, err := renderClaudePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	var kinds []string
	for _, b := range blocks {
		if b["type"] == "image" {
			kinds = append(kinds, "image")
			all.WriteString("<IMG>")
			continue
		}
		s, _ := b["text"].(string)
		all.WriteString(s)
	}
	got := all.String()
	open, end := strings.Index(got, "<conversation_history>"), strings.Index(got, "</conversation_history>")
	cur := strings.Index(got, "<current_turn>")
	if open < 0 || end < open || cur < end || !strings.HasSuffix(got, "</current_turn>") {
		t.Fatalf("the turns aren't marked:\n%s", got)
	}
	history, now := got[open:end], got[cur:]
	if !strings.Contains(history, "Human: look at this<IMG>") || !strings.Contains(history, "Assistant: a cat") || strings.Contains(history, "and this one") {
		t.Fatalf("history:\n%s", history)
	}
	if !strings.Contains(now, "Human: and this one<IMG>") || !strings.Contains(now, "[tool result id=c1]") || strings.Count(now, "<IMG>") != 2 {
		t.Fatalf("the turn to answer:\n%s", now)
	}

	// a first turn, or a conversation with no reply in it, is told as before
	for _, msgs := range [][]Message{req.Messages[:1], {req.Messages[0], req.Messages[0]}} {
		blocks, _ := renderClaudePrompt(&Request{Messages: msgs})
		b, _ := json.Marshal(blocks)
		if strings.Contains(string(b), "conversation_history") || strings.Contains(string(b), "current_turn") {
			t.Fatalf("marked a conversation with no reply: %s", b)
		}
	}
}
