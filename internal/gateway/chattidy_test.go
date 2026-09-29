package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// WorkBuddy's stream goes on sending an empty reasoning_content with the
// text once thinking is over; it reaches the agent without it, the
// thinking and the rest as they came.
func TestChatRelayDropsEmptyReasoning(t *testing.T) {
	fresh(t)
	up := sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The user said hi."}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi","reasoning_content":""}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"!","reasoning_content":""}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"reasoning_content":""`) ||
		!strings.Contains(body, `"reasoning_content":"The user said hi."`) ||
		!strings.Contains(body, `"content":"Hi"`) || !strings.Contains(body, `"content":"!"`) ||
		!strings.Contains(body, `"finish_reason":"stop"`) || !strings.Contains(body, `"total_tokens":5`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

// CodeBuddy CN's own stream shapes, verified against copilot.tencent.com:
// every chunk that hasn't finished says "finish_reason": "" where the
// contract has null, and every tool-call argument fragment after the
// first repeats "function": {"name": ""}. Both reach the agent mended —
// null, and no empty name — with the real name and the arguments as they
// came.
func TestChatRelayTidiesCodeBuddyStream(t *testing.T) {
	fresh(t)
	up := sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"The user","tool_calls":[]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"","tool_calls":[]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"grep","arguments":""},"index":0}]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"function":{"name":"","arguments":"{\"pat"},"index":0}]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"function":{"name":"","arguments":"tern\"}"},"index":0}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"finish_reason":""`) || strings.Contains(body, `"name":""`) ||
		strings.Contains(body, `"reasoning_content":""`) ||
		!strings.Contains(body, `"finish_reason":null`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(body, `"reasoning_content":"The user"`) ||
		!strings.Contains(body, `"name":"grep"`) ||
		!strings.Contains(body, `"arguments":"{\"pat`) || !strings.Contains(body, `"arguments":"tern\"}"`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

// Lines split across reads come out whole; one without an empty
// reasoning_content comes out byte for byte.
func TestChatTidySplitLines(t *testing.T) {
	in := "data: {\"choices\":[{\"delta\":{\"content\":\"a\",\"reasoning_content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}],  \"x\":1}\r\n\r\ndata: [DONE]"
	var tidy chatTidy
	var out []byte
	for i := 0; i < len(in); i += 7 {
		out = append(out, tidy.write([]byte(in[i:min(i+7, len(in))]))...)
	}
	out = append(out, tidy.flush()...)
	want := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}],  \"x\":1}\r\n\r\ndata: [DONE]"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

// The empty finish_reason of an unfinished chunk becomes null, on every
// choice; a real one and an already null one pass as they came.
func TestChatTidyFinishReason(t *testing.T) {
	var tidy chatTidy
	in := `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":""},{"index":1,"delta":{"content":"b"},"finish_reason":""}]}` + "\n"
	out := string(tidy.write([]byte(in)))
	if strings.Contains(out, `"finish_reason":""`) || strings.Count(out, `"finish_reason":null`) != 2 {
		t.Fatalf("got %q", out)
	}
	same := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n"
	if got := string(tidy.write([]byte(same))) + string(tidy.flush()); got != same {
		t.Fatalf("got %q", got)
	}
}

// A tool-call fragment's repeated empty name is dropped while the first
// fragment's real name stands, and a last-wins merge of the mended
// fragments assembles the call as it was meant.
func TestChatTidyToolCallNames(t *testing.T) {
	fragments := []string{
		`{"id":"call_1","type":"function","function":{"name":"grep","arguments":""},"index":0}`,
		`{"function":{"name":"","arguments":"{\"pat"},"index":0}`,
		`{"function":{"name":"","arguments":"tern\"}"},"index":0}`,
	}
	var tidy chatTidy
	name, args := "", ""
	for _, frag := range fragments {
		line := `data: {"choices":[{"index":0,"delta":{"tool_calls":[` + frag + `]}}]}` + "\n"
		out := string(tidy.write([]byte(line)))
		if strings.Contains(out, `"name":""`) {
			t.Fatalf("empty name left in %q", out)
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Name      *string `json:"name"`
							Arguments string  `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(out), "data:")), &chunk); err != nil {
			t.Fatalf("unreadable %q: %v", out, err)
		}
		fn := chunk.Choices[0].Delta.ToolCalls[0].Function
		if fn.Name != nil { // a last-wins client overwrites with any name present
			name = *fn.Name
		}
		args += fn.Arguments
	}
	if name != "grep" || args != `{"pattern"}` {
		t.Fatalf("merged %q %q", name, args)
	}

	// every empty name of a multi-call, multi-choice chunk goes
	multi := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"a"}},{"index":1,"function":{"name":"","arguments":"b"}}]}},{"index":1,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"c"}}]}}]}` + "\n"
	out := string(tidy.write([]byte(multi)))
	if strings.Contains(out, `"name":""`) || !strings.Contains(out, `"arguments":"a"`) ||
		!strings.Contains(out, `"arguments":"b"`) || !strings.Contains(out, `"arguments":"c"`) {
		t.Fatalf("got %q", out)
	}
}
