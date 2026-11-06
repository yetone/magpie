package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A vendor's "too long" reaches the agent in its own API's words, so it
// compacts and retries: Claude Code on "prompt is too long", Codex and
// chat clients on context_length_exceeded.
func TestContextOverflowSaidTheClientsWay(t *testing.T) {
	volc := "Volcengine: Input exceeds the context limit (1048568 tokens)"
	for _, tc := range []struct {
		proto    provider.Protocol
		status   int
		msg      string
		wantCode string
		wantMsg  string
	}{
		{provider.Anthropic, 400, volc, "", "prompt is too long: " + volc},
		{provider.Chat, 400, volc, "context_length_exceeded", volc},
		{provider.Responses, 413, volc, "context_length_exceeded", volc},
		{provider.Responses, 400, "relay: This model's maximum context length is 131072 tokens. However, you requested 140000 tokens", "context_length_exceeded", ""},
		{provider.Anthropic, 400, "a: prompt is too long: 210000 tokens > 200000 maximum", "", "a: prompt is too long: 210000 tokens > 200000 maximum"},
		{provider.Chat, 400, "zhipu: 输入内容超过模型最大上下文长度", "context_length_exceeded", ""},
		// not the conversation: left as the vendor said it
		{provider.Chat, 400, "x: max_tokens is too large: 100000 exceeds the model's context", "", ""},
		{provider.Chat, 400, "x: invalid tool schema", "", ""},
		{provider.Chat, 502, "x: upstream exceeds context budget", "", ""},
	} {
		w := httptest.NewRecorder()
		status := writeError(w, tc.proto, tc.status, tc.msg)
		var body struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(w.Body.Bytes(), &body)
		code, _ := body.Error.Code.(string)
		overflow := tc.wantCode != "" || strings.HasPrefix(tc.wantMsg, "prompt is too long") || strings.Contains(tc.wantMsg, ": prompt is too long")
		switch {
		case tc.proto == provider.Anthropic && tc.wantMsg != "" && body.Error.Message != tc.wantMsg,
			tc.proto != provider.Anthropic && code != tc.wantCode,
			overflow && (status != 400 || body.Error.Type != "invalid_request_error"),
			!overflow && status != tc.status:
			t.Errorf("%s %d %q: status %d body %s", tc.proto, tc.status, tc.msg, status, w.Body.String())
		}
	}
}

// Each vendor's way of saying the conversation is too long, as pi
// collected them (packages/ai/src/utils/overflow.ts), reaches the agent as
// context_length_exceeded, so it compacts; and none is taken for a used-up
// plan or asked of another account, which holds the conversation no
// better: Kimi's "exceeded model token limit" rested the account as out of
// quota, z.ai's model_context_window_exceeded went to every account in
// turn.
func TestEveryVendorsOverflow(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
	}{
		{400, "The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"},
		{400, "This model's maximum prompt length is 131072 but the request contains 537812 tokens"},
		{400, "Please reduce the length of the messages or completion"},
		{400, "Input length 2000 exceeds the maximum allowed input length of 1000 tokens."},
		{400, "The input (2000 tokens) is longer than the model's context length (1000 tokens)."},
		// the reporter's vendor (#4040): no 's on the model
		{400, "The message is longer than the model context length"},
		{400, "the request exceeds the available context size, try increasing it"},
		{400, "tokens to keep from the initial prompt is greater than the context length"},
		{400, "prompt token count of 2000 exceeds the limit of 1000"},
		{400, "invalid params, context window exceeds limit"},
		{400, "Your request exceeded model token limit: 1000 (requested: 2000)"},
		{400, "Prompt has 2000 tokens, but the configured context size is 1000 tokens"},
		{400, `{"code":"1261","message":"Prompt too long"}`},
		{400, `{"code":"1261","message":"Prompt exceeds max length"}`},
		{400, "model_context_window_exceeded"},
		{400, "prompt too long; exceeded max context length by 100 tokens"},
		{400, "Range of input length should be [1, 98304]"},
		{400, "token limit exceeded"},
		// Chinese that names the input, not the context (#4420, MYCF711)
		{400, "内容太长了，请精简输入或开启新对话"},
		{400, `{"error":{"code":"1261","message":"Prompt 超长"}}`},
		{400, "token数量超过上限。对话历史+问题的字数太多，需要精简输入"},
		{400, "输入过长，请缩短后重试"},
		{400, "输入长度超过限制"},
		{400, "输入内容超过模型最大token数"},
		{400, "提示词超出最大长度"},
		{413, "请求内容过长"},
	} {
		if !tooLong(tc.status, tc.msg) {
			t.Errorf("%q isn't taken for a conversation too long", tc.msg)
		}
		if retryable(tc.status, []byte(tc.msg)) {
			t.Errorf("%q is asked of another account", tc.msg)
		}
		if f := failure(tc.status, []byte(tc.msg)); f != failOther {
			t.Errorf("%q is a %s failure", tc.msg, f)
		}
	}
	// a rate limit worded with tokens is still one
	if tooLong(429, "Too many tokens, please wait before trying again.") {
		t.Error("a rate limit taken for a conversation too long")
	}
}

// A Chinese pace limit beside words of size, or a length that isn't the
// conversation's, isn't taken for a conversation too long (#4420): told
// so, the agent compacts for nothing, and a pace limit worded so would
// never be asked of the next account.
func TestChineseNotTooLong(t *testing.T) {
	for _, msg := range []string{
		"内容太长，已达到速率限制",
		"请求过于频繁，输入内容过长的请求请稍后再试",
		"输入token数超过每分钟限制",
		"输入过长：已超过并发上限",
		"每日token数量超过上限",
		"token数量超过上限",
		"名称超出最大长度",
		"输出过长，请调小 max_tokens",
		"文件名过长",
		"参数错误",
		"服务出现问题，请联系管理员",
		"模型不存在",
	} {
		if tooLong(400, msg) {
			t.Errorf("%q taken for a conversation too long", msg)
		}
	}
	// the pace limit said with size still goes to the next account
	if !retryable(400, []byte("内容太长，已达到速率限制")) {
		t.Error("a pace limit worded with size isn't asked of the next account")
	}
	for _, msg := range []string{"请求过于频繁", "已达到速率限制"} {
		if f := failure(429, []byte(msg)); f != failRate {
			t.Errorf("429 %q is a %s failure", msg, f)
		}
	}
}

// A spent budget, like a used-up plan (pi's retry.ts lists both as not to
// be retried), goes to the next account at once rather than being asked
// again in place: "out of budget" was a rate limit, asked three times.
// LiteLLM's spent key answers 429 "Budget has been exceeded! Key=… (sk-…),
// Current cost: …, Max budget: …" (#5111, wuxiaoqiang12): its "has been"
// sat between the two words the list knew, and the key was asked again
// every half an hour, the user's quota-rest never read.
func TestOutOfBudgetIsntRetriedInPlace(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
	}{
		{429, "out of budget"},
		{400, "Budget exceeded for this key"},
		{429, `{"error":{"message":"Budget has been exceeded! Key=sk-1234 (my-relay), Current cost: 0.0023, Max budget: 0.002"}}`},
		{429, "Budget has been exceeded! Current cost: 0.0023, Max budget: 0.002"},
		{429, `{"error":{"type":"GoUsageLimitError","message":"Monthly usage limit reached"}}`},
		{429, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`},
	} {
		if !retryable(tc.status, []byte(tc.msg)) {
			t.Errorf("%d %q isn't asked of the next account", tc.status, tc.msg)
		}
		if f := failure(tc.status, []byte(tc.msg)); f != failQuota {
			t.Errorf("%d %q is a %s failure", tc.status, tc.msg, f)
		}
		if _, again := passing(tc.status, nil, []byte(tc.msg), 0); again {
			t.Errorf("%d %q is asked again in place", tc.status, tc.msg)
		}
	}
	if _, again := passing(429, nil, []byte("Rate limit reached for requests"), 0); !again {
		t.Error("a rate limit isn't asked again")
	}
	// the relay's own per-minute limit names no budget, and stays a rate
	if f := failure(429, []byte("LiteLLM: Rate limit exceeded")); f != failRate {
		t.Errorf(`"LiteLLM: Rate limit exceeded" is a %s failure`, f)
	}
}
