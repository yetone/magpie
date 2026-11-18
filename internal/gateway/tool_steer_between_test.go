package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The reporter's turn (wizcheu on X, deepseek-flash at max effort through
// Claude Code): a reply of thinking, text and a call, the call's block
// the third (messages.N.2), DeepSeek's own call id.
const (
	steerCallID = "call_00_jdrDF4PKrnFwMUMBRQlr4516"
	steerAsst   = `{"role":"assistant","content":[{"type":"thinking","thinking":"I will list the files.","signature":""},{"type":"text","text":"Let me look."},{"type":"tool_use","id":"` + steerCallID + `","name":"Bash","input":{"command":"ls"}}]}`
	steerResult = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + steerCallID + `","content":"a.go"}]}`
)

// steerBetween is each {role: "system"} message Claude Code puts in the
// conversation, as it stood between a call and its result.
var steerBetween = map[string]string{
	"a steer":                   `{"role":"system","content":[{"type":"text","text":"The user sent a new message while you were working:\nhi"}]}`,
	"a turn's effort":           `{"role":"system","content":[],"output_config":{"effort":"max"}}`,
	"an effort with no content": `{"role":"system","output_config":{"effort":"max"}}`,
	"a deferred tool":           `{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"Read"}}]}`,
	"words":                     `{"role":"system","content":"note"}`,
}

// deepseekPairing is what api.deepseek.com/anthropic holds a request to,
// checked against it on 2026-11-18 (a system message between a call and
// its result: 400; after the result: 200) and 2026-11-19 (a text block
// after a call in its message: 400 messages.N.K, K the text's block;
// thinking after it: 200): nothing but thinking or calls follows a call
// in its message, the message right after a tool_use's is a user message
// whose results answer every call, and a system message holds no
// tool_result. "" when the request passes.
func deepseekPairing(body []byte) string {
	var q struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &q) != nil {
		return "bad body"
	}
	blocks := func(c json.RawMessage) []map[string]any {
		var bs []map[string]any
		_ = json.Unmarshal(c, &bs)
		return bs
	}
	for i, m := range q.Messages {
		if m.Role == "system" {
			for _, b := range blocks(m.Content) {
				if b["type"] == "tool_result" {
					return fmt.Sprintf("messages.%d: a system message holds text only", i)
				}
			}
		}
		if m.Role != "assistant" {
			continue
		}
		var ids []string
		for j, b := range blocks(m.Content) {
			if b["type"] == "tool_use" {
				ids = append(ids, fmt.Sprintf("%d:%v", j, b["id"]))
				continue
			}
			if len(ids) > 0 && b["type"] != "thinking" && b["type"] != "redacted_thinking" {
				// words after a call (checked 2026-11-19): K is their block
				var calls []string
				for _, id := range ids {
					_, call, _ := strings.Cut(id, ":")
					calls = append(calls, call)
				}
				return fmt.Sprintf("messages.%d.%d: `tool_use` ids were found without `tool_result` blocks immediately after: %s", i, j, strings.Join(calls, ", "))
			}
		}
		if len(ids) == 0 {
			continue
		}
		answered := map[string]bool{}
		if i+1 < len(q.Messages) && q.Messages[i+1].Role == "user" {
			for _, b := range blocks(q.Messages[i+1].Content) {
				if b["type"] == "tool_result" {
					answered[fmt.Sprint(b["tool_use_id"])] = true
				}
			}
		}
		for _, id := range ids {
			n, call, _ := strings.Cut(id, ":")
			if !answered[call] {
				return fmt.Sprintf("messages.%d.%s: `tool_use` ids were found without `tool_result` blocks immediately after: %s", i, n, call)
			}
		}
	}
	return ""
}

// strictDeepSeek is a DeepSeek Anthropic endpoint refusing as deepseekPairing
// does; it keeps each body it was sent.
func strictDeepSeek(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if why := deepseekPairing(b); why != "" {
			w.WriteHeader(400)
			msg, _ := json.Marshal(why)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":%s}}`, msg)
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-flash","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// A Claude Code request whose {role: "system"} message stands between a
// call and its result reaches DeepSeek with the message after the result,
// the call answered by its real result, never a made-up one: magpie read
// the system message as the turn after the call, gave the call a
// synthetic result there and the real one as text, and DeepSeek refused
// every later request of the conversation, "400 due to tool use
// concurrency issues" (wizcheu on X).
func TestSystemMessageBetweenCallAndResult(t *testing.T) {
	for name, between := range steerBetween {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			srv, sent := strictDeepSeek(t)
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Anthropic: srv.URL, Models: []string{"deepseek-flash"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"deepseek/deepseek-flash","max_tokens":32,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"list the files"},` +
				steerAsst + `,` + between + `,` + steerResult + `,{"role":"user","content":"go on"}]}`
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			got := sent()
			if len(got) != 1 {
				t.Fatalf("sent %d requests, want 1", len(got))
			}
			if strings.Contains(got[0], interruptedTool) {
				t.Fatalf("the call got a made-up result, though its own came:\n%s", got[0])
			}
			if !strings.Contains(got[0], `"tool_use_id":"`+steerCallID+`","content":"a.go"`) {
				t.Fatalf("the call's own result is gone:\n%s", got[0])
			}
		})
	}
}

// pairedMessages keeps the system message whole, after the results, and
// a body whose system message follows the results comes back as it was.
func TestSystemMessageFollowsTheResults(t *testing.T) {
	for name, between := range steerBetween {
		t.Run(name, func(t *testing.T) {
			msgs := `[` + steerAsst + `,` + between + `,` + steerResult + `]`
			got, changed := pairedMessages([]byte(msgs))
			want := `[` + steerAsst + `,` + steerResult + `,` + between + `]`
			if !changed || string(got) != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
			after := `{"model":"m","messages":[` + steerAsst + `,` + steerResult + `,` + between + `]}`
			if out := toolBlocksPaired([]byte(after)); string(out) != after {
				t.Fatalf("a paired body changed:\n%s", out)
			}
		})
	}
	// no result came: the synthetic one goes in a user message of its
	// own, the system message after it
	msgs := `[` + steerAsst + `,` + steerBetween["a steer"] + `,{"role":"assistant","content":[{"type":"text","text":"done"}]}]`
	got, _ := pairedMessages([]byte(msgs))
	var out []map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range out {
		roles = append(roles, fmt.Sprint(m["role"]))
	}
	if strings.Join(roles, ",") != "assistant,user,system,assistant" {
		t.Fatalf("roles %v: %s", roles, got)
	}
}

// A Chat request relayed to DeepSeek's Chat API with a system message
// between the calls and their tool messages: the system message goes
// after them, and the tool messages stay tool messages.
func TestChatSystemMessageBetweenCallAndResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "list the files"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": steerCallID, "type": "function", "function": map[string]any{"name": "Bash", "arguments": `{"command":"ls"}`}}}},
		{"role": "system", "content": "The user sent a new message while you were working:\nhi"},
		{"role": "tool", "tool_call_id": steerCallID, "content": "a.go"},
		{"role": "user", "content": "go on"},
	}
	out := pairToolMessages(msgs)
	var roles []string
	for _, m := range out {
		roles = append(roles, fmt.Sprint(m["role"]))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,system,user" {
		t.Fatalf("roles %v", roles)
	}
	if out[2]["content"] != "a.go" {
		t.Fatalf("the call's own result is gone: %v", out[2])
	}
	body := `{"model":"deepseek-flash","messages":[{"role":"user","content":"list the files"},{"role":"assistant","content":"","tool_calls":[{"id":"` + steerCallID + `","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"system","content":"note"},{"role":"tool","tool_call_id":"` + steerCallID + `","content":"a.go"}]}`
	var q struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(toolPairsOut([]byte(body)), &q); err != nil {
		t.Fatal(err)
	}
	roles = nil
	for _, m := range q.Messages {
		roles = append(roles, fmt.Sprint(m["role"]))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,system" || q.Messages[2]["content"] != "a.go" {
		t.Fatalf("relayed %v", q.Messages)
	}
}
