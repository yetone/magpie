package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A plugin's provider whose `api` is on Mistral's API (a Mistral Vibe
// plugin's https://api.mistral.ai/v1) is Mistral to the gateway (#5464):
// an earlier turn's thinking reaches its fetch as the typed part Mistral
// takes, not as reasoning_content, which Mistral turns the whole request
// away for (422 extra_forbidden, #5462). Claude Code's turns are
// translated, and a streamed Chat request is relayed; both are covered.
// A plugin whose `api` is on no preset's host gets what it got before.
func TestPluginInFrontOfMistralIsMistral(t *testing.T) {
	t.Setenv("FAKE_DEEPSEEK", "1") // a model the build replays reasoning for
	t.Setenv("FAKE_API", "https://api.mistral.ai/v1")
	var mu sync.Mutex
	var seen []string
	pid := besideFake(t, "vibeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		var q struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		json.Unmarshal(b, &q)
		for i, m := range q.Messages {
			if _, ok := m["reasoning_content"]; ok {
				// Mistral's own words for it
				w.WriteHeader(422)
				io.WriteString(w, `{"detail":[{"type":"extra_forbidden","loc":["body","messages",`+itoa(i)+`,"assistant","reasoning_content"],"msg":"Extra inputs are not permitted"}]}`)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	h := New().Handler()
	ask := func(path, body string) (int, string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer magpie")
		req.Header.Set("anthropic-version", "2023-06-01")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	last := func() string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			t.Fatal("the plugin's fetch was never asked")
		}
		return seen[len(seen)-1]
	}
	thinkingPart := `{"thinking":[{"text":"7*13=91.","type":"text"}],"type":"thinking"}` // keys sorted, as compactJSON writes them
	// Claude Code's turn, translated to the plugin's Chat API: the
	// reporter's case
	code, body := ask("/v1/messages", `{"model":"`+pid+`/deepseek-v4-pro","max_tokens":64,"messages":[
		{"role":"user","content":"Is 91 prime?"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"7*13=91.","signature":""},{"type":"text","text":"91 = 7 x 13."}]},
		{"role":"user","content":"And 97?"}]}`)
	if code != 200 {
		t.Errorf("translated: %d %s", code, body)
	}
	if got := last(); strings.Contains(got, "reasoning_content") || !strings.Contains(compactJSON(got), thinkingPart) {
		t.Errorf("translated: the plugin's fetch got %s", got)
	}
	// a streamed Chat request, relayed
	code, body = ask("/v1/chat/completions", `{"model":"`+pid+`/deepseek-v4-pro","stream":true,"messages":[
		{"role":"user","content":"Is 91 prime?"},
		{"role":"assistant","content":"91 = 7 x 13.","reasoning_content":"7*13=91."},
		{"role":"user","content":"And 97?"}]}`)
	if code != 200 {
		t.Fatalf("relayed: %d %s", code, body)
	}
	if got := last(); strings.Contains(got, "reasoning_content") || !strings.Contains(compactJSON(got), thinkingPart) {
		t.Errorf("relayed: the plugin's fetch got %s", got)
	}
}

// A plugin whose `api` is on no vendor's preset host keeps
// reasoning_content, as a DeepSeek model on an unknown API wants it.
func TestPluginOnAnUnknownAPIIsNoVendor(t *testing.T) {
	t.Setenv("FAKE_DEEPSEEK", "1")
	var mu sync.Mutex
	var seen []string
	pid := besideFake(t, "otherco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+pid+`/deepseek-v4-pro","max_tokens":64,"messages":[
		{"role":"user","content":"Is 91 prime?"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"7*13=91.","signature":""},{"type":"text","text":"91 = 7 x 13."}]},
		{"role":"user","content":"And 97?"}]}`))
	req.Header.Set("Authorization", "Bearer magpie")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !strings.Contains(seen[len(seen)-1], `"reasoning_content"`) {
		t.Errorf("a plugin on fake.invalid lost the turn's reasoning_content: %v", seen)
	}
}

func compactJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
