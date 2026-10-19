package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A client that rewrote its conversation between a tool call and its result,
// as Pi does compacting it mid-turn, is answered by a run told the
// conversation as it now is: the run that made the call still holds the one
// from before, and would tell the client a context as large as ever, so the
// client compacted again at each tool call. A conversation that only went on
// keeps its run.
//
// The run that made the calls is not ended by the rewrite: a fork or a
// resumed session of the same conversation answers the very calls it waits
// on (#2933), by their ids, and its request must not end a run whose own
// results are still to come. It waits parkLongest more, as a run whose
// caller stopped does, and is let go if they never come.
func TestRunLetGoWhenTheClientRewroteTheConversation(t *testing.T) {
	s := New()
	b := s.subscription
	// what a run's agent does with a turn: call a tool, or say a word
	calls := func(run *subscriptionRun, id string) chan mcpToolResult {
		waiter := make(chan mcpToolResult, 1)
		run.mu.Lock()
		run.pending[id] = waiter
		run.mu.Unlock()
		b.mu.Lock()
		b.calls[id] = run
		b.mu.Unlock()
		run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
		run.emit(Event{Kind: KToolStart, ID: id, Name: "read"})
		run.emit(Event{Kind: KToolArgs, Text: "{}"})
		run.emit(Event{Kind: KStop, Stop: "tool"})
		run.endSegment()
		return waiter
	}
	says := func(run *subscriptionRun, text string) {
		run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
		run.emit(Event{Kind: KText, Text: text})
		run.emit(Event{Kind: KStop, Stop: "stop"})
		run.endSegment()
	}
	var started []*subscriptionRun
	var told [][]Message
	var first chan mcpToolResult
	second := make(chan chan mcpToolResult, 1)
	start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
		run := &subscriptionRun{bridge: b, token: "run" + strconv.Itoa(len(started)), pending: map[string]chan mcpToolResult{}}
		b.mu.Lock()
		b.runs[run.token] = run
		b.mu.Unlock()
		events := run.attach()
		if len(started) == 0 {
			first = calls(run, "call_1")
		} else {
			says(run, "from the conversation as it now is")
		}
		started = append(started, run)
		told = append(told, req.Messages)
		return run, events, nil
	}
	ask := func(msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, "Claude Code", "claude-sonnet-5", "", []byte(body), &u, start); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var res struct {
			Content []struct{ Type, Text, ID string } `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if len(res.Content) == 0 {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return res.Content[0].Type + " " + res.Content[0].Text + res.Content[0].ID
	}
	user := func(text string) string { return `{"role":"user","content":` + strconv.Quote(text) + `}` }
	call := func(id string) string {
		return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`
	}
	result := func(id, text string) string {
		return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + strconv.Quote(text) + `}]}`
	}

	conv := user("read a, then b")
	if got := ask(`[` + conv + `]`); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	// the conversation goes on: the run that made the call has its result
	go func(run *subscriptionRun) {
		if r, ok := <-first; !ok || r.Content[0]["text"] != "A" {
			t.Errorf("call_1 answered %v, %v", r, ok)
		}
		second <- calls(run, "call_2")
	}(started[0])
	conv += `,` + call("call_1") + `,` + result("call_1", "A")
	if got := ask(`[` + conv + `]`); got != "tool_use call_2" || len(started) != 1 {
		t.Fatalf("a conversation that went on: %q from %d runs", got, len(started))
	}
	// the client compacts, and sends call_2's result after its summary; the
	// rewrite is answered by a run of its own, and the run that made the
	// calls goes on waiting for the results of the client that has not
	// rewritten anything
	go func(run *subscriptionRun, waiter chan mcpToolResult) {
		if r, ok := <-waiter; !ok {
			t.Errorf("the run's call was closed before its own results came")
			return
		} else if r.Content[0]["text"] != "B2" {
			t.Errorf("the run's own results answered %v", r)
		}
		says(run, "from the conversation it was told")
	}(started[0], <-second)
	rewritten := `[` + user("a summary of the conversation so far") + `,` + call("call_2") + `,` + result("call_2", "B") + `]`
	if got := ask(rewritten); got != "text from the conversation as it now is" {
		t.Fatalf("after the rewrite: %q", got)
	}
	if len(started) != 2 || len(told[1]) != 3 || told[1][0].Parts[0].Text != "a summary of the conversation so far" {
		t.Fatalf("%d runs, the last told %+v", len(started), told[len(told)-1])
	}
	started[0].mu.Lock()
	closed := started[0].closed
	started[0].mu.Unlock()
	if closed {
		t.Fatal("the run that made the calls was ended by another conversation's results")
	}
	// its own results, a moment later: they go on in the run that made the
	// calls, which holds the conversation they follow
	conv += `,` + call("call_2") + `,` + result("call_2", "B2")
	if got := ask(`[` + conv + `]`); got != "text from the conversation it was told" || len(started) != 2 {
		t.Fatalf("the run's own results: %q from %d runs", got, len(started))
	}
	started[0].mu.Lock()
	closed = started[0].closed
	started[0].mu.Unlock()
	if closed {
		t.Fatal("the run that made the calls was ended though its results came")
	}
}

// nopStdin stands for Claude Code's open input: a run kept for its
// conversation's next turn, whose park does not reset its timer (parkLongest
// is what released promises, not what park leaves).
type nopStdin struct{}

func (nopStdin) Write(p []byte) (int, error) { return len(p), nil }
func (nopStdin) Close() error                { return nil }

// A run whose conversation was rewritten by another's request goes on
// waiting parkLongest for the results of the client that has not rewritten
// anything, and is let go then: a rewrite leaves no Claude Code waiting an
// hour for results that came by another conversation (#2933).
func TestRewrittenRunWaitsParkLongestForItsOwnResults(t *testing.T) {
	oldPark := parkLongest
	parkLongest = time.Second
	defer func() { parkLongest = oldPark }()

	s := New()
	b := s.subscription
	calls := func(run *subscriptionRun, id string) chan mcpToolResult {
		waiter := make(chan mcpToolResult, 1)
		run.mu.Lock()
		run.pending[id] = waiter
		run.mu.Unlock()
		b.mu.Lock()
		b.calls[id] = run
		b.mu.Unlock()
		run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
		run.emit(Event{Kind: KToolStart, ID: id, Name: "read"})
		run.emit(Event{Kind: KToolArgs, Text: "{}"})
		run.emit(Event{Kind: KStop, Stop: "tool"})
		run.endSegment()
		return waiter
	}
	says := func(run *subscriptionRun, text string) {
		run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
		run.emit(Event{Kind: KText, Text: text})
		run.emit(Event{Kind: KStop, Stop: "stop"})
		run.endSegment()
	}
	var started []*subscriptionRun
	start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
		run := &subscriptionRun{bridge: b, token: "run" + strconv.Itoa(len(started)), pending: map[string]chan mcpToolResult{}, stdin: nopStdin{}}
		b.mu.Lock()
		b.runs[run.token] = run
		b.mu.Unlock()
		events := run.attach()
		run.timer = time.AfterFunc(time.Hour, func() { run.abortFor("it waited as long as it may") })
		if len(started) == 0 {
			calls(run, "call_1")
		} else {
			says(run, "from the conversation as it now is")
		}
		started = append(started, run)
		return run, events, nil
	}
	ask := func(msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, "Claude Code", "claude-sonnet-5", "", []byte(body), &u, start); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var res struct {
			Content []struct{ Type, Text, ID string } `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if len(res.Content) == 0 {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return res.Content[0].Type + " " + res.Content[0].Text + res.Content[0].ID
	}
	user := func(text string) string { return `{"role":"user","content":` + strconv.Quote(text) + `}` }
	call := func(id string) string {
		return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`
	}
	result := func(id, text string) string {
		return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + strconv.Quote(text) + `}]}`
	}

	if got := ask(`[` + user("read a") + `]`); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	// a rewritten request for the same call's results: answered by a run of
	// its own, and the run that made the call is not ended for it
	rewritten := `[` + user("a summary of the conversation so far") + `,` + call("call_1") + `,` + result("call_1", "A") + `]`
	if got := ask(rewritten); got != "text from the conversation as it now is" || len(started) != 2 {
		t.Fatalf("after the rewrite: %q from %d runs", got, len(started))
	}
	started[0].mu.Lock()
	closed := started[0].closed
	started[0].mu.Unlock()
	if closed {
		t.Fatal("the run that made the calls was ended at once by another conversation's results")
	}
	// past parkLongest with no results of its own, it is let go
	deadline := time.Now().Add(10 * time.Second)
	for {
		started[0].mu.Lock()
		closed = started[0].closed
		started[0].mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run whose conversation was rewritten is still waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
