package gateway

import (
	"encoding/json"
	"testing"
)

// Claude 4.5 and later stop with model_context_window_exceeded when the
// reply runs into the context window: the reply is cut short, as with
// max_tokens, and a Chat Completions or Responses client must be told so.
func TestContextWindowStopIsTruncation(t *testing.T) {
	var col collector
	for _, c := range []string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Half an ans"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":5}}`,
	} {
		decodeAnthropic(c, col.add)
	}
	res := col.finish()
	var chat struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	json.Unmarshal(renderChat(res, "m"), &chat)
	if got := chat.Choices[0].FinishReason; got != "length" {
		t.Errorf("chat finish_reason = %q, want length", got)
	}
	var resp struct {
		Status string `json:"status"`
	}
	json.Unmarshal(renderResponses(res, "m"), &resp)
	if resp.Status != "incomplete" {
		t.Errorf("responses status = %q, want incomplete", resp.Status)
	}
}
