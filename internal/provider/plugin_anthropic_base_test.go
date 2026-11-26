package provider

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// A Claude model through the Copilot plugin goes to Copilot's
// /v1/messages: the loader's base is the bare https://api.githubcopilot.com
// for every model, and the model's own URL (models.dev's) says /v1. Sent to
// the bare base it went to /messages and Copilot answered 404 (cyoking1 on
// X). What already ends in /v1, what isn't Anthropic's, and a model whose
// own URL says no /v1 go as they did.
func TestPluginAnthropicBaseKeepsV1(t *testing.T) {
	copilot := plugin.Provider{ID: "github-copilot", NPM: "@ai-sdk/openai-compatible", API: "https://api.githubcopilot.com"}
	claude := plugin.Model{ID: "claude-sonnet-5", NPM: "@ai-sdk/anthropic", URL: "https://api.githubcopilot.com/v1"}
	gpt := plugin.Model{ID: "gpt-5.5", NPM: "@ai-sdk/github-copilot", URL: "https://api.githubcopilot.com"}
	kiro := plugin.Provider{ID: "kiro", NPM: "@ai-sdk/anthropic"}
	kiroModel := plugin.Model{ID: "claude-sonnet-4.5", NPM: "@ai-sdk/anthropic", URL: "https://runtime.us-east-1.kiro.dev"}
	for _, c := range []struct {
		name   string
		pp     plugin.Provider
		m      plugin.Model
		loader string
		want   string
	}{
		{"copilot claude", copilot, claude, "https://api.githubcopilot.com", "https://api.githubcopilot.com/v1"},
		{"copilot claude, slash", copilot, claude, "https://api.githubcopilot.com/", "https://api.githubcopilot.com/v1"},
		{"copilot enterprise claude", copilot, claude, "https://copilot-api.example.ghe.com", "https://copilot-api.example.ghe.com/v1"},
		{"copilot gpt", copilot, gpt, "https://api.githubcopilot.com", "https://api.githubcopilot.com"},
		{"loader base has /v1", copilot, claude, "https://api.githubcopilot.com/v1", "https://api.githubcopilot.com/v1"},
		{"no loader base", copilot, claude, "", "https://api.githubcopilot.com/v1"},
		{"model url without /v1", kiro, kiroModel, "https://runtime.us-east-1.kiro.dev", "https://runtime.us-east-1.kiro.dev"},
		{"minimax", plugin.Provider{ID: "minimax"}, plugin.Model{ID: "MiniMax-M3", NPM: "@ai-sdk/anthropic", URL: "https://x.example/mavis/api/v1/llm/v1"}, "https://x.example/mavis/api/v1/llm/v1", "https://x.example/mavis/api/v1/llm/v1"},
	} {
		if got := pluginRequestBase(c.pp, c.m, c.loader); got != c.want {
			t.Errorf("%s: base %q, want %q", c.name, got, c.want)
		}
	}
}

// A model the plugin doesn't list (a test model the user typed in), asked
// on Anthropic's Messages API, goes where the plugin's own Claude models
// go, not under the provider's chat base (Anotiaww on X: WorkBuddy's
// /v2/messages is "404 Route Not Found"). WorkBuddy's plugin as it
// declares itself: the provider on chat at <endpoint>/v2, its Claude
// models on Messages at <endpoint>/v1, and no base of the loader's.
// Asked on chat, such a model stays on the provider's chat base; and a
// plugin with no model on Messages (WorkBuddy AI's) leaves it as it was.
func TestPluginUnlistedModelTakesItsAPIsRoute(t *testing.T) {
	wb := plugin.Provider{ID: "workbuddy", NPM: "@ai-sdk/openai-compatible", API: "https://copilot.tencent.com/v2", Models: []plugin.Model{
		{ID: "glm-5.3", NPM: "@ai-sdk/openai-compatible", URL: "https://copilot.tencent.com/v2"},
		{ID: "claude-opus-5.5", NPM: "@ai-sdk/anthropic", URL: "https://copilot.tencent.com/v1"},
	}}
	wbAI := plugin.Provider{ID: "workbuddy-ai", NPM: "@ai-sdk/openai-compatible", API: "https://www.workbuddy.ai/v2", Models: []plugin.Model{
		{ID: "glm-5.3", NPM: "@ai-sdk/openai-compatible", URL: "https://www.workbuddy.ai/v2"},
	}}
	copilot := plugin.Provider{ID: "github-copilot", NPM: "@ai-sdk/github-copilot", API: "https://api.githubcopilot.com", Models: []plugin.Model{
		{ID: "gpt-5.5", NPM: "@ai-sdk/github-copilot", URL: "https://api.githubcopilot.com"},
		{ID: "claude-sonnet-5", NPM: "@ai-sdk/anthropic", URL: "https://api.githubcopilot.com/v1"},
	}}
	for _, c := range []struct {
		name, model, rest, loader string
		pp                        plugin.Provider
		want                      string
	}{
		{"workbuddy, messages", "test-model-x", "/v1/messages", "", wb, "https://copilot.tencent.com/v1/messages"},
		{"workbuddy, claude-named", "claude-test-5", "/v1/messages", "", wb, "https://copilot.tencent.com/v1/messages"},
		{"workbuddy, count_tokens", "test-model-x", "/v1/messages/count_tokens", "", wb, "https://copilot.tencent.com/v1/messages/count_tokens"},
		{"workbuddy, chat", "test-model-x", "/v1/chat/completions", "", wb, "https://copilot.tencent.com/v2/chat/completions"},
		{"workbuddy ai, no messages route", "test-model-x", "/v1/messages", "", wbAI, "https://www.workbuddy.ai/v2/messages"},
		{"copilot, messages", "claude-test-6", "/v1/messages", "https://api.githubcopilot.com", copilot, "https://api.githubcopilot.com/v1/messages"},
		{"copilot, chat", "claude-test-6", "/v1/chat/completions", "https://api.githubcopilot.com", copilot, "https://api.githubcopilot.com/chat/completions"},
	} {
		m := unlistedModel(c.pp, c.model, c.rest)
		if m.ID != c.model {
			t.Errorf("%s: model %q, want %q", c.name, m.ID, c.model)
		}
		if got := pluginRequestBase(c.pp, m, c.loader) + strings.TrimPrefix(c.rest, "/v1"); got != c.want {
			t.Errorf("%s: sent to %q, want %q", c.name, got, c.want)
		}
	}
}
