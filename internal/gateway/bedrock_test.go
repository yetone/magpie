package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
		if !sentByTest(c.head, "ABSK-test") {
			continue
		}
		if c.path != "/anthropic/v1/messages" && c.path != "/openai/v1/chat/completions" {
			t.Errorf("asked at %s", c.path)
		}
	}
}

// betaBedrock is a Bedrock runtime that, as the real one does, turns away
// the whole request for an anthropic-beta it doesn't know, naming them.
type betaBedrock struct {
	bedrock
	known []string
}

func (b *betaBedrock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var bad []string
	for _, v := range strings.Split(r.Header.Get("anthropic-beta"), ",") {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(b.known, v) {
			bad = append(bad, "`"+v+"`")
		}
	}
	if len(bad) > 0 {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		b.mu.Lock()
		b.calls = append(b.calls, bedrockCall{r.URL.Path, r.Header.Clone(), "", m})
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Unexpected value(s) `+strings.Join(bad, ", ")+" for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header.\"}}")
		return
	}
	b.bedrock.ServeHTTP(w, r)
}

// Claude Code's betas at Bedrock (#176: 400 Unexpected value(s)
// `advanced-tool-use-2025-11-20`, `prompt-caching-scope-2026-01-05`,
// `redact-thinking-2026-02-12` for the `anthropic-beta` header): tool
// search is asked by Bedrock's name for it, the ones Bedrock refuses are
// left out, and one it refuses that magpie didn't know of is dropped on a
// second try and not sent again.
func TestBedrockBetas(t *testing.T) {
	fresh(t)
	up := &betaBedrock{known: []string{"claude-code-20250219", "interleaved-thinking-2025-05-14",
		"context-management-2025-06-27", "tool-search-tool-2025-10-19"}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "ABSK-test"
	p.Anthropic, p.Chat = srv.URL+"/anthropic", srv.URL+"/openai/v1"
	p.Models = []string{"global.anthropic.claude-opus-5-5"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("User-Agent", "claude-cli/2.1.90 (external, cli)")
		req.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,"+
			"advanced-tool-use-2025-11-20,prompt-caching-scope-2026-01-05,redact-thinking-2026-02-12,brand-new-2026-09-01")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	calls := func() int {
		up.mu.Lock()
		defer up.mu.Unlock()
		return len(up.calls)
	}
	msg := `{"model":"bedrock/global.anthropic.claude-opus-5-5","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	want := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,tool-search-tool-2025-10-19"

	code, body := ask(msg)
	if code != 200 || !strings.Contains(body, "from claude") {
		t.Fatalf("status %d: %s", code, body)
	}
	if n := calls(); n != 2 || up.last().head.Get("anthropic-beta") != want {
		t.Fatalf("%d calls, betas %q", n, up.last().head.Get("anthropic-beta"))
	}

	// the one it refused isn't asked again
	code, body = ask(msg)
	if code != 200 || !strings.Contains(body, "from claude") {
		t.Fatalf("again: status %d: %s", code, body)
	}
	if n := calls(); n != 3 || up.last().head.Get("anthropic-beta") != want {
		t.Fatalf("again: %d calls, betas %q", n, up.last().head.Get("anthropic-beta"))
	}

	// betas in the body are fitted alike
	code, body = ask(`{"model":"bedrock/global.anthropic.claude-opus-5-5","max_tokens":20,"stream":true,` +
		`"anthropic_beta":["redact-thinking-2026-02-12","brand-new-2026-09-01","context-1m-2025-08-07"],"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("body betas: status %d: %s", code, body)
	}
	if got := up.last().body["anthropic_beta"]; !reflect.DeepEqual(got, []any{"context-1m-2025-08-07"}) {
		t.Fatalf("body betas sent: %v", got)
	}
}

// gptBedrock is Bedrock's runtime for OpenAI's GPT models, as #176 found it:
// Responses at /openai/v1/responses (when responses is set), and chat
// completions that turn function tools with reasoning away unless the
// effort is "none".
type gptBedrock struct {
	bedrock
	responses bool
}

const toolsEffortRefusal = `{"error":{"code":null,"message":"Amazon Bedrock: Function tools with reasoning_effort are not supported for global.openai.gpt-6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","param":null,"type":"invalid_request_error"}}`

func (b *gptBedrock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(body, &m)
	model, _ := m["model"].(string)
	b.mu.Lock()
	b.calls = append(b.calls, bedrockCall{r.URL.Path, r.Header.Clone(), model, m})
	b.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		if _, tools := m["tools"]; tools && m["reasoning_effort"] != "none" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, toolsEffortRefusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
	case r.URL.Path == "/openai/v1/responses" && b.responses:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"from responses"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"from responses"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}`))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"no such route"}`)
	}
}

const (
	codexToolsBody = `{"model":"%s","stream":true,"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}],"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	chatToolsBody  = `{"model":"%s","stream":true,"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object","properties":{}}}}],"messages":[{"role":"user","content":"hi"}]}`
)

// Codex at Bedrock's GPT models (#176: 400 Function tools with
// reasoning_effort are not supported for global.openai.gpt-6-luna in
// /v1/chat/completions): they are asked on the runtime's Responses API,
// with the key as a Bearer, a provider saved before the preset had that
// API too, and a chat client's tools go there as well; gpt-oss, which the
// runtime serves on chat alone, stays on chat.
func TestBedrockGPTOnResponses(t *testing.T) {
	fresh(t)
	up := &gptBedrock{responses: true}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "ABSK-test"
	// as v0.1.392 saved it: no Responses base
	p.Anthropic, p.Chat, p.Responses = srv.URL+"/anthropic", srv.URL+"/openai/v1", ""
	p.Models = []string{"global.openai.gpt-6-luna", "openai.gpt-oss-120b-1:0"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, body, reply, upstream string }{
		{"codex", "/v1/responses", fmt.Sprintf(codexToolsBody, "bedrock/global.openai.gpt-6-luna"), "from responses", "/openai/v1/responses"},
		{"chat client", "/v1/chat/completions", fmt.Sprintf(chatToolsBody, "bedrock/global.openai.gpt-6-luna"), "from responses", "/openai/v1/responses"},
		{"gpt-oss", "/v1/chat/completions", `{"model":"bedrock/openai.gpt-oss-120b-1:0","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "from chat", "/openai/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up.mu.Lock()
			up.calls = nil
			up.mu.Unlock()
			code, body := post(t, tc.path, tc.body)
			if code != 200 || !strings.Contains(body, tc.reply) {
				t.Fatalf("status %d: %s", code, body)
			}
			c := up.last()
			if c.path != tc.upstream || c.head.Get("Authorization") != "Bearer ABSK-test" {
				t.Fatalf("upstream: %s %v", c.path, c.head)
			}
			// Codex's request goes to Responses at once; a chat client's is
			// relayed on chat first, and taken to Responses when refused
			up.mu.Lock()
			n := len(up.calls)
			up.mu.Unlock()
			if tc.name == "codex" && n != 1 {
				t.Fatalf("%d calls", n)
			}
		})
	}
}

// A chat completions endpoint with no Responses API that refuses tools
// with reasoning as Bedrock's does (#176) is asked again with
// reasoning_effort "none", whether the agent spoke Responses (Codex,
// translated) or chat (relayed).
func TestToolsEffortRefusedOnChat(t *testing.T) {
	fresh(t)
	up := &gptBedrock{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: srv.URL + "/v1",
		Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, body string }{
		{"codex", "/v1/responses", fmt.Sprintf(codexToolsBody, "relay/gpt-6-luna")},
		{"chat client", "/v1/chat/completions", fmt.Sprintf(chatToolsBody, "relay/gpt-6-luna")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up.mu.Lock()
			up.calls = nil
			up.mu.Unlock()
			code, body := post(t, tc.path, tc.body)
			if code != 200 || !strings.Contains(body, "from chat") {
				t.Fatalf("status %d: %s", code, body)
			}
			up.mu.Lock()
			defer up.mu.Unlock()
			if len(up.calls) != 2 || up.calls[1].body["reasoning_effort"] != "none" {
				t.Fatalf("%d calls, last %v", len(up.calls), up.calls[len(up.calls)-1].body)
			}
		})
	}
}
