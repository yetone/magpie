package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fake is an upstream that records what it got and replies with a script.
type fake struct {
	t     *testing.T
	got   []byte
	path  string
	head  http.Header
	reply string // SSE body
	ctype string // "" means text/event-stream; "none" sends no header at all
	code  int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header
	ct := f.ctype
	if ct == "" {
		ct = "text/event-stream"
	}
	if ct == "none" {
		w.Header()["Content-Type"] = nil // like the ChatGPT backend: the body alone says it streams
	} else {
		w.Header().Set("Content-Type", ct)
	}
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// setup points magpie's provider file at a temp dir and adds one provider
// speaking only the given protocol, backed by the fake.
func setup(t *testing.T, proto provider.Protocol, f *fake) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

func post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func events(body string) []map[string]any {
	var out []map[string]any
	readSSE(strings.NewReader(body), func(_, data string) error {
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			out = append(out, m)
		}
		return nil
	})
	return out
}

type fallbackFake struct {
	chatBody      []byte
	responsesBody []byte
	chatCalls     int
	responseCalls int
}

func (f *fallbackFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/v1/chat/completions":
		f.chatCalls++
		f.chatBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"This model is not supported in the v1/chat/completions endpoint. Use v1/responses."}}`)
	case "/v1/responses":
		f.responseCalls++
		f.responsesBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"fallback ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
		))
	default:
		http.NotFound(w, r)
	}
}

func TestAnthropicClientFallsBackFromChatToResponses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fallbackFake{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	handler := New().Handler()
	call := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := call(`{"model":"m1","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.chatCalls != 1 || f.responseCalls != 1 {
		t.Fatalf("calls: chat=%d responses=%d", f.chatCalls, f.responseCalls)
	}
	var chat, responses map[string]any
	if json.Unmarshal(f.chatBody, &chat) != nil || json.Unmarshal(f.responsesBody, &responses) != nil {
		t.Fatalf("bad translated requests: chat=%s responses=%s", f.chatBody, f.responsesBody)
	}
	if chat["stream"] != true || responses["stream"] != true || responses["model"] != "m1" {
		t.Errorf("requests: chat=%v responses=%v", chat, responses)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage aUsage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Text != "fallback ok" || out.Usage.InputTokens != 4 || out.Usage.OutputTokens != 2 {
		t.Errorf("reply: %s", body)
	}
	code, body = call(`{"model":"m1","max_tokens":100,"messages":[{"role":"user","content":"again"}]}`)
	if code != 200 {
		t.Fatalf("second status %d: %s", code, body)
	}
	if f.chatCalls != 1 || f.responseCalls != 2 {
		t.Fatalf("cached calls: chat=%d responses=%d", f.chatCalls, f.responseCalls)
	}
}

func TestNotChatModel(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"message":"not a chat model; use /v1/responses"}}`, true},
		{404, `use v1/completions`, true},
		{429, `rate limit`, false},
		{200, `use v1/responses`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestAnthropicClientChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":100,"stream":true,"system":"be brief",
	  "messages":[{"role":"user","content":"read a.go"},
	    {"role":"assistant","content":[{"type":"tool_use","id":"t0","name":"read","input":{"path":"x"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"t0","content":"package x"}]}],
	  "tools":[{"name":"read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
	  "thinking":{"type":"enabled","budget_tokens":5000}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	// what the upstream saw
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" || f.head.Get("Authorization") != "Bearer k" {
		t.Errorf("upstream path/auth: %s %v", f.path, f.head)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" {
		t.Errorf("messages: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" || up["stream"] != true || up["max_tokens"] != float64(100) {
		t.Errorf("params: %v", up)
	}
	if _, ok := up["tools"].([]any)[0].(map[string]any)["function"]; !ok {
		t.Errorf("tools: %v", up["tools"])
	}
	// what the client got
	evs := events(body)
	var types []string
	for _, e := range evs {
		types = append(types, e["type"].(string))
	}
	want := "message_start content_block_start content_block_delta content_block_stop content_block_start content_block_delta content_block_delta content_block_stop content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop"
	if got := strings.Join(types, " "); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if b := evs[1]["content_block"].(map[string]any); b["type"] != "thinking" {
		t.Errorf("first block: %v", b)
	}
	if b := evs[8]["content_block"].(map[string]any); b["type"] != "tool_use" || b["name"] != "read" || b["id"] != "call_1" {
		t.Errorf("tool block: %v", b)
	}
	md := evs[len(evs)-2]
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" || md["usage"].(map[string]any)["output_tokens"] != float64(5) {
		t.Errorf("message_delta: %v", md)
	}
}

func TestChatClientAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"ls","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"dir\":\".\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	// non-streaming client
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"system","content":"sys"},{"role":"user","content":"ls"}],
	  "tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}],"temperature":0.2,"max_tokens":50}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["system"] != "sys" || up["max_tokens"] != float64(50) || up["temperature"] != 0.2 || up["stream"] != true {
		t.Errorf("upstream: %s", f.got)
	}
	if f.head.Get("x-api-key") != "k" || f.head.Get("anthropic-version") == "" {
		t.Errorf("headers: %v", f.head)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}
	}
	json.Unmarshal([]byte(body), &out)
	c := out.Choices[0]
	if c.Message.Content != "hello" || c.FinishReason != "tool_calls" || len(c.Message.ToolCalls) != 1 ||
		c.Message.ToolCalls[0].Function.Arguments != `{"dir":"."}` || c.Message.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("reply: %s", body)
	}
	if out.Usage.PromptTokens != 10 || out.Usage.CompletionTokens != 4 {
		t.Errorf("usage: %s", body)
	}
}

func TestResponsesClientChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/responses", `{"model":"m1","stream":true,"instructions":"you are codex",
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"list"}]},
	    {"type":"function_call","call_id":"c0","name":"shell","arguments":"{\"cmd\":\"pwd\"}"},
	    {"type":"function_call_output","call_id":"c0","output":"/tmp"}],
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"reasoning":{"effort":"high"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[2].(map[string]any)["tool_calls"] == nil || msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("upstream messages: %v", msgs)
	}
	if up["reasoning_effort"] != "high" {
		t.Errorf("effort: %v", up["reasoning_effort"])
	}
	var types []string
	var done []map[string]any
	for _, e := range events(body) {
		types = append(types, e["type"].(string))
		if e["type"] == "response.output_item.done" {
			done = append(done, e["item"].(map[string]any))
		}
	}
	want := "response.created response.in_progress response.output_item.added response.content_part.added response.output_text.delta response.output_text.done response.content_part.done response.output_item.done response.output_item.added response.function_call_arguments.delta response.function_call_arguments.done response.output_item.done response.completed"
	if got := strings.Join(types, " "); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if len(done) != 2 || done[1]["type"] != "function_call" || done[1]["call_id"] != "call_9" || done[1]["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("items: %v", done)
	}
}

// Parallel tool calls each keep their own call_id: a repeated one makes
// the next turn fail upstream with "Duplicate value for 'tool_call_id'".
func TestResponsesClientParallelToolCalls(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"shell","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"shell","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"cmd\":\"pwd\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_c","function":{"name":"shell","arguments":"{\"cmd\":\"date\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/responses", `{"model":"m1","stream":true,
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"run three"}]}],
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var got []string
	for _, e := range events(body) {
		switch e["type"] {
		case "response.function_call_arguments.done":
			got = append(got, fmt.Sprintf("args.done %v %v", e["call_id"], e["arguments"]))
		case "response.output_item.done":
			item := e["item"].(map[string]any)
			got = append(got, fmt.Sprintf("item.done %v %v", item["call_id"], item["arguments"]))
		case "response.completed":
			for _, it := range e["response"].(map[string]any)["output"].([]any) {
				item := it.(map[string]any)
				got = append(got, fmt.Sprintf("output %v %v", item["call_id"], item["arguments"]))
			}
		}
	}
	want := []string{
		`args.done call_a {"cmd":"ls"}`, `item.done call_a {"cmd":"ls"}`,
		`args.done call_b {"cmd":"pwd"}`, `item.done call_b {"cmd":"pwd"}`,
		`args.done call_c {"cmd":"date"}`, `item.done call_c {"cmd":"date"}`,
		`output call_a {"cmd":"ls"}`, `output call_b {"cmd":"pwd"}`, `output call_c {"cmd":"date"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tool calls:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestPassthroughRewritesModel(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	setup(t, provider.Anthropic, f)
	p, _ := provider.Find("fake")
	p.Models = []string{"real-model"}
	provider.Save(*p)
	code, body := post(t, "/v1/messages", `{"model":"fake/real-model","max_tokens":1.5e2,"messages":[],"metadata":{"x":12345678901234567890}}`)
	if code != 200 || !strings.Contains(body, `"id":"msg"`) {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`"model":"real-model"`)) || !bytes.Contains(f.got, []byte(`12345678901234567890`)) || !bytes.Contains(f.got, []byte(`1.5e2`)) {
		t.Errorf("rewritten body: %s", f.got)
	}
	if f.path != "/v1/messages" {
		t.Errorf("path %s", f.path)
	}
}

func TestChatPassthroughSendsDeveloperAsSystem(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[]}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","reasoning_effort":"high","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"a developer asks"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`{"content":"be brief","role":"system"}`)) || bytes.Contains(f.got, []byte(`"role":"developer"`)) || !bytes.Contains(f.got, []byte(`"content":"a developer asks"`)) {
		t.Errorf("forwarded body: %s", f.got)
	}
}

func TestErrorsAndUnknownModel(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance","type":"x"}}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 402 || !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "Fake: Insufficient Balance") {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1/chat/completions", `{"model":"nope","messages":[]}`)
	if code != 404 || !strings.Contains(body, `"error":{`) || !strings.Contains(body, "m1") {
		t.Errorf("%d %s", code, body)
	}
}

func TestModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"fake/m1"`) || !strings.Contains(rec.Body.String(), `"display_name"`) {
		t.Errorf("%s", rec.Body.String())
	}
}

func TestGeminiClientChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Sure"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	req := `{"systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"id":"fc0","name":"read_file","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"id":"fc0","name":"read_file","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
	  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", req)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m1" || up["stream"] != true || up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) {
		t.Errorf("upstream: %s", f.got)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["content"] != "be brief" || msgs[3].(map[string]any)["tool_call_id"] != "fc0" {
		t.Errorf("messages: %v", msgs)
	}
	if fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any); !strings.Contains(string(mustJSON(fn["parameters"])), `"type":"object"`) {
		t.Errorf("schema not lowercased: %v", fn)
	}
	evs := events(body)
	var parts []map[string]any
	var finish string
	for _, e := range evs {
		c := e["candidates"].([]any)[0].(map[string]any)
		for _, p := range c["content"].(map[string]any)["parts"].([]any) {
			parts = append(parts, p.(map[string]any))
		}
		if fr, ok := c["finishReason"]; ok {
			finish = fr.(string)
		}
	}
	if len(parts) != 3 || parts[0]["thought"] != true || parts[1]["text"] != "Sure" {
		t.Errorf("parts: %v", parts)
	}
	fc, _ := parts[2]["functionCall"].(map[string]any)
	if fc == nil || fc["name"] != "read_file" || fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call: %v", parts[2])
	}
	last := evs[len(evs)-1]
	um := last["usageMetadata"].(map[string]any)
	if finish != "STOP" || um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("finish/usage: %s %v", finish, um)
	}
	// non-streaming, error shape, countTokens, model list
	code, body = post(t, "/v1beta/models/fake/m1:generateContent", req)
	if code != 200 || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"name":"read_file"`) {
		t.Errorf("generateContent: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", req)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) {
		t.Errorf("models: %s", rec.Body.String())
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// A signed-in Codex account: the ChatGPT backend only streams and rejects
// the sampling knobs, so a plain non-streaming Responses call is
// translated, signed with the account's tokens, and answered as JSON.
func TestCodexAccountUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)

	f := &fake{t: t, ctype: "none", reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`)}
	up := httptest.NewServer(f)
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":20,"temperature":0.3}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/backend-api/codex/responses" || f.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(f.head.Get("Authorization"), "Bearer h.") {
		t.Fatalf("upstream call: %s %v", f.path, f.head)
	}
	var upstream map[string]any
	json.Unmarshal(f.got, &upstream)
	if _, ok := upstream["max_output_tokens"]; ok || upstream["temperature"] != nil || upstream["stream"] != true || upstream["store"] != false || upstream["model"] != "gpt-5.5" {
		t.Fatalf("upstream body: %s", f.got)
	}
	var res map[string]any
	json.Unmarshal([]byte(body), &res)
	if res["object"] != "response" || !strings.Contains(body, "pong") {
		t.Fatalf("reply: %s", body)
	}

	// a streaming call is relayed as-is, minus what the backend rejects
	f.got, f.head = nil, nil
	code, body = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true,"max_output_tokens":20}`)
	if code != 200 || !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("stream: %d %s", code, body)
	}
	json.Unmarshal(f.got, &upstream)
	if _, ok := upstream["max_output_tokens"]; ok {
		t.Fatalf("relayed body: %s", f.got)
	}
	if _, isList := upstream["input"].([]any); !isList {
		t.Fatalf("relayed input not a list: %s", f.got)
	}
}

func TestConversationID(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Affinity", "ses_1")
	if got := conversationID(h, []byte(`{}`)); got != "ses_1" {
		t.Errorf("agent session header: %q", got)
	}
	turn1 := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"}]}`
	turn2 := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"},{"role":"assistant","content":"done"},{"role":"user","content":"thanks"}]}`
	other := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"write docs"}]}`
	a, b, c := conversationID(http.Header{}, []byte(turn1)), conversationID(http.Header{}, []byte(turn2)), conversationID(http.Header{}, []byte(other))
	if a != b || a == c || !strings.HasPrefix(a, "magpie-") {
		t.Errorf("derived ids: %q %q %q", a, b, c)
	}
	r1 := conversationID(http.Header{}, []byte(`{"input":[{"role":"user","content":"hi"}]}`))
	r2 := conversationID(http.Header{}, []byte(`{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","output":"x"}]}`))
	if r1 != r2 {
		t.Errorf("responses ids: %q %q", r1, r2)
	}
}

func TestOpenCodeGetsConversationSession(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[]}`}
	up := setup(t, provider.Chat, f)
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	if code, out := post(t, "/v1/chat/completions", body); code != 200 || f.head.Get("x-opencode-session") != "" {
		t.Fatalf("other vendors get no OpenCode header: %d %s %v", code, out, f.head)
	}

	// The same upstream, reached under OpenCode's host name.
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: "http://opencode.ai/zen/go/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	send := func(h http.Header) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		for k, v := range h {
			req.Header[k] = v
		}
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return f.head.Get("x-opencode-session")
	}
	if got := send(http.Header{"Session_id": {"codex-ses"}}); got != "codex-ses" {
		t.Errorf("agent's session: %q", got)
	}
	if a, b := send(nil), send(nil); a == "" || a != b {
		t.Errorf("derived session: %q %q", a, b)
	}
}
