package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// geminiRoomUpstream answers Chat and Gemini's generateContent, whole or
// streamed, and keeps each request's path and body.
type geminiRoomUpstream struct {
	mu   sync.Mutex
	got  []map[string]any
	path []string
}

func (u *geminiRoomUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.got = append(u.got, q)
	u.path = append(u.path, r.URL.Path)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.URL.Path, ":streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
		return
	}
	if strings.Contains(r.URL.Path, ":generateContent") {
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		return
	}
	if st, _ := q["stream"].(bool); st {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			`data: [DONE]`))
		return
	}
	io.WriteString(w, `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *geminiRoomUpstream) take() ([]map[string]any, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	got, path := u.got, u.path
	u.got, u.path = nil, nil
	return got, path
}

// replyLimitOf is the reply limit an upstream request named, in whichever
// words it went: Chat's max_tokens or max_completion_tokens, Gemini's
// generationConfig.maxOutputTokens. -1 for none.
func replyLimitOf(q map[string]any) int {
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if n, ok := q[k].(float64); ok {
			return int(n)
		}
	}
	if gc, ok := q["generationConfig"].(map[string]any); ok {
		if n, ok := gc["maxOutputTokens"].(float64); ok {
			return int(n)
		}
	}
	return -1
}

// fwxkogaia on X (v0.1.4697): Nowledge Mem asked models/gemini-4-hydrogen
// at Google AI Studio with max_tokens 8192 and no reasoning. Gemini 4
// Hydrogen thinks at high by default and the limit counts its thoughts, so
// it spent all 8192 thinking and every reply was a 502, "cut at its length,
// the whole output spent on reasoning". A small reply limit to a Gemini
// that thinks by default, on Google's own routes, with no reasoning asked,
// has room to think above it, as Tencent's DeepSeek has. Reasoning asked,
// the user's own Max output, a large limit, a Flash-Lite, a Gemini client's
// own maxOutputTokens, OpenRouter's Gemini and a relay's go as they came.
func TestGeminiCapGetsThinkingRoom(t *testing.T) {
	fresh(t)
	u := &geminiRoomUpstream{}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	models := []string{"gemini-4-hydrogen", "gemini-2.5-pro", "gemini-3.5-flash-lite", "gemini-4-argon"}
	for _, p := range []provider.Provider{
		// AI Studio's OpenAI-compatible list names its models models/…
		{ID: "google", Name: "Google Gemini", Key: "k", Models: append([]string{"models/gemini-4-hydrogen"}, models...), Chat: "http://generativelanguage.googleapis.com/v1beta/openai"},
		{ID: "gnative", Name: "Gemini API", Key: "k", Models: models, Gemini: "http://generativelanguage.googleapis.com/v1beta"},
		{ID: "or", Name: "OpenRouter", Preset: "openrouter", Key: "k", Models: []string{"google/gemini-4-hydrogen"}, Chat: "http://openrouter.ai/api/v1"},
		{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gemini-4-hydrogen"}, Chat: "http://relay.example.com/v1"},
		{ID: "tencent", Name: "Tencent", Key: "k", Models: []string{"deepseek/deepseek-flash"}, Chat: "http://tokenhub.tencentmaas.com/plan/v3"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelOutput("google/gemini-4-argon", 8192); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	const msgs = `"messages":[{"role":"user","content":"hi"}]`
	chat := func(model, more string) [2]string {
		return [2]string{"/v1/chat/completions", `{"model":"` + model + `",` + more + msgs + `}`}
	}
	const room = 24000 // replyRoom at high
	for _, c := range []struct {
		name string
		ask  [2]string
		want int
	}{
		// the reporter's request, as Nowledge Mem sends it
		{"mem's 8192", chat("google/models/gemini-4-hydrogen", `"max_tokens":8192,"temperature":0.7,`), 8192 + room},
		{"no models/ prefix", chat("google/gemini-4-hydrogen", `"max_tokens":8192,`), 8192 + room},
		{"max_completion_tokens", chat("google/gemini-4-hydrogen", `"max_completion_tokens":4096,`), 4096 + room},
		{"2.5 pro", chat("google/gemini-2.5-pro", `"max_tokens":8192,`), 8192 + room},
		{"responses", [2]string{"/v1/responses", `{"model":"google/gemini-4-hydrogen","max_output_tokens":8192,"input":"hi"}`}, 8192 + room},
		{"messages", [2]string{"/v1/messages", `{"model":"google/gemini-4-hydrogen","max_tokens":8192,` + msgs + `}`}, 8192 + room},
		{"gemini api, chat client", chat("gnative/gemini-4-hydrogen", `"max_tokens":8192,`), 8192 + room},
		{"effort asked", chat("google/gemini-4-hydrogen", `"max_tokens":8192,"reasoning_effort":"low",`), 8192},
		{"thinking_config asked", chat("google/gemini-4-hydrogen", `"max_tokens":8192,"extra_body":{"google":{"thinking_config":{"thinking_level":"low"}}},`), 8192},
		{"messages thinking", [2]string{"/v1/messages", `{"model":"google/gemini-4-hydrogen","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":2048},` + msgs + `}`}, 8192},
		{"messages thinking off", [2]string{"/v1/messages", `{"model":"google/gemini-4-hydrogen","max_tokens":8192,"thinking":{"type":"disabled"},` + msgs + `}`}, 8192},
		{"user's max output", chat("google/gemini-4-argon", `"max_tokens":8192,`), 8192},
		{"a large limit", chat("google/gemini-4-hydrogen", `"max_tokens":32000,`), 32000},
		{"no limit", chat("google/gemini-4-hydrogen", ``), -1},
		{"flash-lite", chat("google/gemini-3.5-flash-lite", `"max_tokens":8192,`), 8192},
		{"gemini client's own", [2]string{"/v1beta/models/gnative/gemini-4-hydrogen:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8192}}`}, 8192},
		{"openrouter's gemini", chat("or/google/gemini-4-hydrogen", `"max_tokens":8192,`), 8192},
		{"a relay's gemini", chat("relay/gemini-4-hydrogen", `"max_tokens":8192,`), 8192},
		// an Anthropic client is read the same way for Tencent's DeepSeek
		{"messages to tencent's deepseek", [2]string{"/v1/messages", `{"model":"tencent/deepseek/deepseek-flash","max_tokens":4096,` + msgs + `}`}, 4096 + room},
	} {
		if code, raw := postJSON(t, s, c.ask[0], c.ask[1]); code != 200 {
			t.Fatalf("%s: %d %s", c.name, code, raw)
		}
		got, path := u.take()
		if len(got) != 1 {
			t.Fatalf("%s: %d requests upstream", c.name, len(got))
		}
		if n := replyLimitOf(got[0]); n != c.want {
			t.Errorf("%s: reply limit %d at %s, want %d", c.name, n, path[0], c.want)
		}
	}
}

// The same on Vertex AI, where a Chat client's request goes to Gemini's
// generateContent at the user's project.
func TestVertexGeminiCapGetsThinkingRoom(t *testing.T) {
	v := &vertexUp{}
	s := vertexServe(t, v, vertexLevels)
	for _, c := range []struct {
		name, body string
		want       float64
	}{
		{"no reasoning", `{"model":"google-vertex/gemini-3.1-pro-preview","max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`, 8192 + 24000},
		{"effort asked", `{"model":"google-vertex/gemini-3.1-pro-preview","max_tokens":8192,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, 8192},
		{"flash-lite", `{"model":"google-vertex/gemini-2.5-flash-lite","max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`, 8192},
	} {
		vertexAsk(t, s, "/v1/chat/completions", c.body)
		gen, _ := v.last(t).body["generationConfig"].(map[string]any)
		if n, _ := gen["maxOutputTokens"].(float64); n != c.want {
			t.Errorf("%s: maxOutputTokens %v, want %v", c.name, n, c.want)
		}
	}
}
