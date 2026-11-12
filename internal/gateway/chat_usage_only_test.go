package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Copilot's gemini-4-carbon now and then answers a Chat request 200 with
// only a usage frame (about 855 bytes down, completion_tokens above 0, no
// content or tool delta and finish_reason null), which went to Codex as an
// empty response.completed: the turn stopped without a word (codealc on X).
// thoughtOnly never saw it, there being no reasoning in it. Such a reply is
// an empty one: asked again in place, and the error when it comes empty
// every time. A reply that said something with no finish_reason, and one
// that ended with a stop and no text, go as they did.
func TestChatUsageOnlyReplyIsEmpty(t *testing.T) {
	fresh(t)
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	var calls atomic.Int32
	var reply string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, reply)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "cp", Name: "Copilot", Key: "k", Models: []string{"gem"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	role := `data: {"id":"c1","object":"chat.completion.chunk","model":"gem","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`
	usage := `data: {"id":"c1","object":"chat.completion.chunk","model":"gem","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":24,"total_tokens":924}}`
	ask := func() (int, string) {
		calls.Store(0)
		return post(t, "/v1/responses", `{"model":"cp/gem","stream":true,"input":"hi"}`)
	}

	reply = sse(role, usage, `data: [DONE]`)
	code, body := ask()
	if code != 200 || !strings.Contains(body, "an empty reply") || strings.Contains(body, "response.completed") || calls.Load() < 2 {
		t.Errorf("a usage-only reply answered %d after %d calls, not the empty-reply error: %s", code, calls.Load(), body)
	}

	text := `data: {"id":"c1","object":"chat.completion.chunk","model":"gem","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`
	reply = sse(role, text, usage, `data: [DONE]`)
	if code, body = ask(); code != 200 || !strings.Contains(body, "hello") || !strings.Contains(body, "response.completed") || calls.Load() != 1 {
		t.Errorf("a reply with text and no finish_reason: %d after %d calls: %s", code, calls.Load(), body)
	}

	stopped := `data: {"id":"c1","object":"chat.completion.chunk","model":"gem","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":900,"completion_tokens":2}}`
	reply = sse(role, stopped, `data: [DONE]`)
	if code, body = ask(); code != 200 || !strings.Contains(body, "response.completed") || strings.Contains(body, "an empty reply") || calls.Load() != 1 {
		t.Errorf("a reply that ended with a stop and no text: %d after %d calls: %s", code, calls.Load(), body)
	}
}
