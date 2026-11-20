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

// sub2apiNoDispatch is sub2api's 403 to /v1/messages for a key whose group
// isn't let dispatch Messages, as its handler writes it
// (openai_gateway_handler.go, anthropicErrorResponse): every Claude Code
// request of a Sidrune AI China-region key (Axo, 11-20).
const sub2apiNoDispatch = `{"type":"error","error":{"type":"permission_error","message":"This group does not allow /v1/messages dispatch"}}`

// A key whose group refuses Messages is asked its models on Chat, and
// after one refusal no model of it is asked on Messages again: the refusal
// is of the API, not of the model asked.
func TestGroupWithoutMessagesGoesToChat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	calls := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, sub2apiNoDispatch)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","model":"m","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
				`data: [DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"claude-sonnet-4-5", "claude-opus-4-1"},
		Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	handler := New().Handler()
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-sonnet-4-5"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"relay/`+model+`","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "chat ok") {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["/v1/messages"] != 1 || calls["/v1/chat/completions"] != 3 {
		t.Fatalf("Messages asked %d times, want once; chat %d, want 3 (%v)", calls["/v1/messages"], calls["/v1/chat/completions"], calls)
	}
}

// sub2api's 404 for a model no account of the key's group serves
// (no_account_error.go; Sidrune AI's gpt-5.6, Axo 11-20) rests that model
// on the key alone, the key asked again for others.
func TestSub2apiNoAccountIsTheModels(t *testing.T) {
	body := []byte(`{"error":{"type":"model_not_found","message":"Model \"gpt-5.6\" is not supported by any configured account in this group"}}`)
	if !modelRefused(http.StatusNotFound, body) {
		t.Error("not read as the model refused")
	}
}
