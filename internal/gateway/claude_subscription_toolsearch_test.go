package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code with ENABLE_TOOL_SEARCH loads a deferred tool by a ToolSearch
// whose result comes back with the tool now offered. The run waiting on the
// ToolSearch call is handed the tool with the result, and goes on; it isn't
// replaced by one told the conversation in one message, its tool calls as
// text, from which the model went on to write its own calls as text.
func TestToolSearchLoadKeepsTheRun(t *testing.T) {
	s := New()
	b := s.subscription
	run := &subscriptionRun{bridge: b, token: "tok", pending: map[string]chan mcpToolResult{}, tools: map[string]bool{"ToolSearch": true}}
	b.runs["tok"] = run
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cb/{token}", b.mcpCall)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	answer := make(chan mcpToolResult, 1)
	go func() {
		res, err := http.Post(srv.URL+"/cb/tok", "application/json", strings.NewReader(`{"tool_call_id":"toolu_1","name":"ToolSearch","arguments":{"query":"select:WebFetch"}}`))
		if err != nil {
			answer <- mcpToolResult{}
			return
		}
		defer res.Body.Close()
		var r mcpToolResult
		_ = json.NewDecoder(res.Body).Decode(&r)
		answer <- r
	}()
	for deadline := time.Now().Add(2 * time.Second); ; {
		b.mu.Lock()
		n := len(b.calls)
		b.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ToolSearch call was not registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[
		{"name":"ToolSearch","input_schema":{"type":"object"}},
		{"name":"WebFetch","description":"fetch a page","input_schema":{"type":"object","properties":{"url":{"type":"string"}}}},
		{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"defer_loading":true}],
		"messages":[{"role":"user","content":"read example.com"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ToolSearch","input":{"query":"select:WebFetch"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"WebFetch"}]}]}]}`
	started := false
	start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
		started = true
		return nil, nil, errors.New("a new run was started")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		s.serveSubscription(w, r, provider.Anthropic, "Claude Code", "claude-sonnet-5", []byte(body), &Usage{}, start)
	}()

	var got mcpToolResult
	select {
	case got = <-answer:
	case <-done:
		t.Fatalf("the request ended before the run had its result (started anew: %v)", started)
	case <-time.After(3 * time.Second):
		t.Fatal("the ToolSearch call never had its result")
	}
	var names []string
	for _, tl := range got.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "ToolSearch,WebFetch" {
		t.Errorf("tools handed over = %v", names)
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0]["text"].(string), "WebFetch is loaded") {
		t.Errorf("result = %v", got.Content)
	}
	if !run.offers([]Tool{{Name: "WebFetch"}}) {
		t.Error("the run doesn't know it has WebFetch now")
	}
	run.finish()
	<-done
	if started {
		t.Error("a new run was started for the loaded tool")
	}
}
