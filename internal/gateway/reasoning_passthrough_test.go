package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// deepseekResponses plays OpenCode Go's /responses for deepseek-v4.1-flash
// in thinking mode: a request with a reasoning item that carries no
// reasoning_text is refused with the 400 #1104's reporter saw, word for
// word. The error names the input, so it reads as one over an item.
type deepseekResponses struct {
	mu     sync.Mutex
	inputs [][]map[string]any
}

func (u *deepseekResponses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(body, &q)
	u.mu.Lock()
	u.inputs = append(u.inputs, q.Input)
	u.mu.Unlock()
	for _, it := range q.Input {
		if it["type"] != "reasoning" {
			continue
		}
		ok := false
		content, _ := it["content"].([]any)
		for _, c := range content {
			if m, _ := c.(map[string]any); m["type"] == "reasoning_text" && m["text"] != "" {
				ok = true
			}
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"The `+"`reasoning_text`"+` in the thinking mode must be passed back to the API.","type":"invalid_request_error","param":"input","code":"invalid_request_error"}}`)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_ds","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_ds","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func (u *deepseekResponses) last() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inputs[len(u.inputs)-1]
}

// TestDeepSeekReasoningPassedThrough (#1104): Codex on OpenCode Go's
// deepseek-v4.1-flash, its request passed through as it is. Codex hands a
// reasoning item back as it got it (codex-rs keeps reasoning_text content
// when it has some, and sends content and encrypted_content as null
// otherwise), so an item from a turn magpie translated, or another provider
// in a mixed group served, has only its summary. Passed on as it was,
// DeepSeek refused the request; and the refusal, naming the input, made
// magpie leave every reasoning item out of the requests after it.
func TestDeepSeekReasoningPassedThrough(t *testing.T) {
	fresh(t)
	up := &deepseekResponses{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Models: []string{"deepseek-v4.1-flash"}, Responses: srv.URL + "/zen/go/v1"}); err != nil {
		t.Fatal(err)
	}
	ask := func(reasoning string) (int, string) {
		body := `{"model":"opencode-go/deepseek-v4.1-flash","stream":true,"store":false,"reasoning":{"effort":"max","summary":"auto"},"include":["reasoning.encrypted_content"],
		  "tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],"input":[
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
		  ` + reasoning + `,
		  {"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
		  {"type":"function_call_output","call_id":"call_1","output":"main.go"}]}`
		return post(t, "/v1/responses", body)
	}
	reasoningText := func(input []map[string]any) string {
		for _, it := range input {
			if it["type"] == "reasoning" {
				content, _ := it["content"].([]any)
				for _, c := range content {
					if m, _ := c.(map[string]any); m["type"] == "reasoning_text" {
						s, _ := m["text"].(string)
						return s
					}
				}
			}
		}
		return ""
	}

	// a summary only, as magpie gives Codex in a translated reply
	if code, b := ask(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Looking at main.go"}],"content":null,"encrypted_content":null}`); code != 200 {
		t.Fatalf("summary-only reasoning: %d %s", code, b)
	}
	if got := reasoningText(up.last()); got != "Looking at main.go" {
		t.Fatalf("reasoning_text sent = %q, want the summary", got)
	}

	// nothing to give back: the refusal reaches the client...
	if code, _ := ask(`{"type":"reasoning","id":"rs_2","summary":[],"content":null,"encrypted_content":null}`); code == 200 {
		t.Fatal("a reasoning item with nothing in it was answered")
	}
	// ...and the next turn's reasoning, which DeepSeek gave, still goes back
	if code, b := ask(`{"type":"reasoning","id":"rs_3","summary":[],"content":[{"type":"reasoning_text","text":"I should list the files first."}],"encrypted_content":null}`); code != 200 {
		t.Fatalf("after a refusal: %d %s", code, b)
	}
	if got := reasoningText(up.last()); got != "I should list the files first." {
		t.Fatalf("after a refusal, reasoning_text sent = %q", got)
	}
}
