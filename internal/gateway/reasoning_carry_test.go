package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// grokReasonedCall is a real Grok reply (Grok Build 1.0.67's session title
// request, grok-4.6-build): a sealed reasoning item, a message, and a
// function call that ends before the message does.
const grokReasonedCallFile = "testdata/grok_reasoning_then_call.sse"

const grokReasonedCallID = "call-b35b8754-d11f-46c5-9c64-fed82640a7f7-0"

// grokReasoningItem is the sealed reasoning item of grokReasonedCallFile,
// as its output_item.done gives it.
func grokReasoningItem(t *testing.T, stream []byte) map[string]any {
	t.Helper()
	for _, ev := range sseData(t, stream) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		if it := ev["item"].(map[string]any); it["type"] == "reasoning" {
			if it["encrypted_content"] == "" || it["encrypted_content"] == nil {
				t.Fatal("the fixture's reasoning isn't sealed")
			}
			return it
		}
	}
	t.Fatal("no reasoning item in the fixture")
	return nil
}

// grokCallUp answers a request with no tool output with the real Grok
// reply, and one with an output with a line of text, keeping each body.
type grokCallUp struct {
	mu    sync.Mutex
	sent  []map[string]any
	seen  []string // what each request was and when, for a failure (#6154)
	reply []byte
}

func (u *grokCallUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.sent = append(u.sent, q)
	u.seen = append(u.seen, fmt.Sprintf("%s %s %dB tool-output=%v %s", r.Method, r.URL.Path, len(b), bytes.Contains(b, []byte(`"function_call_output"`)), time.Now().Format("15:04:05.000")))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if !bytes.Contains(b, []byte(`"function_call_output"`)) {
		w.Write(u.reply)
		return
	}
	done := `{"id":"r2","object":"response","status":"completed","output":[{"type":"message","id":"m2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Titled.","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":1}}`
	io.WriteString(w, sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r2","object":"response","status":"in_progress","output":[]}}`,
		`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m2","role":"assistant","status":"in_progress","content":[]}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","item_id":"m2","output_index":0,"content_index":0,"delta":"Titled."}`,
		`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Titled.","annotations":[]}]}}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`))
}

// askedCallID is the id of the one call a client of from was given in
// body, streamed or whole.
func askedCallID(t *testing.T, from provider.Protocol, body []byte, stream bool) string {
	t.Helper()
	var ids []string
	switch {
	case from == provider.Anthropic && stream:
		for _, ev := range sseData(t, body) {
			if cb, ok := ev["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
				ids = append(ids, cb["id"].(string))
			}
		}
	case from == provider.Anthropic:
		var m struct {
			Content []struct{ Type, ID string }
		}
		json.Unmarshal(body, &m)
		for _, c := range m.Content {
			if c.Type == "tool_use" {
				ids = append(ids, c.ID)
			}
		}
	case stream:
		for _, ev := range sseData(t, body) {
			for _, c := range ev["choices"].([]any) {
				d, _ := c.(map[string]any)["delta"].(map[string]any)
				tcs, _ := d["tool_calls"].([]any)
				for _, tc := range tcs {
					if id, _ := tc.(map[string]any)["id"].(string); id != "" {
						ids = append(ids, id)
					}
				}
			}
		}
	default:
		var m struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct{ ID string } `json:"tool_calls"`
				}
			}
		}
		json.Unmarshal(body, &m)
		for _, c := range m.Choices {
			for _, tc := range c.Message.ToolCalls {
				ids = append(ids, tc.ID)
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("%s client was given calls %v: %s", from, ids, body)
	}
	return ids[0]
}

// grokAsk is a request of from's API as Claude Desktop (Messages, thinking
// on) or a Chat client (reasoning_effort) sends one to a Grok model, with
// the call it was given answered when id isn't "".
func grokAsk(from provider.Protocol, model, id string, stream bool) string {
	s := "false"
	if stream {
		s = "true"
	}
	tool := `{"name":"session_title","description":"Generate the session_title","input_schema":{"type":"object","properties":{"session_title":{"type":"string"}},"required":["session_title"]}}`
	if from == provider.Anthropic {
		msgs := `{"role":"user","content":"Sum a.txt and b.txt into c.txt"}`
		if id != "" {
			msgs += `,{"role":"assistant","content":[{"type":"text","text":"I'll create a short, descriptive session title from that request."},{"type":"tool_use","id":"` + id + `","name":"session_title","input":{"session_title":"Sum a.txt and b.txt numbers into c.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}`
		}
		return `{"model":"` + model + `","max_tokens":32000,"stream":` + s + `,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"tools":[` + tool + `],"messages":[` + msgs + `]}`
	}
	msgs := `{"role":"user","content":"Sum a.txt and b.txt into c.txt"}`
	if id != "" {
		msgs += `,{"role":"assistant","content":"I'll create a short, descriptive session title from that request.","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"session_title","arguments":"{\"session_title\":\"Sum a.txt and b.txt numbers into c.txt\"}"}}]},` +
			`{"role":"tool","tool_call_id":"` + id + `","content":"ok"}`
	}
	return `{"model":"` + model + `","stream":` + s + `,"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"session_title","description":"Generate the session_title","parameters":{"type":"object","properties":{"session_title":{"type":"string"}},"required":["session_title"]}}}],"messages":[` + msgs + `]}`
}

// Grok through Claude Desktop, or a Chat client, gets its reasoning back
// on each step of a tool loop, as Grok Build sends it (jenryai on X): the
// first request asks for it sealed, the call the client is given carries
// it, and the next request has the reasoning item, as Grok wrote it,
// before the call, under the call's own id. Streamed or whole.
func TestGrokReasoningGoesBackThroughOtherAPIs(t *testing.T) {
	reply, err := os.ReadFile(grokReasonedCallFile)
	if err != nil {
		t.Fatal(err)
	}
	item := grokReasoningItem(t, reply)
	up := &grokCallUp{reply: reply}
	pid := besideFake(t, "grok", up)
	paths := map[provider.Protocol]string{provider.Anthropic: "/v1/messages", provider.Chat: "/v1/chat/completions"}
	for _, from := range []provider.Protocol{provider.Anthropic, provider.Chat} {
		for _, stream := range []bool{true, false} {
			up.mu.Lock()
			up.sent, up.seen = nil, nil
			up.mu.Unlock()
			ask := func(id string) []byte {
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", paths[from], strings.NewReader(grokAsk(from, pid+"/fake-resp", id, stream))))
				if rec.Code != 200 {
					t.Fatalf("%s stream=%v: %d %s", from, stream, rec.Code, rec.Body)
				}
				return rec.Body.Bytes()
			}
			id := askedCallID(t, from, ask(""), stream)
			if own, items := unreasonedID(id); own != grokReasonedCallID || len(items) != 1 {
				t.Fatalf("%s stream=%v: the call's id %q carries %d items for %q", from, stream, id, len(items), own)
			}
			ask(id)
			up.mu.Lock()
			sent, seen := up.sent, up.seen
			up.mu.Unlock()
			if len(sent) != 2 {
				t.Fatalf("%s stream=%v: Grok was asked %d times:\n%s", from, stream, len(sent), strings.Join(seen, "\n"))
			}
			inc, _ := json.Marshal(sent[0]["include"])
			if !strings.Contains(string(inc), `"reasoning.encrypted_content"`) {
				t.Fatalf("%s stream=%v: Grok wasn't asked for its sealed reasoning: include %s", from, stream, inc)
			}
			var kinds []string
			var got map[string]any
			for _, it := range sent[1]["input"].([]any) {
				m := it.(map[string]any)
				k, _ := m["type"].(string)
				if k == "" {
					k = "message"
				}
				if k == "message" {
					k += ":" + m["role"].(string)
				}
				if c, _ := m["call_id"].(string); c != "" {
					if c != grokReasonedCallID {
						t.Fatalf("%s stream=%v: %s went to Grok as %q", from, stream, k, c)
					}
				}
				if k == "reasoning" {
					got = m
				}
				kinds = append(kinds, k)
			}
			if want := "message:user,reasoning,message:assistant,function_call,function_call_output"; strings.Join(kinds, ",") != want {
				t.Fatalf("%s stream=%v: Grok was given %v, want %s", from, stream, kinds, want)
			}
			if got["encrypted_content"] != item["encrypted_content"] || got["id"] != item["id"] {
				t.Fatalf("%s stream=%v: the reasoning went back as %v", from, stream, got)
			}
		}
	}
}

// Only Grok's plugin is asked for sealed reasoning by a client of another
// API, and a Responses client keeps its own include and its calls' ids.
func TestSealedReasoningAskedOfGrokAlone(t *testing.T) {
	grok := provider.PluginForTest("grok", "grok")
	other := provider.PluginForTest("kiro", "kiro")
	for _, c := range []struct {
		p    provider.Provider
		from provider.Protocol
		want bool
	}{
		{grok, provider.Anthropic, true},
		{grok, provider.Chat, true},
		{grok, provider.Gemini, true},
		{grok, provider.Responses, false},
		{other, provider.Anthropic, false},
		{provider.Provider{ID: "openai"}, provider.Anthropic, false},
	} {
		r := &Request{}
		askSealedReasoning(c.p, c.from, r)
		if got := len(r.Include) == 1; got != c.want {
			t.Errorf("%s from %s: include %v", c.p.ID, c.from, r.Include)
		}
	}
	reply, err := os.ReadFile(grokReasonedCallFile)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	dec := carryReasoning(decoder(provider.Responses), provider.Responses, provider.Responses)
	for _, l := range strings.Split(string(reply), "\n") {
		if d, ok := strings.CutPrefix(l, "data: "); ok {
			dec(d, func(ev Event) {
				if ev.Kind == KToolStart {
					ids = append(ids, ev.ID)
				}
			})
		}
	}
	if len(ids) != 1 || ids[0] != grokReasonedCallID {
		t.Fatalf("a Responses client was given %v", ids)
	}
}

// An id whose kept reasoning magpie no longer has goes back as the call's
// own, with no reasoning; one a client cut short, or any other id, goes as
// it came.
func TestReasonedIDWithoutItsReasoning(t *testing.T) {
	fresh(t)
	id := reasonedID("call_1", []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"e"}`)})
	if own, items := unreasonedID(id); own != "call_1" || len(items) != 1 {
		t.Fatalf("%q: %q %v", id, own, items)
	}
	if own, items := unreasonedID(id[:len(id)-1]); own != id[:len(id)-1] || items != nil {
		t.Fatalf("cut short: %q %v", own, items)
	}
	os.RemoveAll(sigDir())
	if own, items := unreasonedID(id); own != "call_1" || items != nil {
		t.Fatalf("forgotten: %q %v", own, items)
	}
	for _, plain := range []string{"toolu_01", grokReasonedCallID, "call__rk16__"} {
		if own, items := unreasonedID(plain); own != plain || items != nil {
			t.Fatalf("%q came back %q %v", plain, own, items)
		}
	}
	r := &Request{Messages: []Message{
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "hi"}, {Kind: ToolCall, ID: id, Name: "f"}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: id, Text: "ok"}}},
	}}
	unreasonCalls(r)
	if p := r.Messages[0].Parts; len(p) != 2 || p[1].ID != "call_1" || r.Messages[1].Parts[0].CallID != "call_1" {
		t.Fatalf("forgotten reasoning: %+v", r.Messages)
	}
}
