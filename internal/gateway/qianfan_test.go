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

// qianfan plays the plan's own endpoints: chat completions and Responses
// under /v2/tokenplan/personal, Anthropic messages under
// /anthropic/tokenplan/personal.
type qianfan struct {
	mu    sync.Mutex
	calls []qianfanCall
}

type qianfanCall struct {
	path, model string
	head        http.Header
}

func (q *qianfan) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	q.mu.Lock()
	q.calls = append(q.calls, qianfanCall{r.URL.Path, v.Model, r.Header.Clone()})
	q.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v2/tokenplan/personal/chat/completions":
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
	case "/v2/tokenplan/personal/responses":
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"from responses"}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":5,"output_tokens":2}}}`))
	case "/anthropic/tokenplan/personal/v1/messages":
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
		io.WriteString(w, `{"message":"no such route"}`)
	}
}

func (q *qianfan) last() qianfanCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.calls) == 0 {
		return qianfanCall{}
	}
	return q.calls[len(q.calls)-1]
}

// The plan at a fake Qianfan: every model is relayed on the client's own
// API, and a request for an API the plan speaks is never translated away
// from it.
func TestQianfanTokenPlanRoutes(t *testing.T) {
	fresh(t)
	up := &qianfan{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("qianfan-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "bce-v3-test"
	p.Chat, p.Responses = srv.URL+"/v2/tokenplan/personal", srv.URL+"/v2/tokenplan/personal"
	p.Anthropic = srv.URL + "/anthropic/tokenplan/personal"
	p.Models = []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, body, path2, model, reply string
		bearer                                bool
	}{
		{"flash on responses", "/v1/responses",
			`{"model":"qianfan-token-plan/deepseek-v4.1-flash","input":"hi","stream":true}`,
			"/v2/tokenplan/personal/responses", "deepseek-v4.1-flash", "from responses", true},
		{"pro on responses", "/v1/responses",
			`{"model":"qianfan-token-plan/deepseek-v4-pro","input":"hi","stream":true}`,
			"/v2/tokenplan/personal/responses", "deepseek-v4-pro", "from responses", true},
		{"flash on messages", "/v1/messages",
			`{"model":"qianfan-token-plan/deepseek-v4.1-flash","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/anthropic/tokenplan/personal/v1/messages", "deepseek-v4.1-flash", "from messages", false},
		{"pro on chat", "/v1/chat/completions",
			`{"model":"qianfan-token-plan/deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/v2/tokenplan/personal/chat/completions", "deepseek-v4-pro", "from chat", true},
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
				if c.head.Get("Authorization") != "Bearer bce-v3-test" {
					t.Fatalf("headers: %v", c.head)
				}
			} else if c.head.Get("x-api-key") != "bce-v3-test" || c.head.Get("anthropic-version") != "2023-06-01" {
				t.Fatalf("headers: %v", c.head)
			}
		})
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, c := range up.calls {
		if !strings.HasPrefix(c.path, "/v2/tokenplan/personal/") && !strings.HasPrefix(c.path, "/anthropic/tokenplan/personal/") {
			t.Errorf("asked at %s", c.path)
		}
	}
}
