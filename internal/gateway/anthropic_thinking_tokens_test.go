package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// copilotThinkingReply is Claude's reply as Copilot's /v1/messages streamed
// it (claude-opus-5.5 at effort high, 2026-10-24), the signature cut short:
// its message_delta counts 710 of the 1277 output tokens as thinking, in
// output_tokens_details.
var copilotThinkingReply = []string{
	`data: {"message":{"container":null,"content":[],"context_management":null,"diagnostics":null,"id":"msg_011CgKVXZcKWy1fj39CoDg5H","model":"claude-opus-5-5","role":"assistant","stop_details":null,"stop_reason":null,"stop_sequence":null,"type":"message","usage":{"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":3858},"cache_creation_input_tokens":3858,"cache_read_input_tokens":12030,"inference_geo":"global","input_tokens":2,"output_tokens":4,"speed":"standard"}},"type":"message_start"}`,
	`data: {"content_block":{"signature":"","thinking":"","type":"thinking"},"index":0,"type":"content_block_start"}`,
	`data: {"delta":{"thinking":"","type":"thinking_delta"},"index":0,"type":"content_block_delta"}`,
	`data: {"delta":{"signature":"CAQSpQ8KEAgTGAI4AUIIdGhp","type":"signature_delta"},"index":0,"type":"content_block_delta"}`,
	`data: {"index":0,"type":"content_block_stop"}`,
	`data: {"content_block":{"text":"","type":"text"},"index":1,"type":"content_block_start"}`,
	`data: {"delta":{"text":"ANSWER: 1245","type":"text_delta"},"index":1,"type":"content_block_delta"}`,
	`data: {"index":1,"type":"content_block_stop"}`,
	`data: {"context_management":{"applied_edits":[]},"copilot_usage":{"token_details":[{"batch_size":1000000,"cost_per_batch":400000000000,"model":"claude-opus-5.5","token_count":2,"token_type":"input"},{"batch_size":1000000,"cost_per_batch":20000000000,"model":"claude-opus-5.5","token_count":12030,"token_type":"cache_read"},{"batch_size":1000000,"cost_per_batch":500000000000,"model":"claude-opus-5.5","token_count":3858,"token_type":"cache_write"},{"batch_size":1000000,"cost_per_batch":2000000000000,"model":"claude-opus-5.5","token_count":1277,"token_type":"output"}],"total_nano_aiu":4724400000},"delta":{"container":null,"stop_details":null,"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta","usage":{"cache_creation_input_tokens":3858,"cache_read_input_tokens":12030,"input_tokens":2,"iterations":[{"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":3858},"cache_creation_input_tokens":3858,"cache_read_input_tokens":12030,"input_tokens":2,"output_tokens":1277,"type":"message"}],"output_tokens":1277,"output_tokens_details":{"thinking_tokens":710}}}`,
	`data: {"type":"message_stop"}`,
}

// Claude's thinking is counted from what its usage says, as every other
// protocol's reasoning is: a reply relayed as it came and one translated
// for a Chat or Responses client both record its 710 thinking tokens, and
// the translated reply tells the client so. Before, an Anthropic reply's
// usage row had no reasoning at all.
func TestAnthropicThinkingTokensCounted(t *testing.T) {
	for _, tc := range []struct{ path, told string }{
		{"/v1/messages", `"thinking_tokens":710`},
		{"/v1/chat/completions", `"reasoning_tokens":710`},
		{"/v1/responses", `"reasoning_tokens":710`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(&paced{events: copilotThinkingReply})
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Anthropic: up.URL, Key: "k", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":300}`
			if tc.path == "/v1/responses" {
				body = `{"model":"fake/m1","stream":true,"input":"hi"}`
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(body)))
			u := lastUsage(t)
			if rec.Code != 200 || u.Output != 1277 || u.Reasoning != 710 {
				t.Fatalf("thinking tokens not counted: %d output %d reasoning %d\n%s", rec.Code, u.Output, u.Reasoning, rec.Body)
			}
			// counted, its reasoning isn't marked hidden, as one a stream shows
			// and its usage doesn't count is (#4545), but for a Responses
			// client's, which is marked hidden however it went
			if u.ReasoningHidden && tc.path != "/v1/responses" {
				t.Fatalf("710 thinking tokens counted, and the reasoning still marked hidden")
			}
			if !strings.Contains(rec.Body.String(), tc.told) {
				t.Fatalf("the client isn't told the thinking tokens (%s):\n%s", tc.told, rec.Body)
			}
		})
	}
}

// A Claude Code client served by a Chat model that reasoned is told how
// many of the output tokens were thinking, in output_tokens_details as
// Copilot tells it, as a Chat or Responses client is told in its own
// usage.
func TestTranslatedReasoningToldAsThinkingTokens(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(&paced{events: []string{
		`data: {"choices":[{"delta":{"reasoning_content":"a thought"}}]}`,
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":120,"completion_tokens_details":{"reasoning_tokens":100}}}`,
		`data: [DONE]`,
	}})
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":300}`)))
	if u := lastUsage(t); rec.Code != 200 || u.Reasoning != 100 {
		t.Fatalf("%d %+v\n%s", rec.Code, u, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"output_tokens_details":{"thinking_tokens":100}`) {
		t.Fatalf("message_delta doesn't tell the thinking tokens:\n%s", rec.Body)
	}
}
