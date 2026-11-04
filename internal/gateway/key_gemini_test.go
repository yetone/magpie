package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A relay that gives out a key per protocol, Gemini's among them
// (corddis_114 on Discord): a Gemini model goes to the key made for the
// Gemini API, on generateContent, whatever the agent spoke, and Claude
// and GPT models never go there, though the Gemini key is the first.
func TestKeyMadeForGemini(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = r.Header.Get("x-goog-api-key")
		}
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		seen = append(seen, r.URL.Path+" "+key)
		gem := strings.HasPrefix(r.URL.Path, "/v1beta/models/")
		want := map[string]string{"/v1/messages": "k-ant", "/v1/chat/completions": "k-oai"}[r.URL.Path]
		if gem {
			want = "k-gem"
		}
		if key != want {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"this key is for another protocol"}}`)
			return
		}
		switch {
		case gem:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
		case r.URL.Path == "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case bytes.Contains(b, []byte(`"stream":true`)):
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		}
	}))
	defer srv.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL, Gemini: srv.URL + "/v1beta",
		Key: "k-gem", KeyProtocol: provider.Gemini, Models: []string{"claude-opus-4-8", "gpt-5.5", "gemini-3-pro"},
		Keys: []provider.KeyAccount{{Key: "k-ant", Protocol: provider.Anthropic}, {Key: "k-oai", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ path, body, want string }{
		// Claude Code and an OpenAI agent asking for Gemini: the Gemini key
		{"/v1/messages", `{"model":"relay/gemini-3-pro","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1beta/models/gemini-3-pro:streamGenerateContent k-gem"},
		{"/v1/chat/completions", `{"model":"relay/gemini-3-pro","messages":[{"role":"user","content":"hi"}]}`, "/v1beta/models/gemini-3-pro:streamGenerateContent k-gem"},
		// Claude and GPT never go to the Gemini key, the first though it is
		{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
		{"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
		{"/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
	} {
		seen = nil
		code, body := post(t, x.path, x.body)
		if code != 200 || len(seen) != 1 || seen[0] != x.want {
			t.Errorf("%s %s: %d %s, upstream saw %v", x.path, x.body, code, body, seen)
		}
	}
	if got := modelFamily("google/gemini-2.5-flash"); got != provider.Gemini {
		t.Errorf("gemini: %q", got)
	}
}
