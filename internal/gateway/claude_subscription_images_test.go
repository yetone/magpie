package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An image a tool returned reaches the Claude Code a subscription runs as
// MCP image content, which it hands its model as an image block: in an
// Anthropic tool_result, and beside a Chat tool message, where a client
// sends it in a user message after (smart-lty on Discord: Read on a PNG, a
// browser screenshot, reached the model as text only).
func TestSubscriptionToolResultImages(t *testing.T) {
	// a 1x1 PNG
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	cases := []struct {
		name string
		from provider.Protocol
		body string
		mime string
	}{
		{"anthropic tool_result", provider.Anthropic, `{"model":"m","max_tokens":10,"messages":[
			{"role":"user","content":"look at it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{"file_path":"a.png"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"read a.png"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}]}]}`, "image/png"},
		{"chat image after the tool message", provider.Chat, `{"model":"m","messages":[
			{"role":"user","content":"look at it"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"read a.png"},
			{"role":"user","content":[{"type":"text","text":"Attached image(s) from tool result:"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}}]}]}`, "image/png"},
		{"type read from the bytes", provider.Anthropic, `{"model":"m","max_tokens":10,"messages":[
			{"role":"user","content":"look at it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"read a.png"},{"type":"image","source":{"type":"base64","data":"` + png + `"}}]}]}]}`, "image/png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newSubscriptionBridge()
			run := &subscriptionRun{bridge: b, token: "tok", pending: map[string]chan mcpToolResult{}}
			b.runs["tok"] = run
			mux := http.NewServeMux()
			mux.HandleFunc("POST /cb/{token}", b.mcpCall)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			// Claude Code's tools/call, as the MCP helper hands it over
			answer := make(chan mcpToolResult, 1)
			go func() {
				res, err := http.Post(srv.URL+"/cb/tok", "application/json", strings.NewReader(`{"tool_call_id":"call_1","name":"Read","arguments":{}}`))
				if err != nil {
					answer <- mcpToolResult{}
					return
				}
				defer res.Body.Close()
				var r mcpToolResult
				_ = json.NewDecoder(res.Body).Decode(&r)
				answer <- r
			}()
			for deadline := time.Now().Add(2 * time.Second); ; {
				b.mu.Lock()
				n := len(b.calls)
				b.mu.Unlock()
				if n == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("call_1 was not registered")
				}
				time.Sleep(5 * time.Millisecond)
			}

			req, err := parse(c.from, []byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			found, results := b.findRun(req)
			if found != run {
				t.Fatal("no run for call_1")
			}
			if _, err := run.continueWith(results, nil); err != nil {
				t.Fatal(err)
			}
			var got mcpToolResult
			select {
			case got = <-answer:
			case <-time.After(2 * time.Second):
				run.finish()
				t.Fatal("call_1 never had its result")
			}
			var image map[string]any
			for _, part := range got.Content {
				if part["type"] == "image" {
					image = part
				}
			}
			if image == nil || image["data"] != png || image["mimeType"] != c.mime {
				out, _ := json.Marshal(got)
				t.Fatalf("the tool's image did not reach the agent: %s", out)
			}
			if got.Content[0]["type"] != "text" || !strings.Contains(got.Content[0]["text"].(string), "read a.png") {
				t.Fatalf("the tool's text: %v", got.Content[0])
			}

			// a run started anew is told the whole conversation, the image too
			prompt, _ := renderClaudePrompt(req)
			seen := false
			for _, block := range prompt {
				if src, _ := block["source"].(map[string]any); block["type"] == "image" && src["data"] == png {
					seen = true
				}
			}
			if !seen {
				out, _ := json.Marshal(prompt)
				t.Fatalf("a new run's prompt has no image: %s", out)
			}
		})
	}
}
