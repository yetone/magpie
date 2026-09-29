package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A backend that only streams (WorkBuddy: "Non-stream chat request is
// currently not supported") is streamed a client's non-streaming request,
// and the client given one answer. Issue #124
func TestStreamOnlyGetsNonStreamTranslated(t *testing.T) {
	var streamed []bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := strings.Contains(string(b), `"stream":true`)
		streamed = append(streamed, s)
		if !s {
			http.Error(w, `{"code":400,"msg":"Non-stream chat request is currently not supported"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
			`data: [DONE]`))
	}))
	defer up.Close()
	p := provider.Provider{ID: "wb", Name: "WB", Chat: up.URL, Account: &provider.Account{Agent: "workbuddy", User: "u", Stream: true}}
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	var u Usage
	status, msg := s.translate(w, r, p, provider.Chat, provider.Chat, "auto", body, &u)
	if status != 200 || len(streamed) != 1 || !streamed[0] {
		t.Fatalf("status %d %q, streamed %v", status, msg, streamed)
	}
	if got := w.Body.String(); strings.Contains(got, "data:") || !strings.Contains(got, `"hello"`) || !strings.Contains(got, `"chat.completion"`) {
		t.Fatalf("client got %s", got)
	}
}
