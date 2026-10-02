package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tokenhub plays TokenHub's pay-as-you-go endpoints: chat completions,
// Responses and Anthropic messages, all three under /v1 at the host's root.
type tokenhub struct {
	mu    sync.Mutex
	calls []tokenhubCall
}

type tokenhubCall struct {
	path, model string
	head        http.Header
}

func (q *tokenhub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	q.mu.Lock()
	q.calls = append(q.calls, tokenhubCall{r.URL.Path, v.Model, r.Header.Clone()})
	q.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v1/chat/completions":
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
	case "/v1/responses":
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"from responses"}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":5,"output_tokens":2}}}`))
	case "/v1/messages":
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"`+v.Model+`","usage":{"input_tokens":5}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from messages"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`data: {"type":"message_stop"}`))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"404000","message":"no such route"}}`)
	}
}

func (q *tokenhub) last() tokenhubCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.calls) == 0 {
		return tokenhubCall{}
	}
	return q.calls[len(q.calls)-1]
}

// TokenHub pay as you go (Jorben on Discord) at a fake TokenHub, China's
// preset and the global one alike: each client's API reaches TokenHub's own
// path for it, never translated away, with the key as a Bearer on the
// OpenAI APIs and as x-api-key on Anthropic's, which TokenHub's docs ask.
func TestTencentTokenHubRoutes(t *testing.T) {
	for _, id := range []string{"tencent-tokenhub-cn", "tencent-tokenhub"} {
		t.Run(id, func(t *testing.T) {
			fresh(t)
			up := &tokenhub{}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			p, err := provider.FromPreset(id)
			if err != nil {
				t.Fatal(err)
			}
			// the preset's paths kept, its host swapped for the fake's
			host := strings.TrimSuffix(p.Chat, "/v1")
			if p.Responses != host+"/v1" || p.Anthropic != host {
				t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
			}
			p.Chat = strings.Replace(p.Chat, host, srv.URL, 1)
			p.Responses = strings.Replace(p.Responses, host, srv.URL, 1)
			p.Anthropic = strings.Replace(p.Anthropic, host, srv.URL, 1)
			p.Key = "sk-tokenhub-test"
			p.Models = []string{"hy3", "deepseek-v4-pro"}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}

			for _, tc := range []struct {
				name, path, body, path2, model, reply string
				bearer                                bool
			}{
				{"chat", "/v1/chat/completions",
					`{"model":"` + id + `/hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
					"/v1/chat/completions", "hy3", "from chat", true},
				{"responses", "/v1/responses",
					`{"model":"` + id + `/deepseek-v4-pro","input":"hi","stream":true}`,
					"/v1/responses", "deepseek-v4-pro", "from responses", true},
				{"messages", "/v1/messages",
					`{"model":"` + id + `/hy3","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
					"/v1/messages", "hy3", "from messages", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					code, body := post(t, tc.path, tc.body)
					if code != 200 || !strings.Contains(body, tc.reply) {
						t.Fatalf("status %d: %s", code, body)
					}
					c := up.last()
					if c.path != tc.path2 || c.model != tc.model {
						t.Fatalf("upstream: %s %q", c.path, c.model)
					}
					if tc.bearer {
						if c.head.Get("Authorization") != "Bearer sk-tokenhub-test" {
							t.Fatalf("headers: %v", c.head)
						}
					} else if c.head.Get("x-api-key") != "sk-tokenhub-test" || c.head.Get("anthropic-version") == "" {
						t.Fatalf("headers: %v", c.head)
					}
				})
			}
			up.mu.Lock()
			defer up.mu.Unlock()
			for _, c := range up.calls {
				if c.path != "/v1/chat/completions" && c.path != "/v1/responses" && c.path != "/v1/messages" {
					t.Errorf("asked at %s", c.path)
				}
			}
		})
	}
}
