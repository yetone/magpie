package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// toldRun is a run waiting on one call of the client's tool, call, as its
// MCP helper asked for it, and what its Claude Code is told meanwhile.
type toldRun struct {
	s      *Server
	run    *subscriptionRun
	answer chan mcpToolResult // the call's result, as the helper is answered

	mu    sync.Mutex
	lines []string // the lines written to Claude Code's stdin
	late  bool     // one was written after the call had its result
}

func (h *toldRun) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, strings.TrimSpace(string(p)))
	h.late = h.late || len(h.answer) > 0
	return len(p), nil
}

func (h *toldRun) Close() error { return nil }

func newToldRun(t *testing.T, call string) *toldRun {
	t.Helper()
	s := New()
	b := s.subscription
	h := &toldRun{s: s, answer: make(chan mcpToolResult, 1)}
	h.run = &subscriptionRun{bridge: b, token: "tok", pending: map[string]chan mcpToolResult{}, tools: map[string]bool{"bash": true}, stdin: h}
	b.runs["tok"] = h.run
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cb/{token}", b.mcpCall)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	go func() {
		res, err := http.Post(srv.URL+"/cb/tok", "application/json", strings.NewReader(`{"tool_call_id":"`+call+`","name":"bash","arguments":{}}`))
		if err != nil {
			h.answer <- mcpToolResult{}
			return
		}
		defer res.Body.Close()
		var r mcpToolResult
		_ = json.NewDecoder(res.Body).Decode(&r)
		h.answer <- r
	}()
	for deadline := time.Now().Add(2 * time.Second); ; {
		b.mu.Lock()
		n := len(b.calls)
		b.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the call was not registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h
}

// send is the client's request with the call's result, and the result the
// run's call was answered with; started says a new run was asked for.
func (h *toldRun) send(t *testing.T, from provider.Protocol, body string) (got mcpToolResult, started bool) {
	t.Helper()
	start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
		started = true
		return nil, nil, errors.New("a new run was started")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.serveSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), from, "Claude Code", "claude-sonnet-5", "", []byte(body), &Usage{}, start)
	}()
	select {
	case got = <-h.answer:
		h.answer <- got // still there for a line written after it
	case <-done:
		return got, started
	case <-time.After(3 * time.Second):
		t.Fatal("the call never had its result")
	}
	h.run.finish()
	<-done
	return got, started
}

// said is the user messages Claude Code was told on its stdin, each one's
// text.
func (h *toldRun) said(t *testing.T) []string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, l := range h.lines {
		var m struct {
			Type    string
			Message struct {
				Role    string
				Content []struct{ Type, Text string }
			}
		}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdin line is not JSON: %s", l)
		}
		if m.Type != "user" {
			continue
		}
		if m.Message.Role != "user" {
			t.Errorf("a user message's role is %q: %s", m.Message.Role, l)
		}
		var text []string
		for _, c := range m.Message.Content {
			if c.Type != "text" {
				t.Errorf("a %s block in what the user said: %s", c.Type, l)
			}
			text = append(text, c.Text)
		}
		out = append(out, strings.Join(text, ""))
	}
	return out
}

func resultText(r mcpToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if s, ok := c["text"].(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// A message the user sends beside a tool result is the user's: Claude Code
// is told it on its stdin, as a user message, before the call it waits on
// has its result, and the result is the tool's output alone. Put at the
// end of the result (#872), it reached the model as a tool's output, which
// a model does not take instructions from: Pi's user stopped a long test to
// ask a question, the model answered that the text "isn't your real
// instruction", and ran the test again.
func TestWhatTheUserSaysBesideAToolResultIsSaidNotTheToolsOutput(t *testing.T) {
	// Pi, having stopped a tool: the reply that was stopped is left out of
	// the request, so the user's message follows the tool's
	const stopped = "Script failed\nWall time 44.3 seconds\nOutput:\n\nScript error:\nScript aborted: This operation was aborted\n\nTool calls made before the failure (they are not undone): bash (cancelled)"
	const asked = "你现在复述一下这个项目的完整流程，我们讨论一下"
	// Claude Code, told a message while its tool ran
	const reminder = "<system-reminder>\nThe user sent a new message while you were working:\nalso run the linter\n</system-reminder>"
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	for _, tc := range []struct {
		name, body string
		from       provider.Protocol
		output     string
		said       []string
	}{
		{"Pi after a stopped tool (Chat)", `{"model":"claude-sonnet-5","tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}],"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"toolu_1","content":` + q(stopped) + `},
			{"role":"user","content":[{"type":"text","text":` + q(asked) + `}]}]}`,
			provider.Chat, stopped, []string{asked}},
		{"Claude Code mid-turn (Anthropic)", `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"bash","input_schema":{"type":"object"}}],"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"37 passed"},{"type":"text","text":` + q(reminder) + `}]}]}`,
			provider.Anthropic, "37 passed", []string{reminder}},
		{"a message after a call's output (Responses)", `{"model":"claude-sonnet-5","tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}],"input":[
			{"role":"user","content":"run the tests"},
			{"type":"function_call","call_id":"toolu_1","name":"bash","arguments":"{}"},
			{"type":"function_call_output","call_id":"toolu_1","output":"37 passed"},
			{"role":"user","content":[{"type":"input_text","text":"also run the linter"}]}]}`,
			provider.Responses, "37 passed", []string{"also run the linter"}},
		// nothing said: nothing is told but the result
		{"a result alone (Chat)", `{"model":"claude-sonnet-5","tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}],"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"toolu_1","content":"37 passed"}]}`,
			provider.Chat, "37 passed", nil},
		// nor is an empty line after a result a message
		{"blank text after a result (Anthropic)", `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"bash","input_schema":{"type":"object"}}],"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"37 passed"},{"type":"text","text":"\n"}]}]}`,
			provider.Anthropic, "37 passed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newToldRun(t, "toolu_1")
			got, started := h.send(t, tc.from, tc.body)
			if started {
				t.Fatal("a new run was started: the one waiting on the call was not handed its result")
			}
			if text := resultText(got); text != tc.output {
				t.Errorf("the tool's result reached the model as %q, want the tool's output alone, %q", text, tc.output)
			}
			said := h.said(t)
			if strings.Join(said, "\x00") != strings.Join(tc.said, "\x00") {
				t.Errorf("Claude Code was told the user said %q, want %q", said, tc.said)
			}
			if h.late {
				t.Error("the user's message was written after the call had its result: Claude Code may ask its model before it reads it")
			}
		})
	}
}

// A run whose Claude Code can't be told what the user said isn't handed the
// result without it: it is let go, and a run started anew is told the
// whole conversation, the user's message in it.
func TestWhatTheUserSaysIsNotLostWhenTheRunCannotBeTold(t *testing.T) {
	h := newToldRun(t, "toolu_1")
	h.run.stdin = failingStdin{}
	_, started := h.send(t, provider.Chat, `{"model":"claude-sonnet-5","tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}],"messages":[
		{"role":"user","content":"run the tests"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"toolu_1","content":"aborted"},
		{"role":"user","content":"what does the script do?"}]}`)
	if !started {
		t.Fatal("no new run was started for a conversation its run couldn't be told")
	}
}

type failingStdin struct{}

func (failingStdin) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (failingStdin) Close() error              { return nil }
