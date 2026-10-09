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

// A relay whose one key serves a model only on Anthropic's messages, while
// its chat completions take the others and answer that one with no channel
// (01huadalang on Discord): with Anthropic picked for the model in its
// provider's editor, an agent's chat request for it goes to /v1/messages,
// translated, and chat completions is never tried; another model still
// goes to chat.
func TestModelAPIRoutes(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		mu.Lock()
		hits = append(hits, r.URL.Path+" "+body.Model)
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/chat/completions" && body.Model == "plain":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
				`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
				`data: [DONE]`))
		case r.URL.Path == "/v1/chat/completions":
			http.Error(w, `{"error":{"message":"no available channel for model `+body.Model+` under group default"}}`, 503)
		case r.URL.Path == "/v1/messages" && body.Model == "mixed":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"mixed","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
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
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1", Anthropic: up.URL, Models: []string{"mixed", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelAPI("relay/mixed", "anthropic"); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	for _, c := range []struct{ model, want, hit string }{
		{"mixed", "from messages", "/v1/messages mixed"},
		{"plain", "from chat", "/v1/chat/completions plain"},
	} {
		mu.Lock()
		hits = nil
		mu.Unlock()
		res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"relay/`+c.model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		mu.Lock()
		h := append([]string(nil), hits...)
		mu.Unlock()
		if res.StatusCode != 200 || !strings.Contains(string(b), c.want) {
			t.Fatalf("%s: %d %s (upstream %v)", c.model, res.StatusCode, b, h)
		}
		if len(h) != 1 || h[0] != c.hit {
			t.Fatalf("%s: upstream asked %v", c.model, h)
		}
	}
}

// A model the user picked Responses for stays on Responses after the relay
// once answers it there with "use /v1/chat/completions" (01huadalang on
// Discord: 为什么会转成 chat，我都设置的 response): the next request was
// sent on chat completions, the first API the provider has a URL for,
// for as long as magpie ran. Now it is asked on Responses again, and the
// relay's error reaches the agent rather than a request on an API the
// user said the model isn't asked on.
func TestModelAPIPickHoldsAfterWrongEndpoint(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	refused := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		first := !refused
		refused = true
		mu.Unlock()
		if r.URL.Path != "/v1/responses" {
			http.Error(w, `{"error":{"message":"no available channel for model resp under group default"}}`, 503)
			return
		}
		if first {
			http.Error(w, `{"error":{"message":"this request is not supported in /v1/responses, use /v1/chat/completions","type":"invalid_request_error"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`event: response.created
data: {"type":"response.created","response":{"id":"r","object":"response","status":"in_progress","model":"resp","output":[]}}`,
			`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"from responses"}`,
			`event: response.completed
data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","model":"resp","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"from responses"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`))
	}))
	defer up.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"resp"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelAPI("relay/resp", "responses"); err != nil {
		t.Fatal(err)
	}
	// each agent API on a gateway of its own, whose memory of the
	// refusal starts empty
	for _, c := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"relay/resp","stream":true,"input":"hi"}`},
		{"/v1/chat/completions", `{"model":"relay/resp","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		mu.Lock()
		hits, refused = nil, false
		mu.Unlock()
		srv := New()
		gw := httptest.NewServer(srv.Handler())
		for i := range 2 {
			res, err := http.Post(gw.URL+c.path, "application/json", strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if i == 1 && (res.StatusCode != 200 || !strings.Contains(string(b), "from responses")) {
				mu.Lock()
				h := append([]string(nil), hits...)
				mu.Unlock()
				t.Errorf("%s, after the refusal: %d %s (upstream %v)", c.path, res.StatusCode, b, h)
			}
		}
		gw.Close()
		mu.Lock()
		for _, h := range hits {
			if h != "/v1/responses" {
				t.Errorf("%s: asked on %s, though the model is picked for Responses (upstream %v)", c.path, h, hits)
				break
			}
		}
		mu.Unlock()
	}
}
