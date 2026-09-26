package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A vendor that serves each model on endpoints of its own, and says which
// in its model list (Command Code): each request goes where its model is
// served, translated if it must be. Issue #93
func TestLiveListsEndpointsRoute(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		var body struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		switch r.URL.Path {
		case "/provider/v1/models":
			io.WriteString(w, `{"data":[{"id":"claude-sonnet-5","supported_endpoints":["/messages"]},{"id":"deepseek/deepseek-v4-flash","supported_endpoints":["/chat/completions","/responses"]}]}`)
		case "/provider/v1/chat/completions":
			if strings.HasPrefix(body.Model, "claude") {
				http.Error(w, `{"error":{"message":"must be called via /provider/v1/messages"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
				`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
				`data: [DONE]`))
		case "/provider/v1/messages":
			if !strings.HasPrefix(body.Model, "claude") {
				http.Error(w, `{"error":{"message":"is not supported on this endpoint"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
				`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from messages"}}`,
				`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
				`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`event: message_stop
data: {"type":"message_stop"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "cc", Name: "CC", Key: "k", Chat: up.URL + "/provider/v1", Anthropic: up.URL + "/provider"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("cc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if apis := p.APIs("deepseek/deepseek-v4-flash"); len(apis) != 2 || apis[0] != provider.Chat || apis[1] != provider.Responses {
		t.Fatalf("apis = %v", apis)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	for _, c := range []struct{ path, body, want string }{
		{"/v1/messages", `{"model":"cc/deepseek/deepseek-v4-flash","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "from chat"},
		{"/v1/chat/completions", `{"model":"cc/claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`, "from messages"},
	} {
		mu.Lock()
		hits = nil
		mu.Unlock()
		res, err := http.Post(gw.URL+c.path, "application/json", strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), c.want) {
			t.Fatalf("%s: %d %s (upstream %v)", c.path, res.StatusCode, b, hits)
		}
		if len(hits) != 1 {
			t.Fatalf("%s: tried %v", c.path, hits)
		}
	}
}
