package provider

import "testing"

// Each of Go's URLs a request can go to has Zen's free tier's beside it,
// where OpenCode 2 asks it; any other URL has none.
func TestOpenCodeGoToZenFree(t *testing.T) {
	for _, c := range []struct {
		u     string
		proto Protocol
		want  string
	}{
		{"https://opencode.ai/zen/go/v1/chat/completions", Chat, "https://opencode.ai/inference/openai/v1/chat/completions"},
		{"https://opencode.ai/zen/go/v1/responses", Responses, "https://opencode.ai/inference/openai/v1/responses"},
		{"https://opencode.ai/zen/go/v1/messages", Anthropic, "https://opencode.ai/inference/anthropic/v1/messages"},
		{"https://opencode.ai/inference/go/v1/chat/completions", Chat, "https://opencode.ai/inference/openai/v1/chat/completions"},
		{"https://opencode.ai/inference/go/openai/v1/chat/completions", Chat, "https://opencode.ai/inference/openai/v1/chat/completions"},
		{"https://opencode.ai/inference/go/anthropic/v1/messages", Anthropic, "https://opencode.ai/inference/anthropic/v1/messages"},
		{"https://opencode.ai/zen/v1/chat/completions", Chat, ""},
		{"https://opencode.ai/inference/custom/v1/chat/completions", Chat, ""},
		{"https://example.com/zen/go/v1/chat/completions", Chat, ""},
	} {
		if got := openCodeGoToZenFree(c.u, c.proto); got != c.want {
			t.Errorf("%s: %q, want %q", c.u, got, c.want)
		}
	}
}
