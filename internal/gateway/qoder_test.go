package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/qoder"
)

func testQoderModel(t *testing.T) qoder.ModelInfo {
	t.Helper()
	ms, err := qoder.ModelConfigs([]byte(`{"chat":[{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash","format":"openai","source":"system","is_vl":true,"is_reasoning":true,"max_input_tokens":200000}]}`))
	if err != nil || len(ms) != 1 {
		t.Fatalf("model fixture: %v", err)
	}
	return ms[0]
}

// qoderUpstream stands in for api3.qoder.sh: it checks the wire body decodes to
// a well-formed Qoder chat body carrying the COSY envelope, then streams a
// Qoder SSE reply (each line an OpenAI chunk inside the envelope). It returns a
// cleanup that restores the real auth/URL hooks.
func qoderUpstream(t *testing.T, lines []string, modelKeys ...string) func() {
	t.Helper()
	modelKey := "qfmodel"
	if len(modelKeys) > 0 {
		modelKey = modelKeys[0]
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Errorf("missing COSY Authorization")
		}
		if r.Header.Get("X-Model-Key") != modelKey {
			t.Errorf("X-Model-Key = %q", r.Header.Get("X-Model-Key"))
		}
		plain := qoder.DecodeRequestBody(string(raw))
		if !gjson.ValidBytes(plain) {
			t.Errorf("decoded body is not JSON: %q", plain)
		}
		if gjson.GetBytes(plain, "model_config.key").String() != modelKey {
			t.Errorf("wrong body model configuration")
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
	auth, url, model := qoderAuth, qoderURL, qoderModel
	qoderAuth = func(ctx context.Context, user string) (*qoder.Credential, error) {
		return &qoder.Credential{UID: "uid-test", Token: "jt-test", MachineID: "machine-test"}, nil
	}
	qoderModel = func(context.Context, string, string) (qoder.ModelInfo, error) { return testQoderModel(t), nil }
	qoderURL = func() string { return up.URL }
	return func() { up.Close(); qoderAuth, qoderURL, qoderModel = auth, url, model }
}

func TestQoderSelectedModel(t *testing.T) {
	done := qoderUpstream(t, []string{qoderChunk(qoderContent(t, "ok"))}, "other-model")
	defer done()
	ms, err := qoder.ModelConfigs([]byte(`{"chat":[{"key":"other-model","enable":true,"display_name":"Other","format":"custom","source":"system","is_vl":false,"is_reasoning":false,"max_input_tokens":64000,"custom_field":17}]}`))
	if err != nil {
		t.Fatal(err)
	}
	qoderModel = func(_ context.Context, user, key string) (qoder.ModelInfo, error) {
		if user != "selected@x" || key != "other-model" {
			t.Errorf("selected user/model %s %s", user, key)
		}
		return ms[0], nil
	}
	s := New()
	ch, status, msg := s.askQoder("other-model", "selected@x")(context.Background(), &Request{Model: "other-model", Effort: "max"})
	if ch == nil {
		t.Fatalf("ask: %d %s", status, msg)
	}
	for range ch {
	}
	b, err := qoderChatBody(&Request{Effort: "high"}, ms[0])
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(b, "model_config.custom_field").Int() != 17 || gjson.GetBytes(b, "parameters.context_length").Int() != 64000 || gjson.GetBytes(b, "parameters.enable_thinking").Bool() || gjson.GetBytes(b, "parameters.reasoning_effort").Exists() {
		t.Fatalf("selected config: %s", b)
	}
	qoderModel = func(context.Context, string, string) (qoder.ModelInfo, error) {
		return qoder.ModelInfo{}, errors.New("unknown model")
	}
	ch, status, _ = s.askQoder("unknown", "selected@x")(context.Background(), &Request{})
	if ch != nil || status != 400 {
		t.Fatalf("unknown model: %d", status)
	}
}

func TestQoderReasoning(t *testing.T) {
	model := testQoderModel(t)
	model.Efforts = []string{"low", "high"}
	for _, tt := range []struct {
		want, got string
		thinking  bool
	}{
		{"medium", "high", true}, {"max", "high", true}, {"none", "", false}, {"", "high", true}, {"invalid", "high", true},
	} {
		b, err := qoderChatBody(&Request{Effort: tt.want}, model)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(b, "parameters.reasoning_effort").String() != tt.got || gjson.GetBytes(b, "parameters.enable_thinking").Bool() != tt.thinking {
			t.Fatalf("effort %q: %s", tt.want, b)
		}
	}
}

func TestQoderMultipleToolResults(t *testing.T) {
	blocks, _, result := qoderBlocks(Message{Role: "user", Parts: []Part{
		{Kind: ToolResult, CallID: "A", Text: "result A"}, {Kind: ToolResult, CallID: "B", Text: "result B", IsError: true},
	}})
	if !result || len(blocks) != 4 {
		t.Fatalf("blocks %+v", blocks)
	}
	for i, want := range []string{"Function result for call A", "result A", "Function result for call B", "Error: result B"} {
		if !strings.HasPrefix(blocks[i].(map[string]any)["text"].(string), want) {
			t.Fatalf("block %d: %+v", i, blocks[i])
		}
	}
}

func qoderDecoded(lines ...string) []Event {
	var wire strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&wire, "data: %s\n\n", line)
	}
	ch := make(chan Event, 32)
	go decodeQoder(context.Background(), &http.Response{Body: io.NopCloser(strings.NewReader(wire.String()))}, ch, "qfmodel")
	var events []Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestQoderEOF(t *testing.T) {
	call := qoderCallOpen + `{"name":"read","arguments":{"path":"x"}}` + qoderCallClose
	for _, tt := range []struct{ input, text, stop string }{
		{"a < b and x <", "a < b and x <", "stop"},
		{call + "tail <", "tail <", "tool"},
		{"before " + qoderCallOpen + "incomplete", "before " + qoderCallOpen + "incomplete", "stop"},
		{"", "", "stop"},
	} {
		events := qoderDecoded(qoderChunk(qoderContent(t, tt.input)), "[DONE]")
		var text, stop string
		stops := 0
		for _, ev := range events {
			if ev.Kind == KText {
				text += ev.Text
			}
			if ev.Kind == KStop {
				stop = ev.Stop
				stops++
			}
		}
		if text != tt.text || stop != tt.stop || stops != 1 {
			t.Fatalf("EOF %q => %q %q stops %d", tt.input, text, stop, stops)
		}
	}
}

func TestQoderEnvelopeErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		for _, proto := range []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic, provider.Gemini} {
			for _, stream := range []bool{false, true} {
				for _, partial := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/stream=%t/partial=%t", proto, status, stream, partial), func(t *testing.T) {
						var lines []string
						if partial {
							lines = append(lines, qoderChunk(qoderContent(t, "some text")))
						}
						b, _ := json.Marshal(map[string]any{"statusCodeValue": status, "body": `{"message":"upstream failure"}`})
						if status == 429 {
							b, _ = json.Marshal(map[string]any{"statusCodeValue": status, "body": `{"message":"额度已耗尽"}`})
						}
						lines = append(lines, string(b))
						done := qoderUpstream(t, lines)
						defer done()
						s := New()
						req := &Request{Model: "qfmodel", Stream: stream}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						events, _, _ := s.askQoder("qfmodel", "test")(ctx, req)
						w := httptest.NewRecorder()
						var usage Usage
						code, msg := relayQoder(w, proto, req, events, &usage, cancel)
						want := status
						if want == 403 {
							want = 401
						}
						if stream && partial {
							want = 200
						}
						if code != want || w.Code != want || msg == "" || !strings.Contains(w.Body.String(), "error") {
							t.Fatalf("code %d/%d want %d, msg %s body %s", code, w.Code, want, msg, w.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestQoderSearchEnvelopeError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			in := make(chan Event, 3)
			in <- Event{Kind: KStart}
			if partial {
				in <- Event{Kind: KText, Text: "partial answer"}
			}
			in <- Event{Kind: KError, Status: 503, Text: "Qoder upstream unavailable"}
			close(in)
			out := make(chan Event, 4)
			req := &Request{Model: "qfmodel", Stream: stream}
			go func() {
				defer close(out)
				New().searchRounds(ctx, req, "search", in, nil, out)
			}()
			w := httptest.NewRecorder()
			var usage Usage
			code, msg := relayQoder(w, provider.Chat, req, out, &usage, cancel)
			cancel()
			want := 503
			if stream && partial {
				want = 200
			}
			if code != want || w.Code != want || msg == "" || !strings.Contains(w.Body.String(), "error") {
				t.Fatalf("stream=%t partial=%t: code %d/%d want %d, msg %s body %s", stream, partial, code, w.Code, want, msg, w.Body.String())
			}
		}
	}
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
	b, err := qoderChatBody(req, testQoderModel(t))
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
	if status, msg := s.serveQoder(w, r, provider.Chat, provider.Provider{Account: &provider.Account{User: "test"}}, "qfmodel", []byte(`{"model":"qfmodel","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &u); status != 200 {
		t.Fatalf("status %d msg %q", status, msg)
	}
	out := w.Body.String()
	for _, kw := range []string{"thinking", "Hi there", "read", "path", "x"} {
		if !strings.Contains(out, kw) {
			t.Fatalf("streamed output missing %q:\n%s", kw, out)
		}
	}
}
