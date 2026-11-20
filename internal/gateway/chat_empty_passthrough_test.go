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

// Copilot's gemini-4-hydrogen answered dsh's Chat request 200 with its role
// chunk, a usage frame and [DONE]: no text, no reasoning, no tool call and
// no finish_reason. The Responses translation calls that an empty reply
// (TestChatUsageOnlyReplyIsEmpty), but relayed as it came to a Chat
// client it went through as a clean end, and dsh said "Stream ended
// without finish_reason" (@yuushabu_buyinn on X). Relayed, it is an empty
// reply too: asked again in place, the error when it comes empty every
// time, [DONE] or the body just ending. A Gemini model on a local
// OpenAI-compatible server is no exception: its missing finish_reason
// used to be filled in as "stop" for the usage it counted. A reply with
// text, and an empty one that ended with a stop, go as they did.
func TestChatPassthroughVoidStreamIsEmpty(t *testing.T) {
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	for _, model := range []string{"gem", "gemini-4-hydrogen"} {
		t.Run(model, func(t *testing.T) {
			fresh(t)
			var calls atomic.Int32
			var replies []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				n := int(calls.Add(1))
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, replies[min(n, len(replies))-1])
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "cp", Name: "Copilot", Key: "k", Models: []string{model}, Chat: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			role := `data: {"choices":[{"index":0,"delta":{"content":"","role":"assistant"}}],"created":1763600000,"id":"c1","model":"` + model + `"}`
			usage := `data: {"choices":[],"created":1763600000,"id":"c1","usage":{"completion_tokens":0,"prompt_tokens":18342,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":18342},"model":"` + model + `"}`
			text := `data: {"choices":[{"index":0,"delta":{"content":"hello"}}],"created":1763600000,"id":"c1","model":"` + model + `"}`
			stopped := `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"created":1763600000,"id":"c1","model":"` + model + `"}`
			ask := func(r ...string) (int, string) {
				calls.Store(0)
				replies = r
				return post(t, "/v1/chat/completions", `{"model":"cp/`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			}

			for name, void := range map[string]string{
				"[DONE]": sse(role, usage, `data: [DONE]`),
				"EOF":    sse(role, usage),
			} {
				code, body := ask(void)
				if code != 200 || !strings.Contains(body, "an empty reply") || !strings.HasSuffix(body, "data: [DONE]\n\n") ||
					strings.Count(body, "[DONE]") != 1 || calls.Load() < 2 {
					t.Errorf("a void stream ending in %s answered %d after %d calls, not the empty-reply error: %q", name, code, calls.Load(), body)
				}
				// asked again, the second answer is the client's alone
				code, body = ask(void, sse(role, text, stopped, usage, `data: [DONE]`))
				if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "an empty reply") || calls.Load() != 2 {
					t.Errorf("a void stream ending in %s, then a reply: %d after %d calls: %q", name, code, calls.Load(), body)
				}
			}

			// translated, asked as a stream or whole, it was empty already
			// for "gem"; a Gemini model's lost finish_reason is no longer
			// filled in as "stop" there either
			for _, x := range []struct{ path, body string }{
				{"/v1/messages", `{"model":"cp/` + model + `","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
				{"/v1/messages", `{"model":"cp/` + model + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
				{"/v1/responses", `{"model":"cp/` + model + `","stream":true,"input":"hi"}`},
			} {
				calls.Store(0)
				replies = []string{sse(role, usage)}
				if code, body := post(t, x.path, x.body); !strings.Contains(body, "an empty reply") || calls.Load() < 2 {
					t.Errorf("%s %s: a void stream answered %d after %d calls, not the empty-reply error: %q", x.path, x.body, code, calls.Load(), body)
				}
			}

			if code, body := ask(sse(role, text, usage, `data: [DONE]`)); code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "an empty reply") || calls.Load() != 1 {
				t.Errorf("a reply with text and no finish_reason: %d after %d calls: %q", code, calls.Load(), body)
			}
			if code, body := ask(sse(role, stopped, usage, `data: [DONE]`)); code != 200 || !strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, "an empty reply") || calls.Load() != 1 {
				t.Errorf("an empty reply that ended with a stop: %d after %d calls: %q", code, calls.Load(), body)
			}
		})
	}
}

// In a group, the void stream is held and fails over to the next member,
// with nothing of it reaching the client and nobody rested.
func TestChatPassthroughVoidStreamFailsOver(t *testing.T) {
	void := sse(`data: {"choices":[{"index":0,"delta":{"content":"","role":"assistant"}}],"created":1763600000,"id":"cvoid","model":"gemini-4-hydrogen"}`,
		`data: {"choices":[],"created":1763600000,"id":"cvoid","usage":{"completion_tokens":0,"prompt_tokens":18342,"total_tokens":18342},"model":"gemini-4-hydrogen"}`,
		`data: [DONE]`)
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", void}, {200, "text/event-stream", void}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
	c := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
	threeGroup(t, a, b, c)
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "cvoid") || strings.Contains(body, emptyReply) {
		t.Fatalf("%d %q", code, body)
	}
	if b.n != 1 || c.n != 0 {
		t.Fatalf("a asked %d, b asked %d, c asked %d", a.n, b.n, c.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if f := r.Tries[0]; f.Fail != failOther || f.Rest != nil || !strings.Contains(f.Error, emptyReply) {
		t.Fatalf("tries: %+v", r.Tries)
	}
	for _, x := range r.Tries {
		if x.Rest != nil {
			t.Fatalf("rested: %+v", r.Tries)
		}
	}
}
