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

// #7833's repro, as kongk0ng posted it: Claude Code's turn after ToolSearch
// loaded TaskStop, to Command Code's DeepSeek V4.1 Flash.
const commandCodeRefsRepro = `{
  "model": "commandcode/deepseek/deepseek-v4.1-flash",
  "max_tokens": 32,
  "thinking": {"type": "disabled"},
  "tools": [
    {"name": "ToolSearch", "description": "Find a tool.", "input_schema": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}},
    {"name": "TaskStop", "description": "Stop a task.", "input_schema": {"type": "object", "properties": {}}}
  ],
  "messages": [
    {"role": "user", "content": "stop the background task"},
    {"role": "assistant", "content": [
      {"type": "text", "text": "Loading TaskStop."},
      {"type": "tool_use", "id": "call_1", "name": "ToolSearch", "input": {"query": "select:TaskStop"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "call_1", "content": [
        {"type": "tool_reference", "tool_name": "TaskStop"}
      ]}
    ]}
  ]
}`

func commandCodeAt(t *testing.T) (*commandCodeAPI, func(body string) (int, string)) {
	t.Helper()
	fresh(t)
	up := &commandCodeAPI{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "commandcode", Name: "Command Code", Key: "k", Anthropic: srv.URL,
		Models: []string{"deepseek/deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler() // one gateway, which learns
	return up, func(body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
}

func sentJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toolSearchAnswer is the tool_result for call_1 in body's messages[i].
func toolSearchAnswer(t *testing.T, m map[string]any, i int) string {
	t.Helper()
	msgs := m["messages"].([]any)
	if i >= len(msgs) {
		t.Fatalf("sent %d messages: %s", len(msgs), sentJSON(t, m))
	}
	b, _ := json.Marshal(msgs[i].(map[string]any)["content"])
	return string(b)
}

// #7833 (kongk0ng): Command Code answers a tool_reference in a tool_result,
// ToolSearch's answer, with 400 "Invalid input at messages.2.content", and
// names no block. magpie reads message 2 of what it sent, finds the
// reference there, asks again at once with it as text, and sends Command
// Code text from then on.
func TestCommandCodeContentRefusalAtToolSearchGoesAsText(t *testing.T) {
	up, post := commandCodeAt(t)
	code, out := post(commandCodeRefsRepro)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	sent := up.all()
	if len(sent) != 2 || !hasResultRefs(sentJSON(t, sent[0])) {
		t.Fatalf("Command Code asked %d times, want the reference once and then text", len(sent))
	}
	if got := toolSearchAnswer(t, sent[1], 2); got != `[{"content":[{"text":"Tool TaskStop is loaded and can be called now.","type":"text"}],"tool_use_id":"call_1","type":"tool_result"}]` {
		t.Errorf("the ToolSearch answer went as %s", got)
	}

	// learned: the next turn goes as text the first time
	n := len(sent)
	if code, out := post(commandCodeRefsRepro); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if sent = up.all(); len(sent) != n+1 || hasResultRefs(sentJSON(t, sent[n])) {
		t.Fatalf("Command Code asked %d more times, want once, as text", len(sent)-n)
	}
}

// The reporter's turn had Claude Code's system message in messages, which
// magpie moves to the top for Command Code (#1407), so the index in "Invalid
// input at messages.72.content" counts the messages as sent, one fewer than
// the client's. Here the reference is the client's message 3 and the sent
// message 2; the client's message 2 is the tool_use. Text beside the
// reference is refused the same.
func TestCommandCodeContentRefusalCountsMessagesAsSent(t *testing.T) {
	up, post := commandCodeAt(t)
	code, out := post(`{"model":"commandcode/deepseek/deepseek-v4.1-flash","max_tokens":32,
		"tools":[{"name":"ToolSearch","description":"Find a tool.","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"name":"TaskStop","description":"Stop a task.","input_schema":{"type":"object","properties":{}},"defer_loading":true}],
		"messages":[
			{"role":"user","content":"stop the background task"},
			{"role":"system","content":[{"type":"text","text":"# Environment <cwd>"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"ToolSearch","input":{"query":"select:TaskStop"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"Found 1 tool."},{"type":"tool_reference","tool_name":"TaskStop"}]}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	sent := up.all()
	if len(sent) != 3 {
		t.Fatalf("Command Code asked %d times, want the role refused, then the content, then taken", len(sent))
	}
	last := sent[2]
	if got := toolSearchAnswer(t, last, 2); got != `[{"content":[{"text":"Found 1 tool.","type":"text"},{"text":"Tool TaskStop is loaded and can be called now.","type":"text"}],"tool_use_id":"call_1","type":"tool_result"}]` {
		t.Errorf("the ToolSearch answer went as %s", got)
	}
	for _, x := range last["tools"].([]any) {
		if tl := x.(map[string]any); tl["defer_loading"] != nil {
			t.Errorf("%s went with defer_loading, but nothing can load it by reference", tl["name"])
		}
	}
}

// The newer ToolSearch answer carries the loaded tool's definition in the
// tool_result (#5404), which Command Code reads no better: the definition
// goes on the tools list and its reference, refused next, as text.
func TestCommandCodeContentRefusalAtToolDefinition(t *testing.T) {
	up, post := commandCodeAt(t)
	code, out := post(`{"model":"commandcode/deepseek/deepseek-v4.1-flash","max_tokens":32,
		"tools":[{"name":"ToolSearch","description":"Find a tool.","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}],
		"messages":[
			{"role":"user","content":"stop the background task"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"ToolSearch","input":{"query":"select:TaskStop"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"tool_definition","definition":{"name":"TaskStop","description":"Stop a task.","input_schema":{"type":"object"}}}]}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	sent := up.all()
	if len(sent) != 3 {
		t.Fatalf("Command Code asked %d times, want the definition refused, then its reference, then taken", len(sent))
	}
	last := sent[2]
	if got := toolSearchAnswer(t, last, 2); got != `[{"content":[{"text":"Tool TaskStop is loaded and can be called now.","type":"text"}],"tool_use_id":"call_1","type":"tool_result"}]` {
		t.Errorf("the ToolSearch answer went as %s", got)
	}
	if tools, _ := json.Marshal(last["tools"]); !strings.Contains(string(tools), `"TaskStop"`) {
		t.Errorf("tools = %s, want TaskStop's definition on the list", tools)
	}
}

// A content refusal at a message with no reference (or deferred block) in
// it is about something else in that message, and one naming a block inside
// a message's content is about that block: each 400 goes back as it came,
// asked once, and nothing is learned, so the references still go to a
// vendor that read them before.
func TestContentRefusalWithoutAReferencePassedOn(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		said := ""
		switch {
		case strings.Contains(string(b), "bad turn"):
			said = "Invalid input at messages.0.content"
		case strings.Contains(string(b), "bad block"):
			said = "messages.2.content.0: unexpected `tool_use_id` found in `tool_result` blocks: call_1"
		}
		if said != "" {
			w.WriteHeader(400)
			e, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": said}})
			w.Write(e)
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: srv.URL,
		Models: []string{"deepseek/deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	post := func(first string) (int, string) {
		body := strings.Replace(strings.Replace(commandCodeRefsRepro, "commandcode/", "anth/", 1), "stop the background task", first, 1)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	asked := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
	for i, first := range []string{"bad turn", "bad block"} {
		code, out := post(first)
		if code != 400 || !strings.Contains(out, "messages.") {
			t.Fatalf("%s: status %d: %s, want the refusal passed on", first, code, out)
		}
		if n := len(asked()); n != i+1 {
			t.Fatalf("%s: asked %d times, want once", first, n-i)
		}
	}
	if code, out := post("stop the background task"); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if b := asked(); !hasResultRefs([]byte(b[len(b)-1])) {
		t.Errorf("the reference went as text, though nothing refused it: %s", b[len(b)-1])
	}
}
