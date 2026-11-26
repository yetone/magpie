package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model a plugin doesn't list (a test model the user typed in), its API
// set to Anthropic in magpie, is sent where the plugin's own Claude models
// go (Anotiaww on X: WorkBuddy's plugin declares its chat at
// <endpoint>/v2 and its Claude models on Messages at <endpoint>/v1, and
// such a model went to /v2/messages, which WorkBuddy answers "404 Route
// Not Found"). The fake plugin is declared as WorkBuddy's is
// ($FAKE_SPLIT), and its upstream answers that 404 on any other path.
// Claude Code's turn and a chat client's, translated to Messages, both
// reach /v1/messages; the same model with no API set, asked on chat,
// stays on the provider's /v2; the plugin's own Claude model goes where it
// did. A model on the list whose route is chat, its API set to Anthropic,
// goes to /v1/messages too, asked directly or through a group (the
// reporter's group/claude-opus-5.5 to blind-model-f).
func TestPluginUnlistedModelOnMessagesTakesClaudeRoute(t *testing.T) {
	t.Setenv("FAKE_SPLIT", "1")
	var mu sync.Mutex
	var paths []string
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		// as WorkBuddy answers a path it has no route for: /v2/messages
		// (and /v1/chat/completions) are "404 Route Not Found"
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") && !strings.HasSuffix(r.URL.Path, "/v2/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			io.WriteString(w, `{"error_msg":"404 Route Not Found"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "event: "+strings.SplitN(strings.TrimPrefix(e, `{"type":"`), `"`, 2)[0]+"\ndata: "+e+"\n\n")
		}
	}))
	if err := provider.SetModels(pid, []string{"fake-1", "fake-claude", "wb-test-1", "wb-test-2"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelAPI(pid+"/wb-test-1", "anthropic"); err != nil {
		t.Fatal(err)
	}
	// a model on the plugin's own list whose route is chat (fake-1, as the
	// reporter's blind-model-f), its API set to Anthropic and asked by
	// Claude Code through a group named for a Claude model: e00986a85
	// sent it to /v2/messages still
	if err := provider.SetModelAPI(pid+"/fake-1", "anthropic"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "claude-opus-5.5", Name: "claude-opus-5.5", Members: []string{pid + "/fake-1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	ask := func(name, path, body string) string {
		t.Helper()
		mu.Lock()
		paths = nil
		mu.Unlock()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer magpie")
		req.Header.Set("anthropic-version", "2023-06-01")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(paths, ",")
	}
	for _, c := range []struct{ name, path, body, want string }{
		{"claude code, api set", "/v1/messages", `{"model":"` + pid + `/wb-test-1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"chat client, api set", "/v1/chat/completions", `{"model":"` + pid + `/wb-test-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"chat client, no api set", "/v1/chat/completions", `{"model":"` + pid + `/wb-test-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v2/chat/completions"},
		{"listed claude", "/v1/messages", `{"model":"` + pid + `/fake-claude","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"listed chat model, api set, claude code", "/v1/messages", `{"model":"` + pid + `/fake-1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"listed chat model, api set, through a group", "/v1/messages", `{"model":"group/claude-opus-5.5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"listed chat model, api set, chat client", "/v1/chat/completions", `{"model":"` + pid + `/fake-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
	} {
		if got := ask(c.name, c.path, c.body); got != c.want {
			t.Errorf("%s: the vendor was asked at %q, want %q", c.name, got, c.want)
		}
	}
}
