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

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// citricdu on Discord (magpie 0.1.3356): Claude Code auto-compacted a
// session on a Smart group that had moved to Cursor's Grok 4.7, then the
// group went back to Kiro's Claude Opus 5.5. The compacted history kept a
// thinking block Grok made, which magpie had handed Claude Code signed
// empty, and Kiro turned the session away 13 times in a row with a bare
// invalid_request_error. The same request without the block was answered.

// grokThought is the shape of the reasoning Grok gave in the report: its
// own words, not Claude's, in a tool turn.
const grokThought = "The user wants the split logic checked. I should read internal/split.go first, then run the tests to see which case fails."

// unsignedConversation is the reporter's request after the compaction:
// the summary Claude Code continues from, then the tool loop it was in,
// whose assistant turns open with Grok's thinking signed "" before the
// calls, and the results after them.
func unsignedConversation(model string) string {
	return `{"model":"` + model + `","max_tokens":32000,"stream":false,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}},{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\nSummary: the user asked to fix the split of long requests."}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":` + jsonString(grokThought) + `,"signature":""},{"type":"text","text":"Reading the split code."},{"type":"tool_use","id":"toolu_01Grok1","name":"Read","input":{"file_path":"internal/split.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01Grok1","content":"package split"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"Now run the tests.","signature":""},{"type":"tool_use","id":"toolu_01Grok2","name":"Bash","input":{"command":"go test ./internal/split"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01Grok2","content":"ok"}]}]}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// kiroClaude answers as the reporter's Kiro did: a thinking block with no
// signature is turned away with Anthropic's error type and words that
// name nothing (so refuseBoundThinking can't mend it); anything else is
// answered as boundClaude answers.
type kiroClaude struct{ boundClaude }

func (c *kiroClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	for _, m := range gjson.GetBytes(b, "messages").Array() {
		for _, blk := range m.Get("content").Array() {
			if blk.Get("type").String() == "thinking" && blk.Get("signature").String() == "" {
				c.mu.Lock()
				c.bodies = append(c.bodies, string(b))
				c.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Improperly formed request."}}`)
				return
			}
		}
	}
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	c.boundClaude.ServeHTTP(w, r)
}

// Every route to Claude leaves the empty-signed thinking out, and keeps
// the turns' text, calls and results: relayed to a Claude API, and
// through Kiro's plugin's fetch.
func TestUnsignedThinkingNotSentToClaude(t *testing.T) {
	check := func(t *testing.T, sent []string, code int, said string) {
		t.Helper()
		if code != 200 {
			t.Fatalf("answered %d %s, want 200", code, said)
		}
		if len(sent) != 1 {
			t.Fatalf("Claude asked %d times, want once", len(sent))
		}
		b := sent[0]
		if strings.Contains(b, "split logic") || strings.Contains(b, "Now run the tests.") || len(signatures(b)) != 0 {
			t.Errorf("unsigned thinking went to Claude: %s", gjson.Get(b, "messages").Raw)
		}
		for _, want := range []string{`"Reading the split code."`, `"toolu_01Grok1"`, `"toolu_01Grok2"`, `"package split"`, `"go test ./internal/split"`} {
			if strings.Count(b, want) == 0 {
				t.Errorf("%s left out: %s", want, gjson.Get(b, "messages").Raw)
			}
		}
		if n := len(gjson.Get(b, "messages").Array()); n != 5 {
			t.Errorf("%d messages sent, want the 5 the agent sent", n)
		}
	}
	t.Run("relayed", func(t *testing.T) {
		fresh(t)
		up := &kiroClaude{}
		srv := httptest.NewServer(up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: "kiro-relay", Name: "Kiro relay", Key: "k", Anthropic: srv.URL, Models: []string{"claude-opus-5-5"}}); err != nil {
			t.Fatal(err)
		}
		code, said := claudeCodeMessages(t, New(), unsignedConversation("kiro-relay/claude-opus-5-5"))
		check(t, up.sent(), code, said)
	})
	t.Run("plugin", func(t *testing.T) {
		up := &kiroClaude{boundClaude{stream: true}}
		t.Setenv("FAKE_KIRO", "1")
		pid := besideFake(t, "kiro", up)
		code, said := claudeCodeMessages(t, New(), unsignedConversation(pid+"/claude-opus-5.5"))
		check(t, up.sent(), code, said)
	})
}

func claudeCodeMessages(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "claude-cli/2.1.120 (external, cli)")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// switchGrok answers as a Chat vendor serving Grok does: its reasoning,
// then a tool call, or text once the call has its result.
type switchGrok struct {
	mu sync.Mutex
	n  int
}

func (g *switchGrok) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.n++
	n := g.n
	g.mu.Unlock()
	ms := gjson.GetBytes(b, "messages").Array()
	done := len(ms) > 0 && ms[len(ms)-1].Get("role").String() == "tool"
	msg := map[string]any{"role": "assistant", "reasoning_content": grokThought}
	finish := "tool_calls"
	if done {
		msg["content"], finish = "Read it.", "stop"
	} else {
		msg["content"] = ""
		msg["tool_calls"] = []any{map[string]any{"id": fmt.Sprintf("call_grok%d", n), "type": "function", "function": map[string]any{"name": "Read", "arguments": fmt.Sprintf(`{"file_path":"f%d.go"}`, n)}}}
	}
	if !gjson.GetBytes(b, "stream").Bool() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "c1", "object": "chat.completion", "model": "grok-4.7", "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta map[string]any, finish any) {
		c, _ := json.Marshal(map[string]any{"id": "c1", "object": "chat.completion.chunk", "model": "grok-4.7", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	chunk(map[string]any{"role": "assistant", "reasoning_content": grokThought}, nil)
	if done {
		chunk(map[string]any{"content": "Read it."}, nil)
	} else {
		calls := msg["tool_calls"].([]any)
		c := calls[0].(map[string]any)
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": c["id"], "type": "function", "function": c["function"]}}}, nil)
	}
	chunk(map[string]any{}, finish)
	io.WriteString(w, "data: [DONE]\n\n")
}

// The reporter's whole path: a group's turns on Grok (a Chat vendor,
// whose reasoning Claude Code gets as thinking signed empty), then the
// group moves to Claude in the middle of a tool loop. Claude, which
// turns a block it didn't sign away, is never sent Grok's.
func TestGrokThinkingThenClaude(t *testing.T) {
	fresh(t)
	gk, cl := &switchGrok{}, &switchClaude{}
	gku, clu := httptest.NewServer(gk), httptest.NewServer(cl)
	t.Cleanup(gku.Close)
	t.Cleanup(clu.Close)
	for _, p := range []provider.Provider{
		{ID: "grokco", Name: "Grok", Key: "sk-gk", Chat: gku.URL + "/v1", Models: []string{"grok-4.7"}},
		{ID: "claude-api", Name: "Claude API", Key: "sk-ant", Anthropic: clu.URL, Models: []string{"claude-opus-5-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	g := provider.Group{ID: "dc", Name: "Smart split", Members: []string{"grokco/grok-4.7", "claude-api/claude-opus-5-5"}}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	s := New()
	msgs := []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"check the split"}]}`)}
	var code int
	var said string
	if msgs, code, said = switchTurn(t, s, false, msgs); code != 200 {
		t.Fatalf("Grok's turn: %d %s", code, said)
	}
	last := msgs[len(msgs)-1]
	th := gjson.GetBytes(last, `content.#(type=="thinking")`)
	if th.Get("thinking").String() != grokThought || !th.Get("signature").Exists() || th.Get("signature").String() != "" {
		t.Fatalf("Grok's reply = %s, want its thinking signed empty", last)
	}
	id := gjson.GetBytes(last, `content.#(type=="tool_use").id`).String()
	msgs = append(msgs, json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+id+`","content":"package f"}]}`))
	// Grok unticked: the tool loop goes on on Claude
	g.Off = []string{"grokco/grok-4.7"}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if _, code, said = switchTurn(t, s, false, msgs); code != 200 {
		t.Fatalf("Claude's turn: %d %s", code, said)
	}
	if cl.refused != 0 || len(cl.bodies) != 1 {
		t.Fatalf("Claude refused %d of %d requests: %s", cl.refused, len(cl.bodies), cl.bodies[0])
	}
	sent := cl.bodies[0]
	if strings.Contains(sent, "split logic") {
		t.Errorf("Grok's thinking went to Claude: %s", sent)
	}
	if !strings.Contains(sent, id) || !strings.Contains(sent, `"package f"`) {
		t.Errorf("Grok's call or its result left out: %s", sent)
	}
}
