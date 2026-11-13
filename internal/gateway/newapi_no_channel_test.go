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
	// a fork's refusal of a GPT on Anthropic, where it differs from New
	// API's 503 (WandAPI answers 404)
	status int
	body   string
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
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			io.WriteString(w, f.body)
			return
		}
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

// wandAPINoSource is WandAPI's 404 to Claude Code's /v1/messages for
// gpt-6.1-terra, byte for byte as its 181 bytes were (equinox0253 on X,
// 11-13): no channel, no model named, the entry protocol in other words
// than gpt-6.1-sol's "不支持当前入口协议".
const wandAPINoSource = `{"type":"error","error":{"type":"not_found_error","message":"当前没有能承接该入口协议的货源，请联系管理员为端点补充入口格式或配置协议适配。"}}`

// WandAPI answers 404 where New API answers 503 (equinox0253 on X). The
// fallback follows the body's shape (New API's model_not_found code, its
// message naming the group), not the status alone: a 404 of any other kind
// is the relay's word, and nothing is remembered.
func TestNoChannelAs404FromAFork(t *testing.T) {
	for _, c := range []struct {
		name, body string
		falls      bool
	}{
		{"fork no channel", `{"error":{"message":"分组 default 下模型 gpt-6.1-sol 暂无渠道","type":"new_api_error","code":"model_not_found"}}`, true},
		// WandAPI's own words (equinox0253 on X, 11-13): the protocol
		// named, no channel; a wrong endpoint (wrongEndpoint) as the
		// tweet's screenshot reads it
		{"entry protocol", `{"error":{"message":"模型 gpt-6.1-sol 不支持当前入口协议","type":"new_api_error","code":"model_not_found"}}`, true},
		{"entry protocol, Anthropic's shape", `{"type":"error","error":{"type":"invalid_request_error","message":"模型 gpt-6.1-sol 不支持当前入口协议"}}`, true},
		{"entry protocol, no source (gpt-6.1-terra)", wandAPINoSource, true},
		{"missing model", `{"error":{"message":"The model gpt-6.1-sol does not exist or you do not have access to it.","type":"invalid_request_error","code":"model_not_found"}}`, false},
		{"wrong base URL", `404 page not found`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			f := &newAPI{status: 404, body: c.body}
			up := httptest.NewServer(f)
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol"},
				Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
				t.Fatal(err)
			}
			srv := New()
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"relay/gpt-6.1-sol","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
			if c.falls {
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "gpt ok") || srv.fits("relay", "gpt-6.1-sol", provider.Anthropic) {
					t.Fatalf("no fallback: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != 404 || !srv.fits("relay", "gpt-6.1-sol", provider.Anthropic) || f.count("/v1/chat/completions gpt") != 0 {
				t.Fatalf("a real 404 was turned into a protocol switch: %d %s %v", rec.Code, rec.Body.String(), f.calls)
			}
		})
	}
}
