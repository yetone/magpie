package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// wbTextStream is a WorkBuddy (copilot.tencent.com) reply as it streams:
// every delta, text too, carries "tool_calls": [] and, once the thinking
// is over, an empty reasoning_content and finish_reason.
func wbTextStream() string {
	chunk := func(delta, finish string) string {
		return `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}`
	}
	return sse(
		chunk(`{"role":"assistant","content":"","reasoning_content":"Look first.","tool_calls":[]}`, `""`),
		chunk(`{"content":"我来","reasoning_content":"","tool_calls":[]}`, `""`),
		chunk(`{"content":"看","reasoning_content":"","tool_calls":[]}`, `""`),
		chunk(`{"content":"一下","reasoning_content":"","tool_calls":[]}`, `""`),
		chunk(`{"content":"。","reasoning_content":"","tool_calls":[]}`, `""`),
		chunk(`{"content":"","reasoning_content":"","tool_calls":[]}`, `"stop"`),
		`data: [DONE]`)
}

// A relayed WorkBuddy stream reaches a Chat client without the empty
// tool_calls list on each delta: Qoder closed its text block on each one,
// so its reply read a word to a line (ARNO on Discord: workbuddy provider
// 断句出错). The text, the thinking and the finish are as they came.
func TestChatRelayDropsEmptyToolCalls(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", wbTextStream()}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"tool_calls":[]`) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, s := range []string{`"content":"我来"`, `"content":"看"`, `"content":"一下"`, `"content":"。"`,
		`"reasoning_content":"Look first."`, `"finish_reason":"stop"`} {
		if !strings.Contains(body, s) {
			t.Fatalf("no %s in %s", s, body)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatal(body)
	}
}

// A tool call's own list is left as it came.
func TestChatTidyKeepsToolCalls(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"","tool_calls":[{"index":0,"id":"call_1","function":{"name":"grep","arguments":""}}]}}]}` + "\n"
	if got := string(tidyLine([]byte(line))); got != line {
		t.Fatalf("%s", got)
	}
}

// The same stream spoken to Claude Code's API or to Codex's is one text
// block, one output_text item, after the thinking: the decoder already
// takes an empty list for no call.
func TestWorkBuddyTextIsOneBlock(t *testing.T) {
	for _, c := range []struct{ path, body, open string }{
		{"/v1/messages", `{"model":"a/m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			`"content_block":{"text":"","type":"text"}`},
		{"/v1/responses", `{"model":"a/m","stream":true,"input":"hi"}`,
			`"type":"response.content_part.added"`},
	} {
		fresh(t)
		a := &scripted{replies: []reply{{200, "text/event-stream", wbTextStream()}}}
		scriptedOn(t, "a", provider.Chat, a)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
		body := rec.Body.String()
		if rec.Code != 200 || strings.Count(body, c.open) != 1 || !strings.Contains(body, "我来") || !strings.Contains(body, "一下") {
			t.Fatalf("%s: %d %s", c.path, rec.Code, body)
		}
	}
}
