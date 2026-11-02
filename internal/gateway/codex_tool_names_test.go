package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// namesBackend stands in for the ChatGPT backend's check of tool names:
// a name outside ^[a-zA-Z0-9_.-]+\z, a call's in input or a tool's in
// tools, is the 400 wing3556 got on a compaction (Discord, 11-02), word
// for word. A request that passes is answered with a call to the first
// tool it declares, by the name it was given, or with text when it
// declares none. It keeps every body it was sent.
type namesBackend struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (n *namesBackend) last(t *testing.T) []byte {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.bodies) == 0 {
		t.Fatal("the ChatGPT backend was asked nothing")
	}
	return n.bodies[len(n.bodies)-1]
}

var backendNameOK = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// namesRefused is the backend's 400 for the first name in b it refuses,
// or "" when it takes them all.
func namesRefused(b []byte) string {
	bad := func(field string) string {
		return `{"error":{"message":"Invalid '` + field + `': string does not match pattern. Expected a string that matches the pattern '^[a-zA-Z0-9_.-]+\\z'.","type":"invalid_request_error","param":"` + field + `","code":"invalid_value"}}`
	}
	for i, it := range gjson.GetBytes(b, "input").Array() {
		switch it.Get("type").String() {
		case "function_call", "custom_tool_call":
			if !backendNameOK.MatchString(it.Get("name").String()) {
				return bad(fmt.Sprintf("input[%d].name", i))
			}
		}
	}
	for i, tl := range gjson.GetBytes(b, "tools").Array() {
		if k := tl.Get("type").String(); (k == "function" || k == "custom") && !backendNameOK.MatchString(tl.Get("name").String()) {
			return bad(fmt.Sprintf("tools[%d].name", i))
		}
	}
	return ""
}

func namesUpstream(t *testing.T) *namesBackend {
	t.Helper()
	n := &namesBackend{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","supported_reasoning_levels":[{"effort":"medium"}],"context_window":272000}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		n.mu.Lock()
		n.bodies = append(n.bodies, b)
		n.mu.Unlock()
		if msg := namesRefused(b); msg != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, msg)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		name := gjson.GetBytes(b, `tools.#(type=="function").name`).String()
		if name == "" {
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"Summary: did X."}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Summary: did X."}]}}`,
				`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}}`))
			return
		}
		item := `{"type":"function_call","id":"fc_1","call_id":"call_9","name":"` + name + `","arguments":"{\"q\":\"x\"}","status":"completed"}`
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"`+name+`","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"q\":\"x\"}"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[`+item+`],"usage":{"input_tokens":5,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return n
}

// wing3556 (Discord, 11-02): an agent's compaction through a Codex
// account failed 400 "Invalid 'input[83].name': string does not match
// pattern", the history holding a call by a name the ChatGPT backend
// refuses, which a model another provider served had written. Every path
// to the backend sends each such name as one it takes, the same on every
// request: Claude Code's Messages and a Chat client translated, a
// Responses client relayed, and Codex's own turn on its sign-in. A tool
// the agent declared under such a name is called back by that name.
func TestCodexToolNamesTheBackendTakes(t *testing.T) {
	codexSignedIn(t)
	n := namesUpstream(t)
	bad := []string{"web search", "mcp:files:read", "读取文件", "Read\n"}
	esc := func(s string) string { return strings.TrimSuffix(strings.TrimPrefix(string(mustJSON(s)), `"`), `"`) }

	// Claude Code's compaction: the summary asked for over a history of
	// calls to tools no longer declared, with none offered
	var blocks, results []string
	for i, b := range bad {
		blocks = append(blocks, fmt.Sprintf(`{"type":"tool_use","id":"toolu_%d","name":"%s","input":{}}`, i, esc(b)))
		results = append(results, fmt.Sprintf(`{"type":"tool_result","tool_use_id":"toolu_%d","content":"<tool_use_error>Error: No such tool available: %s</tool_use_error>","is_error":true}`, i, esc(b)))
	}
	compaction := `{"model":"codex/gpt-5.5","max_tokens":2000,"stream":true,"messages":[
	  {"role":"user","content":"look it up"},
	  {"role":"assistant","content":[` + strings.Join(blocks, ",") + `]},
	  {"role":"user","content":[` + strings.Join(results, ",") + `]},
	  {"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`
	code, out := post(t, "/v1/messages", compaction)
	if code != 200 || !strings.Contains(out, "Summary: did X.") {
		t.Fatalf("compaction: %d %s\nthe backend got %s", code, out, n.last(t))
	}
	first := n.last(t)
	if msg := namesRefused(first); msg != "" {
		t.Fatalf("compaction: %s in %s", msg, first)
	}
	if code, _ := post(t, "/v1/messages", compaction); code != 200 || string(n.last(t)) != string(first) {
		t.Errorf("the same compaction went differently the second time:\n%s\n%s", first, n.last(t))
	}
	var sent []string
	for _, it := range gjson.GetBytes(first, `input.#(type=="function_call")#.name`).Array() {
		sent = append(sent, it.String())
	}
	if len(sent) != len(bad) || sent[0] == sent[1] {
		t.Errorf("calls named %q", sent)
	}

	// a Chat client that declares a tool under such a name: the backend
	// is given one it takes, and the client is called back by its own
	code, out = post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":true,"messages":[{"role":"user","content":"find x"}],
	  "tools":[{"type":"function","function":{"name":"search:web","parameters":{"type":"object"}}}]}`)
	if code != 200 || !strings.Contains(out, `"name":"search:web"`) {
		t.Errorf("chat: %d %s\nthe backend got %s", code, out, n.last(t))
	}

	// a Responses client, relayed: the history's call and the declared
	// tool go as names the backend takes, the call back by the agent's
	code, out = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"find x"}]},
	  {"type":"function_call","call_id":"call_1","name":"web search","arguments":"{}"},
	  {"type":"function_call_output","call_id":"call_1","output":"no such tool"}],
	  "tools":[{"type":"function","name":"search:web","parameters":{"type":"object"}}]}`)
	if code != 200 || !strings.Contains(out, `"name":"search:web"`) || namesRefused(n.last(t)) != "" {
		t.Errorf("responses: %d %s\nthe backend got %s", code, out, n.last(t))
	}

	// Codex's own turn on its sign-in, a call another provider's model
	// wrote in its history
	code, out = codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]},
	  {"type":"function_call","call_id":"call_1","name":"mcp:files:read","arguments":"{}"},
	  {"type":"function_call_output","call_id":"call_1","output":"unsupported call"}],
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`)
	if code != 200 || namesRefused(n.last(t)) != "" {
		t.Errorf("codex: %d %s\nthe backend got %s", code, out, n.last(t))
	}
}

// A name the backend takes goes as it is, and a conversation of them
// goes byte for byte.
func TestCodexToolNamesKeepValid(t *testing.T) {
	for _, name := range []string{"Read", "mcp__claude_ai_Gmail__search", "functions.exec_command", "a-b_c.d"} {
		if got := provider.CodexToolName(name); got != name {
			t.Errorf("%q became %q", name, got)
		}
	}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"function_call","call_id":"c","name":"Read","arguments":"{}"}],"tools":[{"type":"function","name":"Read"}]}`)
	if got := provider.CodexToolNames(body); string(got) != string(body) {
		t.Errorf("changed: %s", got)
	}
	long := strings.Repeat("工具", 40)
	if got := provider.CodexToolName(long); !backendNameOK.MatchString(got) || len(got) > 64 || got != provider.CodexToolName(long) {
		t.Errorf("long name became %q", got)
	}
}
