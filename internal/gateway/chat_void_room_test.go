package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// JNDliuliuM on X (v0.1.4613): Hermes's subagents, on a Gemini model at a
// relay's Chat endpoint, failed their tasks after thirteen 502s, each "cut
// at its length, the whole output spent on reasoning" with out 8192.
// Hermes names no max_tokens for a custom provider (its chat_completions
// transport sends one only when the user set model.max_tokens), so the
// 8192 was the relay's own default, which a thinking Gemini spends on
// reasoning alone. A request that names no limit, to a model whose API's
// default cut a reply void, is asked again at once for the room the model
// has (voidRoom), and every later one asks for it from the start. A limit
// the client named goes as it is.

// voidRelay is a relay that cuts a reply naming no max_tokens, or one
// under 32768, at 8192 tokens of reasoning, as a relay's Gemini default
// did, and answers one with room. It records each request's max_tokens
// (0: none named).
type voidRelay struct {
	mu    sync.Mutex
	asked []int64
	srv   *httptest.Server
}

func newVoidRelay(t *testing.T, refuseOver int64) *voidRelay {
	v := &voidRelay{}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n := gjson.GetBytes(b, "max_tokens").Int()
		if n == 0 {
			n = gjson.GetBytes(b, "max_completion_tokens").Int()
		}
		v.mu.Lock()
		v.asked = append(v.asked, n)
		v.mu.Unlock()
		if refuseOver > 0 && n > refuseOver {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"max_tokens must be at most 16384","type":"invalid_request_error"}}`)
			return
		}
		void := n < 32768
		stream := gjson.GetBytes(b, "stream").Bool()
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			if void {
				io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":8192,"completion_tokens_details":{"reasoning_tokens":8192}}}`)
				return
			}
			io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"subagent done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":12000,"completion_tokens_details":{"reasoning_tokens":11990}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if void {
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":8192,"completion_tokens_details":{"reasoning_tokens":8192}}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"subagent done"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":12000,"completion_tokens_details":{"reasoning_tokens":11990}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *voidRelay) take() []int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	a := v.asked
	v.asked = nil
	return a
}

func TestVoidAtDefaultAsksForRoom(t *testing.T) {
	for _, a := range []struct{ name, path, body string }{
		// Hermes's subagent: chat_completions, streamed, no max_tokens
		{"relay streamed", "/v1/chat/completions", `{"model":"rl/gemini-4-hydrogen","stream":true,"messages":[{"role":"user","content":"do the task"}]}`},
		{"relay whole", "/v1/chat/completions", `{"model":"rl/gemini-4-hydrogen","messages":[{"role":"user","content":"do the task"}]}`},
		// Codex names no max_output_tokens
		{"responses", "/v1/responses", `{"model":"rl/gemini-4-hydrogen","stream":true,"input":"do the task"}`},
		{"responses whole", "/v1/responses", `{"model":"rl/gemini-4-hydrogen","input":"do the task"}`},
	} {
		t.Run(a.name, func(t *testing.T) {
			fresh(t)
			v := newVoidRelay(t, 0)
			if err := provider.Save(provider.Provider{ID: "rl", Name: "Relay", Key: "k", Models: []string{"gemini-4-hydrogen"}, Chat: v.srv.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			h := New()
			code, body := postTo(t, h, a.path, a.body)
			asked := v.take()
			if !strings.Contains(body, "subagent done") || strings.Contains(body, "an empty reply") {
				t.Errorf("first request: %d after asks %v, not answered once asked for room: %s", code, asked, body)
			}
			if len(asked) != 2 || asked[0] != 0 || asked[1] != voidRoomUnknown {
				t.Errorf("first request asked with %v, want [0 %d]: none named, then the room", asked, voidRoomUnknown)
			}
			// the next request asks for the room from the start
			code, body = postTo(t, h, a.path, a.body)
			asked = v.take()
			if !strings.Contains(body, "subagent done") || len(asked) != 1 || asked[0] != voidRoomUnknown {
				t.Errorf("next request: %d after asks %v, not asked for the room at once: %s", code, asked, body)
			}
		})
	}
}

// A limit the client named is its own: a void under it fails as before
// (#2972), asked once, and the room isn't asked for after it either.
func TestVoidUnderNamedLimitStays(t *testing.T) {
	fresh(t)
	v := newVoidRelay(t, 0)
	if err := provider.Save(provider.Provider{ID: "rl", Name: "Relay", Key: "k", Models: []string{"gemini-4-hydrogen"}, Chat: v.srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New()
	for _, a := range []struct{ name, path, body string }{
		{"relay", "/v1/chat/completions", `{"model":"rl/gemini-4-hydrogen","stream":true,"max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages", "/v1/messages", `{"model":"rl/gemini-4-hydrogen","stream":true,"max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"rl/gemini-4-hydrogen","stream":true,"max_output_tokens":8192,"input":"hi"}`},
	} {
		for range 2 {
			_, body := postTo(t, h, a.path, a.body)
			asked := v.take()
			if !strings.Contains(body, "cut at its length") || len(asked) != 1 || asked[0] != 8192 {
				t.Errorf("%s: a void under the client's 8192 asked %v: %s", a.name, asked, body)
			}
		}
	}
}

// An API that turns the room away as more than its model takes is asked
// naming none again, as the client did, and the room isn't asked for
// there again: no ask loops between the void and the refusal.
func TestVoidRoomRefusedIsForgotten(t *testing.T) {
	fresh(t)
	v := newVoidRelay(t, 16384)
	if err := provider.Save(provider.Provider{ID: "rl", Name: "Relay", Key: "k", Models: []string{"gemini-4-hydrogen"}, Chat: v.srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New()
	body := `{"model":"rl/gemini-4-hydrogen","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_, got := postTo(t, h, "/v1/chat/completions", body)
	asked := v.take()
	if !strings.Contains(got, "cut at its length") || len(asked) != 3 || asked[0] != 0 || asked[1] != voidRoomUnknown || asked[2] != 0 {
		t.Errorf("void, room refused: asked %v, want [0 %d 0] and the void's error: %s", asked, voidRoomUnknown, got)
	}
	_, got = postTo(t, h, "/v1/chat/completions", body)
	asked = v.take()
	if !strings.Contains(got, "cut at its length") || len(asked) != 1 || asked[0] != 0 {
		t.Errorf("after the refusal: asked %v, want [0]: %s", asked, got)
	}
}

// voidRoom asks for the model's own limit where known and more than was
// spent, the unknown model for voidRoomUnknown, and nothing once the
// model's own, or that, is what was spent.
func TestVoidRoom(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "rl", Name: "Relay", Key: "k", Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1"}
	if got := voidRoom(p, "m", 8192); got != voidRoomUnknown {
		t.Errorf("unknown model, 8192 spent: %d", got)
	}
	if got := voidRoom(p, "m", voidRoomUnknown); got != 0 {
		t.Errorf("unknown model, %d spent: %d", voidRoomUnknown, got)
	}
}
