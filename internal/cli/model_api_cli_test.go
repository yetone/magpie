package cli

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// apiHome is groupsHome with a relay whose one key serves its models on
// three APIs, and a provider with one.
func apiHome(t *testing.T, chat, anthropic string) {
	t.Helper()
	groupsHome(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: chat, Responses: "http://127.0.0.1:1/v1", Anthropic: anthropic, Models: []string{"gpt-x", "claude-y", "mixed"}},
		{ID: "solo", Name: "Solo", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// magpie model api sets the API a model is asked on, as the provider
// editor's picker does, for several models at once (yew_fence_931206 on
// Discord: a script setting each model of one key's relay); apis lists
// them, and --reset or auto takes them off. --help prints the usage, and a
// flag, an unknown API, a model the provider doesn't list or an API it
// has no URL for is refused with nothing written, the other models of the
// same command included.
func TestModelAPICmd(t *testing.T) {
	apiHome(t, "http://127.0.0.1:1/v1", "http://127.0.0.1:1")
	if out, err := runOut(t, "model", "apis"); err != nil || !strings.Contains(out, "no model has an API of yours yet") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	if out, err := runOut(t, "model", "api", "relay/gpt-x", "relay/mixed", "responses"); err != nil || !strings.Contains(out, "relay/gpt-x") || !strings.Contains(out, "relay/mixed") || !strings.Contains(out, "asked on Responses") {
		t.Fatalf("set: %q %v", out, err)
	}
	if out, err := runOut(t, "model", "api", "relay/claude-y", "Anthropic"); err != nil || !strings.Contains(out, "asked on Anthropic") {
		t.Fatalf("set: %q %v", out, err)
	}
	want := map[string]string{"relay/gpt-x": "responses", "relay/mixed": "responses", "relay/claude-y": "anthropic"}
	if got := settings.Load().ModelAPIs; !maps.Equal(got, want) {
		t.Fatalf("settings: %v", got)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if api, ok := p.ModelAPI("gpt-x"); !ok || api != provider.Responses {
		t.Fatalf("relay/gpt-x is asked on %q %v", api, ok)
	}
	if out, err := runOut(t, "model", "api", "relay/gpt-x"); err != nil || !strings.Contains(out, "asked on Responses") || !strings.Contains(out, "chat, responses, anthropic") {
		t.Fatalf("show: %q %v", out, err)
	}
	if out, err := runOut(t, "model", "apis"); err != nil || !strings.Contains(out, "relay/claude-y  anthropic") || !strings.Contains(out, "relay/gpt-x     responses") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, err := runOut(t, "model", "apis", "relay"); err != nil || !strings.Contains(out, "relay/mixed     responses") || !strings.Contains(out, "relay/claude-y  anthropic") {
		t.Fatalf("list relay: %q %v", out, err)
	}
	for _, bad := range [][]string{
		{"model", "api"},
		{"model", "api", "--reset"},
		{"model", "api", "relay/gpt-x", "--force"},
		{"model", "api", "relay/gpt-x", "openai"},
		{"model", "api", "relay/gpt-x", "two words"},
		{"model", "api", "relay/nope", "chat"},
		{"model", "api", "relay/nope", "--reset"},
		{"model", "api", "relay/gpt-x", "relay/nope", "chat"},
		{"model", "api", "relay/mixed", "--force", "chat"},
		{"model", "api", "relay/mixed", "gemini"}, // no Gemini URL
		{"model", "api", "solo/m", "chat"},        // one API only
		{"model", "api", "nobody/m", "chat"},
		{"model", "api", "gpt-x", "chat"},
		{"model", "apis", "nobody"},
		{"model", "apis", "--force"},
	} {
		if _, err := runOut(t, bad...); err == nil {
			t.Errorf("took %v", bad)
		}
	}
	for _, help := range [][]string{
		{"model", "api", "--help"},
		{"model", "api", "relay/gpt-x", "chat", "--help"},
		{"model", "api", "-h"},
		{"model", "apis", "--help"},
	} {
		if out, err := runOut(t, help...); err != nil || !strings.Contains(out, "magpie model api <provider/model>... <api>") {
			t.Errorf("%v: %q %v", help, out, err)
		}
	}
	if got := settings.Load().ModelAPIs; !maps.Equal(got, want) {
		t.Fatalf("a refused command wrote: %v", got)
	}
	if out, err := runOut(t, "model", "api", "relay/gpt-x", "--reset"); err != nil || !strings.Contains(out, "auto") {
		t.Fatalf("reset: %q %v", out, err)
	}
	if out, err := runOut(t, "model", "api", "relay/mixed", "relay/claude-y", "auto"); err != nil || strings.Count(out, "✓") != 2 {
		t.Fatalf("auto: %q %v", out, err)
	}
	if got := settings.Load().ModelAPIs; len(got) != 0 {
		t.Fatalf("after reset: %v", got)
	}
	if out, err := runOut(t, "model", "api", "relay/gpt-x"); err != nil || !strings.Contains(out, "auto") {
		t.Fatalf("show after reset: %q %v", out, err)
	}
}

// The API set from the terminal reaches a gateway already running, as a
// save in the editor does: a model asked on chat completions goes to
// Anthropic's messages from the next request on, and back after --reset.
func TestModelAPIReachesTheRunningGateway(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			io.WriteString(w, `{"id":"c","object":"chat.completion","model":"mixed","choices":[{"index":0,"message":{"role":"assistant","content":"from chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		case "/v1/messages":
			// asked translated, it is asked to stream
			w.Header().Set("Content-Type", "text/event-stream")
			for _, e := range []string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"mixed","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from messages"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			} {
				io.WriteString(w, e+"\n\n")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	apiHome(t, up.URL+"/v1", up.URL)
	gw := httptest.NewServer(gateway.New().Handler())
	defer gw.Close()
	ask := func(want, path string) {
		t.Helper()
		mu.Lock()
		hits = nil
		mu.Unlock()
		res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"relay/mixed","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		mu.Lock()
		h := append([]string(nil), hits...)
		mu.Unlock()
		if res.StatusCode != 200 || !strings.Contains(string(b), want) || len(h) != 1 || h[0] != path {
			t.Fatalf("%d %s, upstream asked %v; want %s at %s", res.StatusCode, b, h, want, path)
		}
	}
	ask("from chat", "/v1/chat/completions")
	if _, err := runOut(t, "model", "api", "relay/mixed", "anthropic"); err != nil {
		t.Fatal(err)
	}
	ask("from messages", "/v1/messages")
	if _, err := runOut(t, "model", "api", "relay/mixed", "--reset"); err != nil {
		t.Fatal(err)
	}
	ask("from chat", "/v1/chat/completions")
}
