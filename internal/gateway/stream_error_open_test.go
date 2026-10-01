package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A key or account whose stream says it's rate limited (a 429 as an error
// event, before any content) and then keeps its connection open hands the
// turn to the next one at once; with none left, the agent is told the
// error at once. Read on until the vendor hung up, the agent waited with
// nothing sent, not even headers, and the next account was asked only
// then (Discord: after an account's 429, Hermes waited with nothing shown
// until the preferred account was changed).
func TestStreamErrorKeptOpenFailsOverAtOnce(t *testing.T) {
	chatAnswer := sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
	responsesAnswer := sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"hello"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1}}}`)
	limited := map[provider.Protocol]string{
		provider.Chat: sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"error":{"code":429,"type":"rate_limit_error","message":"rate limited"}}`),
		provider.Responses: sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
			`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"rate limited"}}}`),
		provider.Anthropic: sse(`event: error`+"\n"+`data: {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`),
	}
	for _, proto := range provider.Protocols {
		for _, both := range []bool{false, true} {
			name := string(proto) + "/one limited"
			if both {
				name = string(proto) + "/both limited"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.ReadAll(r.Body)
					key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					if key == "" {
						key = r.Header.Get("x-api-key")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if key == "first" || both {
						io.WriteString(w, limited[proto])
						w.(http.Flusher).Flush()
						select { // said, and not hung up
						case <-r.Context().Done():
						case <-time.After(20 * time.Second):
						}
						return
					}
					switch proto {
					case provider.Chat:
						io.WriteString(w, chatAnswer)
					case provider.Responses:
						io.WriteString(w, responsesAnswer)
					case provider.Anthropic:
						io.WriteString(w, anthropicAnswer)
					}
				}))
				defer up.Close()
				p := provider.Provider{ID: "plan", Name: "Plan", Key: "first", Models: []string{"m1"}, Keys: []provider.KeyAccount{{Key: "second"}}}
				path, body := "/v1/chat/completions", `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
				switch proto {
				case provider.Chat:
					p.Chat = up.URL + "/v1"
				case provider.Responses:
					p.Responses = up.URL + "/v1"
					path, body = "/v1/responses", `{"model":"plan/m1","stream":true,"input":"hi"}`
				case provider.Anthropic:
					p.Anthropic = up.URL
					path, body = "/v1/messages", `{"model":"plan/m1","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				srv := httptest.NewServer(New().Handler())
				defer srv.Close()
				began := time.Now()
				res, err := (&http.Client{Timeout: 10 * time.Second}).Post(srv.URL+path, "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatalf("after %v: %v", time.Since(began), err)
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				if took := time.Since(began); err != nil || took > 3*time.Second {
					t.Fatalf("answered after %v (%v): %d %s", took, err, res.StatusCode, b)
				}
				if !both && (res.StatusCode != 200 || !strings.Contains(string(b), "hello") && !strings.Contains(string(b), "from b")) {
					t.Fatalf("the next key didn't answer: %d %s", res.StatusCode, b)
				}
				if both && !strings.Contains(string(b), "rate limited") {
					t.Fatalf("the agent isn't told: %d %s", res.StatusCode, b)
				}
			})
		}
	}
}
