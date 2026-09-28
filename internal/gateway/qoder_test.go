package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/qoder"
)

// qoderUpstream stands in for api3.qoder.sh: it checks the wire body decodes to
// a well-formed Qoder chat body carrying the COSY envelope, then streams a
// Qoder SSE reply (each line an OpenAI chunk inside the envelope). It returns a
// cleanup that restores the real auth/URL hooks.
func qoderUpstream(t *testing.T, lines []string) func() {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Errorf("missing COSY Authorization")
		}
		if r.Header.Get("X-Model-Key") != "qfmodel" {
			t.Errorf("X-Model-Key = %q", r.Header.Get("X-Model-Key"))
		}
		plain := qoder.DecodeRequestBody(string(raw))
		if !gjson.ValidBytes(plain) {
			t.Errorf("decoded body is not JSON: %q", plain)
		}
		if gjson.GetBytes(plain, "agent_id").String() != "agent_common" {
			t.Errorf("agent_id = %q", gjson.GetBytes(plain, "agent_id").String())
		}
		if !gjson.GetBytes(plain, "messages").IsArray() {
			t.Errorf("no messages array in decoded body")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			io.WriteString(w, "data: "+l+"\n")
		}
	}))
	auth, url := qoderAuth, qoderURL
	qoderAuth = func(ctx context.Context) (string, string, error) { return "uid-test", "jt-test", nil }
	qoderURL = func() string { return up.URL }
	return func() { up.Close(); qoderAuth, qoderURL = auth, url }
}

// qoderChunk wraps an OpenAI chat.completion.chunk as Qoder's SSE envelope.
func qoderChunk(inner string) string {
	b, _ := json.Marshal(map[string]any{
		"headers": map[string][]string{"Content-Type": {"application/json"}},
		"body":    inner, "statusCodeValue": 200, "statusCode": "OK",
	})
	return string(b)
}

// TestQoderChatBody checks the IR request becomes a Qoder body: the caller's
// system prompt and tool contract ride in the system field, tool calls are
// replayed as XML, and tool results fold into a user turn.
func TestQoderChatBody(t *testing.T) {
	req := &Request{
		System:     "Be terse.",
		ToolChoice: "",
		Tools:      []Tool{{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
			{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"x"}`)}}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "the file"}}},
		},
	}
	b, err := qoderChatBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(b, "agent_id").String() != "agent_common" {
		t.Fatalf("agent_id")
	}
	if gjson.GetBytes(b, "model_config.key").String() != "qfmodel" {
		t.Fatalf("model key")
	}
	if !strings.Contains(gjson.GetBytes(b, "system.0.text").String(), "Read a file") {
		t.Fatalf("tool contract missing: %s", gjson.GetBytes(b, "system.0.text").String())
	}
	msgs := gjson.GetBytes(b, "messages")
	if msgs.Get("0.role").String() != "system" {
		t.Fatalf("first message is %s, want system", msgs.Get("0.role").String())
	}
	// assistant tool call replayed as XML in its content, tool result as a
	// user turn naming the call id
	body := string(b)
	for _, kw := range []string{"Function result for call c1", "the file"} {
		if !strings.Contains(body, kw) {
			t.Fatalf("missing %q in body", kw)
		}
	}
}

// qoderContent builds an OpenAI chunk inner JSON whose delta carries content,
// assembled through encoding/json so a Qoder tool-call block, written here with
// hex-escaped brackets, reaches the wire as real angle brackets without any
// literal tag in this source.
func qoderContent(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
		"object":  "chat.completion.chunk",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestServeQoder runs a request through serveQoder against a stubbed Qoder:
// text, a reasoning delta, an embedded tool call lifted into events, and usage
// all reach the client's protocol.
func TestServeQoder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	call := "\x3ctool_call\x3e\n\x3cfunction=read\x3e\n\x3cparameter=path\x3ex\x3c/parameter\x3e\n\x3c/function\x3e\n\x3c/tool_call\x3e"
	done := qoderUpstream(t, []string{
		qoderChunk(`{"choices":[{"delta":{"role":"assistant","reasoning_content":"thinking"}}],"object":"chat.completion.chunk"}`),
		qoderChunk(`{"choices":[{"delta":{"content":"Hi there"}}],"object":"chat.completion.chunk"}`),
		qoderChunk(qoderContent(t, call)),
		qoderChunk(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`),
		`{"firstTokenDuration":50,"totalDuration":200}`,
	})
	defer done()
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"model":"qoder/qfmodel","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	var u Usage
	if status, msg := s.serveQoder(w, r, provider.Chat, "qfmodel", []byte(`{"model":"qfmodel","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &u); status != 200 {
		t.Fatalf("status %d msg %q", status, msg)
	}
	out := w.Body.String()
	for _, kw := range []string{"thinking", "Hi there", "read", "path", "x"} {
		if !strings.Contains(out, kw) {
			t.Fatalf("streamed output missing %q:\n%s", kw, out)
		}
	}
}
