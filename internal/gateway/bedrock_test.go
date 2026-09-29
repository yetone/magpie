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

// bedrock plays Bedrock's runtime: Anthropic's messages under /anthropic,
// chat completions under /openai/v1, and nothing else.
type bedrock struct {
	mu    sync.Mutex
	calls []bedrockCall
}

type bedrockCall struct {
	path  string
	head  http.Header
	model string
	body  map[string]any
}

func (b *bedrock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	var m map[string]any
	json.Unmarshal(body, &m)
	b.mu.Lock()
	b.calls = append(b.calls, bedrockCall{r.URL.Path, r.Header.Clone(), v.Model, m})
	b.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/anthropic/v1/messages":
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"`+v.Model+`","usage":{"input_tokens":5}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from claude"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`data: {"type":"message_stop"}`))
	case "/openai/v1/chat/completions":
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"no such route"}`)
	}
}

func (b *bedrock) last() bedrockCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return bedrockCall{}
	}
	return b.calls[len(b.calls)-1]
}

// The Bedrock preset at a fake runtime (#176): a Claude inference profile
// is asked on /anthropic/v1/messages with the key as x-api-key, whichever
// API the agent spoke, and any other model on /openai/v1/chat/completions
// with it as a Bearer.
func TestBedrockRoutes(t *testing.T) {
	fresh(t)
	up := &bedrock{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "ABSK-test"
	p.Anthropic, p.Chat = srv.URL+"/anthropic", srv.URL+"/openai/v1"
	p.Models = []string{"apac.anthropic.claude-opus-5-5", "openai.gpt-oss-120b-1:0"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, body, path2, model, reply string
		anthropic                             bool
	}{
		{"claude on messages", "/v1/messages",
			`{"model":"bedrock/apac.anthropic.claude-opus-5-5","max_tokens":20,"metadata":{"user_id":"{\"device_id\":\"d\"}"},"messages":[{"role":"user","content":"hi"}]}`,
			"/anthropic/v1/messages", "apac.anthropic.claude-opus-5-5", "from claude", true},
		{"claude on chat", "/v1/chat/completions",
			`{"model":"bedrock/apac.anthropic.claude-opus-5-5","max_tokens":20,"metadata":{"user_id":"{\"device_id\":\"d\"}"},"messages":[{"role":"user","content":"hi"}]}`,
			"/anthropic/v1/messages", "apac.anthropic.claude-opus-5-5", "from claude", true},
		{"gpt-oss on messages", "/v1/messages",
			`{"model":"bedrock/openai.gpt-oss-120b-1:0","max_tokens":20,"metadata":{"user_id":"{\"device_id\":\"d\"}"},"messages":[{"role":"user","content":"hi"}]}`,
			"/openai/v1/chat/completions", "openai.gpt-oss-120b-1:0", "from chat", false},
		{"gpt-oss on chat", "/v1/chat/completions",
			`{"model":"bedrock/openai.gpt-oss-120b-1:0","max_tokens":20,"metadata":{"user_id":"{\"device_id\":\"d\"}"},"messages":[{"role":"user","content":"hi"}]}`,
			"/openai/v1/chat/completions", "openai.gpt-oss-120b-1:0", "from chat", false},
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
			if tc.anthropic {
				if _, ok := c.body["metadata"]; ok {
					t.Fatalf("metadata sent: %v", c.body)
				}
				if c.head.Get("x-api-key") != "ABSK-test" || c.head.Get("Authorization") != "" || c.head.Get("anthropic-version") != "2023-06-01" {
					t.Fatalf("headers: %v", c.head)
				}
			} else if c.head.Get("Authorization") != "Bearer ABSK-test" {
				t.Fatalf("headers: %v", c.head)
			} else if _, ok := c.body["max_tokens"]; ok || c.body["max_completion_tokens"] != float64(20) {
				t.Fatalf("length asked as: %v", c.body)
			}
		})
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, c := range up.calls {
		if c.path != "/anthropic/v1/messages" && c.path != "/openai/v1/chat/completions" {
			t.Errorf("asked at %s", c.path)
		}
	}
}
