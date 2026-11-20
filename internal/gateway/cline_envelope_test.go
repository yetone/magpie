package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Cline's API answers a request that isn't streamed with the completion in
// {"success":true,"data":…} (6094S on Discord: Alma's tool-model calls read
// no choices and failed). An agent gets the completion itself;
// {"success":false} is an error in the agent's API's shape; a stream and
// another provider's reply pass as they came. (A translated request is
// asked of Cline as a stream, which comes plain.)
func TestClineEnvelopeUnwrapped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cline, err := provider.FromPreset("clinepass")
	if err != nil {
		t.Fatal(err)
	}
	cline.ID, cline.Key, cline.Models = "cline", "clp_key", []string{"deepseek/deepseek-v4.1-flash"}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://other.test/v1", Models: []string{"deepseek/deepseek-v4.1-flash"}}
	for _, p := range []provider.Provider{cline, other} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	const completion = `{"id":"gen_1","object":"chat.completion","model":"deepseek/deepseek-v4.1-flash","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
	reply := `{"success":true,"data":` + completion + `}`
	ctype := "application/json; charset=utf-8"
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ctype}},
			Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}
	ask := func(path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	chat := func(id string) string {
		return `{"model":"` + id + `/deepseek/deepseek-v4.1-flash","stream":false,"messages":[{"role":"user","content":"Say pong"}]}`
	}

	code, body := ask("/v1/chat/completions", chat("cline"))
	var got struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(body), &got); code != 200 || err != nil || got.Success != nil || got.Object != "chat.completion" ||
		len(got.Choices) != 1 || got.Choices[0].Message.Content != "pong" {
		t.Fatalf("chat: %d %s", code, body)
	}

	// another provider's body is its own
	if code, body = ask("/v1/chat/completions", chat("other")); code != 200 || body != reply {
		t.Fatalf("other: %d %s", code, body)
	}

	// a refusal in the envelope
	reply = `{"success":false,"error":"Insufficient balance"}`
	code, body = ask("/v1/chat/completions", chat("cline"))
	if code != 502 || !strings.Contains(body, "Insufficient balance") || !strings.Contains(body, `"error"`) || strings.Contains(body, `"success"`) {
		t.Fatalf("refused: %d %s", code, body)
	}

	// a stream passes as it came (one that carried no reply at all would
	// be an empty one, chat_empty_passthrough_test.go)
	reply = "data: {\"success\":true,\"data\":{}}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	ctype = "text/event-stream"
	code, body = ask("/v1/chat/completions", `{"model":"cline/deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"Say pong"}]}`)
	if code != 200 || !strings.Contains(body, `"success":true`) {
		t.Fatalf("stream: %d %s", code, body)
	}
}
