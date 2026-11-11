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

// New API's own words when a group has no channel for a model on the API
// asked, in the shape of the API asked (equinox0253 on X: gpt-6.1-sol on the
// relay's Anthropic URL, where only its Claude models have a channel).
const (
	newAPINoChannelClaude = `{"type":"error","error":{"type":"new_api_error","message":"No available channel for model gpt-6.1-sol under group default (request id: 20261111093000123456789)"}}`
	newAPINoChannelOpenAI = `{"error":{"message":"No available channel for model claude-sonnet-4-5 under group default (request id: 20261111093000123456790)","type":"new_api_error","param":"","code":"model_not_found"}}`
)

// newAPI serves GPTs on Chat and Responses and Claude on Anthropic's
// Messages alone, and answers anything else 503 as New API does.
type newAPI struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *newAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	gpt := strings.Contains(string(b), `"gpt-6.1-sol"`)
	f.calls[r.URL.Path+map[bool]string{true: " gpt", false: " claude"}[gpt]]++
	f.mu.Unlock()
	refuse := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, body)
	}
	switch {
	case r.URL.Path == "/v1/messages" && gpt:
		refuse(newAPINoChannelClaude)
	case r.URL.Path != "/v1/messages" && !gpt:
		refuse(newAPINoChannelOpenAI)
	case r.URL.Path == "/v1/messages":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"claude ok"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	case r.URL.Path == "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"gpt-6.1-sol","choices":[{"delta":{"role":"assistant","content":"gpt ok"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`))
	case r.URL.Path == "/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"gpt ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *newAPI) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[k]
}

// A relay with Chat, Responses and Anthropic URLs, whose Anthropic channel
// serves Claude only: Claude Code's Messages turn for a GPT is asked on
// Anthropic first, the relay's 503 "no available channel" sends it on to
// Chat, the GPT is remembered off Anthropic, and a Claude model still goes
// to Anthropic.
func TestNoChannelOnOneAPIGoesToTheNext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &newAPI{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol", "claude-sonnet-4-5"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	handler := New().Handler()
	ask := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"relay/`+model+`","max_tokens":100,"stream":true,
		  "messages":[{"role":"user","content":"hi"}]}`)))
		return rec
	}
	for i := 0; i < 2; i++ {
		rec := ask("gpt-6.1-sol")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "gpt ok") {
			t.Fatalf("gpt call %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if n := f.count("/v1/messages gpt"); n != 1 {
		t.Errorf("the GPT was asked on Anthropic %d times, want once (remembered)", n)
	}
	rec := ask("claude-sonnet-4-5")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "claude ok") {
		t.Fatalf("claude: status %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.count("/v1/messages claude"); n != 1 || f.count("/v1/chat/completions claude")+f.count("/v1/responses claude") != 0 {
		t.Errorf("Claude left Anthropic: %v", f.calls)
	}
}

// With no other API to ask, the relay's word reaches the client and nothing
// is remembered.
func TestNoChannelWithNothingElseIsTheError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &newAPI{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"relay/gpt-6.1-sol","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "No available channel") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !srv.fits("relay", "gpt-6.1-sol", provider.Anthropic) {
		t.Error("the only API was marked unfit")
	}
}
