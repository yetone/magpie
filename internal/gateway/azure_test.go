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

// azureUp plays an Azure OpenAI resource's v1 API: chat completions and
// Responses under /openai/v1, a key in api-key and nothing else — a
// Bearer is an Entra ID token there, and a key sent as one is refused.
type azureUp struct {
	mu    sync.Mutex
	calls []bedrockCall
}

func (a *azureUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(body, &m)
	model, _ := m["model"].(string)
	a.mu.Lock()
	a.calls = append(a.calls, bedrockCall{r.URL.RequestURI(), r.Header.Clone(), model, m})
	a.mu.Unlock()
	if r.Header.Get("api-key") != "azure-key" || r.Header.Get("Authorization") != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"401","message":"Access denied due to invalid subscription key or wrong API endpoint."}}`)
		return
	}
	switch r.URL.Path {
	case "/openai/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from azure chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
	case "/openai/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"from azure responses"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"from azure responses"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}`))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"404","message":"Resource not found"}}`)
	}
}

func (a *azureUp) last() bedrockCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 {
		return bedrockCall{}
	}
	return a.calls[len(a.calls)-1]
}

// The Azure OpenAI preset at a fake resource: Codex's Responses, a chat
// client's completions and Claude Code's messages all reach the resource's
// v1 API with the key in api-key and no Authorization, the model in the
// body the deployment's name as the agent picked it, and a reply length
// asked as max_completion_tokens, which its reasoning deployments want.
// A deployment named for a GPT model goes to Responses, one named any
// other way to chat completions.
func TestAzureRoutes(t *testing.T) {
	fresh(t)
	up := &azureUp{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset(provider.AzurePreset)
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "azure-key"
	// A local fake isn't a resource's host, so it is kept as given; the
	// endpoint field sets both, as here.
	p.Chat = srv.URL + "/openai/v1"
	p.Responses = p.Chat
	p.Models = []string{"gpt-5-codex", "team-chat"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("azure")
	if err != nil || saved.Responses != saved.Chat || !saved.IsAzure() {
		t.Fatalf("saved: %v %+v", err, saved)
	}

	for _, tc := range []struct{ name, path, body, reply, upstream, model string }{
		{"codex", "/v1/responses",
			`{"model":"azure/gpt-5-codex","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			"from azure responses", "/openai/v1/responses", "gpt-5-codex"},
		{"claude code on a gpt deployment", "/v1/messages",
			`{"model":"azure/gpt-5-codex","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"from azure responses", "/openai/v1/responses", "gpt-5-codex"},
		{"chat client", "/v1/chat/completions",
			`{"model":"azure/team-chat","max_tokens":20,"stream":true,"thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`,
			"from azure chat", "/openai/v1/chat/completions", "team-chat"},
		{"claude code on another deployment", "/v1/messages",
			`{"model":"azure/team-chat","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"from azure chat", "/openai/v1/chat/completions", "team-chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := post(t, tc.path, tc.body)
			if code != 200 || !strings.Contains(body, tc.reply) {
				t.Fatalf("status %d: %s", code, body)
			}
			c := up.last()
			if c.path != tc.upstream || c.model != tc.model {
				t.Fatalf("upstream: %s %q", c.path, c.model)
			}
			if c.head.Get("api-key") != "azure-key" || c.head.Get("Authorization") != "" || c.head.Get("x-api-key") != "" {
				t.Fatalf("auth headers: %v", c.head)
			}
			if strings.HasSuffix(c.path, "/chat/completions") {
				if _, ok := c.body["max_tokens"]; ok || c.body["max_completion_tokens"] != float64(20) {
					t.Fatalf("length asked as: %v", c.body)
				}
				if _, ok := c.body["thinking"]; ok {
					t.Fatalf("thinking sent to Azure: %v", c.body)
				}
			}
		})
	}
}
