package gateway

import (
	"testing"
)

// A Chat client sends an earlier assistant turn's thinking back in any of
// three spellings: reasoning_content (DeepSeek's, which OpenCode and most
// clients send), reasoning (OpenRouter's) and reasoning_details (OpenRouter's
// structured one). parseChat read only reasoning_content, so a request
// translated to a non-Chat upstream — an Anthropic key, a Codex or
// Antigravity account, a Gemini or DeepSeek key — lost the thinking of every
// turn sent in the other two spellings, and the model went on without its own
// reasoning. A tool-calling session shows it from its second turn on.
func TestParseChatReadsEveryReasoningSpelling(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "reasoning_content",
			body: `{"model":"deepseek/deepseek-v4.1","messages":[
				{"role":"user","content":"Read go.mod and bump the go directive."},
				{"role":"assistant","reasoning_content":"go.mod has to be read before it is changed.",
				 "tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":"module github.com/yetone/magpie\n\ngo 1.24\n"}]}`,
			want: "go.mod has to be read before it is changed.",
		},
		{
			name: "reasoning",
			body: `{"model":"openrouter/qwen3-coder","messages":[
				{"role":"user","content":"Read go.mod and bump the go directive."},
				{"role":"assistant","reasoning":"The user wants the go directive at 1.25, so read go.mod first.",
				 "tool_calls":[{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},
				{"role":"tool","tool_call_id":"call_2","content":"module github.com/yetone/magpie\n\ngo 1.24\n"}]}`,
			want: "The user wants the go directive at 1.25, so read go.mod first.",
		},
		{
			name: "reasoning_details",
			body: `{"model":"openrouter/qwen3-coder","messages":[
				{"role":"user","content":"Read go.mod and bump the go directive."},
				{"role":"assistant","reasoning_details":[
					{"type":"reasoning.text","text":"A read comes before an edit.","signature":null,"format":"unknown","index":0}],
				 "tool_calls":[{"id":"call_3","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},
				{"role":"tool","tool_call_id":"call_3","content":"module github.com/yetone/magpie\n\ngo 1.24\n"}]}`,
			want: "A read comes before an edit.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseChat([]byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			turn := r.Messages[1]
			if len(turn.Parts) != 2 || turn.Parts[0].Kind != Thinking || turn.Parts[1].Kind != ToolCall {
				t.Fatalf("the assistant turn's parts: %+v", turn.Parts)
			}
			if turn.Parts[0].Text != c.want {
				t.Errorf("thinking %q, want %q", turn.Parts[0].Text, c.want)
			}
			if turn.Parts[1].Name != "read_file" || string(turn.Parts[1].Args) != `{"path":"go.mod"}` {
				t.Errorf("the tool call went with it: %+v", turn.Parts[1])
			}
		})
	}
}

// The three spellings are the same thinking, so a client that sends more than
// one of them (OpenRouter's reasoning_details with reasoning beside it, say)
// is read once: reasoning_content first, then reasoning, then
// reasoning_details' text — messageReasoning's order, which chattidy's
// passthrough keeps.
func TestParseChatTakesTheFirstReasoningSpellingSent(t *testing.T) {
	body := `{"model":"deepseek/deepseek-v4.1","messages":[
		{"role":"user","content":"Read go.mod and bump the go directive."},
		{"role":"assistant",
		 "reasoning_content":"the reasoning_content one",
		 "reasoning":"the reasoning one",
		 "reasoning_details":[{"type":"reasoning.text","text":"the reasoning_details one","index":1}],
		 "content":"done"}]}`
	r, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	turn := r.Messages[1]
	if len(turn.Parts) != 2 || turn.Parts[0].Kind != Thinking || turn.Parts[0].Text != "the reasoning_content one" || turn.Parts[1].Kind != Text {
		t.Fatalf("the assistant turn's parts: %+v", turn.Parts)
	}
}
