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

// TestReasoningReplayed (#388): Codex offers its web_search, so its request
// to a provider that doesn't search by itself is translated, and the turn's
// reasoning was left out of it. A thinking model served on the Responses
// API (DeepSeek: "The reasoning_text in the thinking mode must be passed
// back") or on chat completions under its own name gets it back; OpenAI's
// own reads only its sealed reasoning and is sent none.
func TestReasoningReplayed(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r","status":"in_progress","output":[]}}`,
			`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "resp", Name: "Resp", Key: "k", Models: []string{"deepseek-flash"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "chat", Name: "Chat", Key: "k", Models: []string{"deepseek-flash"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	ask := func(model, reasoning string) string {
		mu.Lock()
		got = nil
		mu.Unlock()
		body := `{"model":"` + model + `","stream":true,"tools":[{"type":"function","name":"sh","parameters":{"type":"object"}},{"type":"web_search"}],"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` + reasoning + `,
			{"type":"function_call","call_id":"c1","name":"sh","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
		if code, b := post(t, "/v1/responses", body); code != 200 {
			t.Fatalf("%s: %d %s", model, code, b)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 {
			t.Fatalf("%s: upstream asked %d times", model, len(got))
		}
		return got[0]
	}

	var q struct {
		Input []struct {
			Type    string           `json:"type"`
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	sent := ask("resp/deepseek-flash", `{"type":"reasoning","summary":[{"type":"summary_text","text":"short"}],"content":[{"type":"reasoning_text","text":"THINK"}]}`)
	if err := json.Unmarshal([]byte(sent), &q); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, it := range q.Input {
		kinds = append(kinds, it.Type)
	}
	if strings.Join(kinds, ",") != "message,reasoning,function_call,function_call_output" || q.Input[1].Content[0]["type"] != "reasoning_text" || q.Input[1].Content[0]["text"] != "THINK" {
		t.Fatalf("Responses upstream sent %s", sent)
	}
	// what magpie handed Codex of a translated reply: the summary alone
	if sent := ask("resp/deepseek-flash", `{"type":"reasoning","summary":[{"type":"summary_text","text":"SUMMED"}]}`); !strings.Contains(sent, `"text":"SUMMED","type":"reasoning_text"`) {
		t.Fatalf("summary not replayed: %s", sent)
	}
	if sent := ask("chat/deepseek-flash", `{"type":"reasoning","summary":[{"type":"summary_text","text":"CHATTHINK"}]}`); !strings.Contains(sent, `"reasoning_content":"CHATTHINK"`) {
		t.Fatalf("chat upstream sent %s", sent)
	}

	// OpenAI's own is sent no plain reasoning
	r, err := parseResponses([]byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[{"type":"summary_text","text":"x"}]},{"type":"function_call","call_id":"c","name":"sh","arguments":"{}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.openai.com", "chatgpt.com"} {
		if b := buildResponses(r, "gpt", host, false); strings.Contains(string(b), `"reasoning"`) {
			t.Fatalf("%s sent reasoning: %s", host, b)
		}
	}
	// nor another model behind someone else's Responses API, which may
	// pass it on to OpenAI's
	if b := buildResponses(r, "gpt-5.4", "relay.example", false); strings.Contains(string(b), `"reasoning"`) {
		t.Fatalf("gpt-5.4 sent reasoning: %s", b)
	}
	if b := buildResponses(r, "deepseek-v4.1-flash", "relay.example", false); !strings.Contains(string(b), `"reasoning_text"`) {
		t.Fatalf("deepseek sent no reasoning: %s", b)
	}
}
