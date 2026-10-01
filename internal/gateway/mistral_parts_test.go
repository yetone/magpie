package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// mistralThinkingStream is GLM on api.mistral.ai as it streams (#483):
// after the role chunk, content is typed parts, the thinking a list of
// text parts, and the last of the thinking and the answer in one delta.
func mistralThinkingStream() string {
	chunk := func(delta, finish string) string {
		return `data: {"id":"31c3","object":"chat.completion.chunk","created":1790887322,"model":"zai-glm-5-3","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `,"logprobs":null}]}`
	}
	return sse(
		chunk(`{"role":"assistant","content":""}`, `null`),
		chunk(`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"The user has"}],"closed":true}]}`, `null`),
		chunk(`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":" asked for ok."}],"closed":true},{"type":"text","text":"o"}]}`, `null`),
		chunk(`{"index":0,"content":[{"type":"text","text":"k"}]}`, `null`),
		chunk(`{"content":""}`, `"stop"`),
		`data: [DONE]`)
}

// A Chat client gets Mistral's thinking in reasoning_content and its text
// as a string content, on every chunk: OpenCode's AI SDK turned the first
// array away ("Invalid … stream event") and the request was given up.
func TestMistralPartsStreamFlattened(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", mistralThinkingStream()}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"Reply with just: ok"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, body)
	}
	var think, text string
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta map[string]json.RawMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		for _, c := range ch.Choices {
			if raw, ok := c.Delta["content"]; ok {
				var s *string
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Fatalf("content is not a string: %s", data)
				}
				if s != nil {
					text += *s
				}
			}
			var r string
			json.Unmarshal(c.Delta["reasoning_content"], &r)
			think += r
		}
	}
	if think != "The user has asked for ok." || text != "ok" {
		t.Fatalf("think %q text %q\n%s", think, text, body)
	}
	if !strings.Contains(body, `"reasoning_content":" asked for ok."`) || !strings.Contains(body, `"content":"o"`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatal(body)
	}
}

// A reply that isn't streamed has the same shape in message.content.
func TestMistralPartsWholeFlattened(t *testing.T) {
	fresh(t)
	up := `{"id":"31c3","object":"chat.completion","model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"The user has asked for ok."}],"closed":true},{"type":"text","text":"ok"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":7,"total_tokens":16}}`
	a := &scripted{replies: []reply{{200, "application/json", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"Reply with just: ok"}]}`)))
	var got struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Choices) != 1 ||
		got.Choices[0].Message.Content != "ok" || got.Choices[0].Message.ReasoningContent != "The user has asked for ok." ||
		got.Choices[0].FinishReason != "stop" || got.Usage.TotalTokens != 16 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Replies of other upstreams pass byte for byte, streamed or not.
func TestChatRelayWithoutPartsUntouched(t *testing.T) {
	line := `data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n"
	if got := string(tidyLine([]byte(line))); got != line {
		t.Fatalf("%s", got)
	}
	up := `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"} ,"finish_reason":"stop"}]}`
	fresh(t)
	a := &scripted{replies: []reply{{200, "application/json", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 || rec.Body.String() != up {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Spoken to Claude Code's API or to Codex's, the same stream is thinking
// and then the text: a chunk of parts was not read at all, so both were
// lost.
func TestMistralPartsTranslated(t *testing.T) {
	for _, c := range []struct{ path, body, think string }{
		{"/v1/messages", `{"model":"a/m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			`"type":"thinking_delta"`},
		{"/v1/responses", `{"model":"a/m","stream":true,"input":"hi"}`,
			`"type":"reasoning"`},
	} {
		fresh(t)
		a := &scripted{replies: []reply{{200, "text/event-stream", mistralThinkingStream()}}}
		scriptedOn(t, "a", provider.Chat, a)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, c.think) || !strings.Contains(body, "The user has") ||
			!strings.Contains(body, " asked for ok.") || !strings.Contains(body, `"o"`) || !strings.Contains(body, `"k"`) {
			t.Fatalf("%s: %d %s", c.path, rec.Code, body)
		}
		if i, j := strings.Index(body, "The user has"), strings.Index(body, `"o"`); i > j {
			t.Fatalf("%s: text before thinking: %s", c.path, body)
		}
	}
}
