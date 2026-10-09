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

// toolsOnChatRefused is what a relay answers a Chat Completions request
// with tools for a model it serves with tools on Responses alone (#1308,
// Stevenzc888's relay, as the issue quotes it).
const toolsOnChatRefused = `{"error":{"message":"status_code=400, gpt-6.1-sol requires Responses for tool calls; this account only supports Chat Completions","type":"invalid_request_error"}}`

// toolsRelay is a relay at one base URL serving Chat Completions and
// Responses, which takes gpt-6.1-sol with tools on Responses alone.
type toolsRelay struct {
	mu                  sync.Mutex
	chatCalls, resCalls int
	resBody             []byte
}

func (f *toolsRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/chat/completions":
		f.chatCalls++
		var req struct {
			Tools []any `json:"tools"`
		}
		json.Unmarshal(b, &req)
		if len(req.Tools) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, toolsOnChatRefused)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"gpt-6.1-sol","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`))
	case "/v1/responses":
		f.resCalls++
		f.resBody = b
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`))
	default:
		http.NotFound(w, r)
	}
}

// piChatTools is a turn as Pi's openai-completions API sends it (pi-ai's
// openai-completions.js), the API magpie wires Pi on for a custom
// provider with a Chat URL: streamed, its tools, a thinking level.
const piChatTools = `{"model":"relay/gpt-6.1-sol","stream":true,"stream_options":{"include_usage":true},"store":false,
  "max_completion_tokens":32000,"reasoning_effort":"high",
  "messages":[{"role":"developer","content":"You are Pi."},{"role":"user","content":"list the files"}],
  "tools":[{"type":"function","function":{"name":"ls","description":"List files","parameters":{"type":"object","properties":{"path":{"type":"string"}}},"strict":false}}]}`

// A custom provider whose Chat and Responses URLs are the same relay
// (#1308): Pi asks on Chat Completions with tools, the relay says the model
// takes tools on Responses alone, and the request goes to the provider's
// Responses URL instead of the relay's 400 reaching Pi — and later turns
// go there at once.
func TestToolsThatNeedResponsesGoThere(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &toolsRelay{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if n := mustFind(t, "relay").Native("gpt-6.1-sol"); n != provider.Chat {
		t.Fatalf("Pi is wired on %q for the relay's model, the test assumes chat", n)
	}
	handler := New().Handler()
	for i, want := range []int{1, 2} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(piChatTools)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "responses ok") {
			t.Fatalf("call %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
		f.mu.Lock()
		chat, res, body := f.chatCalls, f.resCalls, string(f.resBody)
		f.mu.Unlock()
		if chat != 1 || res != want {
			t.Fatalf("call %d: chat=%d responses=%d", i, chat, res)
		}
		if !strings.Contains(body, `"name":"ls"`) || !strings.Contains(body, `"model":"gpt-6.1-sol"`) {
			t.Errorf("call %d: Responses request lost the tools or model: %s", i, body)
		}
	}
}

// The sibling on a translated request: Claude Code's Messages turn with
// tools, spoken to the relay as Chat Completions, goes on to Responses too.
func TestTranslatedToolsThatNeedResponsesGoThere(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &toolsRelay{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"relay/gpt-6.1-sol","max_tokens":100,"stream":true,
	  "messages":[{"role":"user","content":"list the files"}],
	  "tools":[{"name":"ls","description":"List files","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "responses ok") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.chatCalls != 1 || f.resCalls != 1 || !strings.Contains(string(f.resBody), `"name":"ls"`) {
		t.Fatalf("chat=%d responses=%d: %s", f.chatCalls, f.resCalls, f.resBody)
	}
}

// The same relay with no Responses URL set: nothing else to ask, and the
// error tells the user which URL to set rather than the relay's word alone.
func TestToolsThatNeedResponsesNameTheMissingURL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &toolsRelay{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol"},
		Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(piChatTools)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "gpt-6.1-sol is served on /v1/responses, which Relay has no URL for") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.resCalls != 0 {
		t.Errorf("Responses asked with no URL for it: %d", f.resCalls)
	}
}
