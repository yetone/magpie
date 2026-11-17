package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// largeConversation is a long agent conversation as its client sends it:
// a system prompt, tools with schemas, and turns of text, tool calls and
// tool results, about words of text in all (~4 bytes a token).
func largeConversation(proto provider.Protocol, model string, words int) []byte {
	para := strings.Repeat("The function reads the file, splits it on newlines and returns the lines that match. ", 12)
	type schema = map[string]any
	var tools []schema
	for i := range 30 {
		props := schema{"path": schema{"type": "string", "description": "Absolute path to the file " + fmt.Sprint(i)}, "limit": schema{"type": "integer"}}
		switch proto {
		case provider.Anthropic:
			tools = append(tools, schema{"name": fmt.Sprintf("Tool%d", i), "description": para[:400], "input_schema": schema{"type": "object", "properties": props, "required": []string{"path"}}})
		case provider.Chat:
			tools = append(tools, schema{"type": "function", "function": schema{"name": fmt.Sprintf("Tool%d", i), "description": para[:400], "parameters": schema{"type": "object", "properties": props, "required": []string{"path"}}}})
		default:
			tools = append(tools, schema{"type": "function", "name": fmt.Sprintf("Tool%d", i), "description": para[:400], "parameters": schema{"type": "object", "properties": props, "required": []string{"path"}}})
		}
	}
	var msgs []any
	n := 0
	for turn := 0; n < words*6; turn++ {
		id := fmt.Sprintf("call_%04d", turn)
		args := fmt.Sprintf(`{"path":"/src/pkg/file%d.go","limit":200}`, turn)
		result := para + strings.Repeat("line of code with \"quotes\" and unicode é 漢字\n", 20)
		n += len(para)*2 + len(result)
		switch proto {
		case provider.Anthropic:
			msgs = append(msgs,
				schema{"role": "user", "content": []any{schema{"type": "text", "text": para}}},
				schema{"role": "assistant", "content": []any{schema{"type": "text", "text": para}, schema{"type": "tool_use", "id": id, "name": "Tool1", "input": json.RawMessage(args)}}},
				schema{"role": "user", "content": []any{schema{"type": "tool_result", "tool_use_id": id, "content": result}}})
		case provider.Chat:
			msgs = append(msgs,
				schema{"role": "user", "content": para},
				schema{"role": "assistant", "content": para, "tool_calls": []any{schema{"id": id, "type": "function", "function": schema{"name": "Tool1", "arguments": args}}}},
				schema{"role": "tool", "tool_call_id": id, "content": result})
		default:
			msgs = append(msgs,
				schema{"type": "message", "role": "user", "content": []any{schema{"type": "input_text", "text": para}}},
				schema{"type": "message", "role": "assistant", "content": []any{schema{"type": "output_text", "text": para}}},
				schema{"type": "function_call", "call_id": id, "name": "Tool1", "arguments": args},
				schema{"type": "function_call_output", "call_id": id, "output": result})
		}
	}
	body := schema{"model": model, "max_tokens": 32000, "stream": false, "tools": tools}
	switch proto {
	case provider.Anthropic:
		// the user's words follow the last call's answer: a call left
		// unanswered is mended before it is relayed (toolBlocksPaired)
		msgs = append(msgs, schema{"role": "assistant", "content": []any{schema{"type": "text", "text": para}}},
			schema{"role": "user", "content": []any{schema{"type": "text", "text": "go on"}}})
		body["system"] = []any{schema{"type": "text", "text": para + para, "cache_control": schema{"type": "ephemeral"}}}
		body["messages"] = msgs
	case provider.Chat:
		// the user's message follows the last call's answer: a call left
		// unanswered is mended before it is relayed (toolPairsOut)
		msgs = append(msgs, schema{"role": "user", "content": "go on"})
		body["messages"] = append([]any{schema{"role": "system", "content": para + para}}, msgs...)
	default:
		delete(body, "max_tokens")
		body["instructions"] = para + para
		body["input"] = msgs
	}
	b, _ := json.Marshal(body)
	return b
}

func BenchmarkLargeRequest(b *testing.B) {
	for _, tc := range []struct {
		name, path, model string
		proto             provider.Protocol
		reply             string
	}{
		{"anthropic", "/v1/messages", "fake/m1", provider.Anthropic, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`},
		{"anthropic-group", "/v1/messages", "group/g", provider.Anthropic, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`},
		{"chat", "/v1/chat/completions", "fake/m1", provider.Chat, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`},
		{"responses", "/v1/responses", "fake/m1", provider.Responses, `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":7,"output_tokens":1}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			b.Setenv("XDG_CACHE_HOME", b.TempDir())
			f := &fake{ctype: "application/json", reply: tc.reply}
			up := httptest.NewServer(f)
			defer up.Close()
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
			switch tc.proto {
			case provider.Chat:
				p.Chat = up.URL + "/v1"
			case provider.Responses:
				p.Responses = up.URL + "/v1"
			case provider.Anthropic:
				p.Anthropic = up.URL
			}
			if err := provider.Save(p); err != nil {
				b.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"fake/m1"}, Routing: provider.Ordered}); err != nil {
				b.Fatal(err)
			}
			body := largeConversation(tc.proto, tc.model, 300_000)
			s := New()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for range b.N {
				// a served request's context ends with it, as a server's
				// does: one that never ends isn't held (holdBody), and
				// every reader of it decodes it again
				ctx, done := context.WithCancel(context.Background())
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, bytes.NewReader(body)).WithContext(ctx))
				done()
				if rec.Code != 200 || f.calls == 0 {
					b.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
