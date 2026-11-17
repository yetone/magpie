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

// supersedeHarness drives serveSubscription with runs that call a tool or
// say a word, each with Claude Code's open input and a timer as a real
// run has, so park leaves the timer as it was.
type supersedeHarness struct {
	t       *testing.T
	s       *Server
	started []*subscriptionRun
	// what the run started next does: call the tool, by this id, or say
	// a word when ""
	next string
}

func newSupersedeHarness(t *testing.T) *supersedeHarness {
	return &supersedeHarness{t: t, s: New()}
}

func (h *supersedeHarness) calls(run *subscriptionRun, id string) chan mcpToolResult {
	b := h.s.subscription
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

func (h *supersedeHarness) says(run *subscriptionRun, text string) {
	run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
	run.emit(Event{Kind: KText, Text: text})
	run.emit(Event{Kind: KStop, Stop: "stop"})
	run.endSegment()
}

func (h *supersedeHarness) start(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
	b := h.s.subscription
	run := &subscriptionRun{bridge: b, token: "run" + strconv.Itoa(len(h.started)), pending: map[string]chan mcpToolResult{}, stdin: nopStdin{}}
	b.mu.Lock()
	b.runs[run.token] = run
	b.mu.Unlock()
	events := run.attach()
	// as a run started for real: runTimeout, which park leaves as it is
	run.timer = time.AfterFunc(runTimeout, func() { run.abortFor("it waited as long as it may") })
	run.ends = time.Now().Add(runTimeout)
	h.t.Cleanup(func() { run.abort() })
	if h.next != "" {
		h.calls(run, h.next)
	} else {
		h.says(run, "from the conversation as it now is")
	}
	h.started = append(h.started, run)
	return run, events, nil
}

func (h *supersedeHarness) ask(msgs string) string {
	h.t.Helper()
	body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := h.s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, "Claude Code", "claude-sonnet-5", "", []byte(body), &u, h.start); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Type, Text, ID string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 {
		h.t.Fatalf("no answer: %s", rec.Body)
	}
	return res.Content[0].Type + " " + res.Content[0].Text + res.Content[0].ID
}

func closedRun(run *subscriptionRun) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.closed
}

func endsIn(run *subscriptionRun) time.Duration {
	run.mu.Lock()
	defer run.mu.Unlock()
	return time.Until(run.ends)
}

func supUser(text string) string { return `{"role":"user","content":` + strconv.Quote(text) + `}` }
func supCall(id string) string {
	return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`
}
func supResult(id, text string) string {
	return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + strconv.Quote(text) + `}]}`
}

// The client compacted the conversation while a run waited on its tool
// calls, and sends their results after its summary (Kling__Lab on X: Alma's
// autoCompact, Claude Code in its interactive mode): the summary is answered
// by a new run, and the run that made the calls is ended there and then. No
// client will send it results any more: its own client has the
// conversation as the summary. Left waiting, its Claude Code moved the calls
// to the background after 120s and went on working for no one, writing its
// session for 30 to 80 minutes, which the new run's model read as another
// session at the same work.
func TestCompactedConversationEndsTheRunThatWaitedOnItsCalls(t *testing.T) {
	h := newSupersedeHarness(t)
	h.next = "call_1"
	if got := h.ask(`[` + supUser("read a") + `]`); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	h.next = ""
	rewritten := `[` + supUser("a summary of the conversation so far") + `,` + supCall("call_1") + `,` + supResult("call_1", "A") + `]`
	if got := h.ask(rewritten); got != "text from the conversation as it now is" || len(h.started) != 2 {
		t.Fatalf("after the compaction: %q from %d runs", got, len(h.started))
	}
	if !closedRun(h.started[0]) {
		t.Fatalf("the run whose conversation was compacted still waits on its calls, ending in %s", endsIn(h.started[0]).Round(time.Second))
	}
	if closedRun(h.started[1]) {
		t.Fatal("the run the compacted conversation goes on in was ended")
	}
}

// The compacted conversation may not carry the waiting calls' results at
// all: the client dropped the call it was running and went on with the
// next turn, its summary and the recent messages, which hold calls the run
// made and was answered for before. No request finds the run (no run waits
// for this turn), and it is ended all the same when the new run starts.
func TestCompactedConversationWithoutTheResultsEndsTheRun(t *testing.T) {
	h := newSupersedeHarness(t)
	h.next = "call_1"
	if got := h.ask(`[` + supUser("read a, then b") + `]`); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	// its results go on in it, and it calls again
	first := h.started[0]
	conv := supUser("read a, then b") + `,` + supCall("call_1") + `,` + supResult("call_1", "A")
	first.mu.Lock()
	waiter := first.pending["call_1"]
	first.mu.Unlock()
	go func() {
		if _, ok := <-waiter; ok {
			h.calls(first, "call_2")
		}
	}()
	if got := h.ask(`[` + conv + `]`); got != "tool_use call_2" || len(h.started) != 1 {
		t.Fatalf("a conversation that went on: %q from %d runs", got, len(h.started))
	}
	// compacted while call_2 runs: the summary, the recent messages and
	// the user's next words, call_2 dropped
	h.next = ""
	compacted := `[` + supUser("a summary of the conversation so far") + `,` + supCall("call_1") + `,` + supResult("call_1", "A") + `,` + supUser("go on") + `]`
	if got := h.ask(compacted); got != "text from the conversation as it now is" || len(h.started) != 2 {
		t.Fatalf("after the compaction: %q from %d runs", got, len(h.started))
	}
	if !closedRun(first) {
		t.Fatalf("the run whose conversation was compacted still waits on call_2, ending in %s", endsIn(first).Round(time.Second))
	}
}

// A conversation that names none of a waiting run's calls is another
// one: a run of the same account, waiting on its own calls, is left as it
// was, however its conversation began.
func TestAnotherConversationLeavesAWaitingRunAlone(t *testing.T) {
	h := newSupersedeHarness(t)
	h.next = "call_1"
	if got := h.ask(`[` + supUser("read a") + `]`); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	h.next = ""
	if got := h.ask(`[` + supUser("something else") + `]`); got != "text from the conversation as it now is" {
		t.Fatalf("another conversation: %q", got)
	}
	if closedRun(h.started[0]) {
		t.Fatal("a run waiting on its calls was ended by another conversation")
	}
	if d := endsIn(h.started[0]); d < runTimeout-time.Minute {
		t.Fatalf("a run waiting on its calls was cut to %s by another conversation", d.Round(time.Second))
	}
}

// A turn sent again while the run it started waits on its calls (Alma
// sending a sub-agent's Task request again): the new request gets a run of
// its own, as before, and the first one isn't left waiting runTimeout on
// calls whose client gave up on the turn. It isn't ended at once either,
// since two of a client's sub-agents may be asked the very same thing: it
// waits parkLongest, as a run whose caller stopped does, and goes on if
// its results come.
func TestTurnSentAgainBoundsTheRunWaitingOnIt(t *testing.T) {
	h := newSupersedeHarness(t)
	h.next = "call_1"
	task := `[` + supUser("review the change") + `]`
	if got := h.ask(task); got != "tool_use call_1" {
		t.Fatalf("the first turn: %q", got)
	}
	h.next = "call_2"
	if got := h.ask(task); got != "tool_use call_2" || len(h.started) != 2 {
		t.Fatalf("the turn sent again: %q from %d runs", got, len(h.started))
	}
	first := h.started[0]
	if closedRun(first) {
		t.Fatal("the run of a turn sent again was ended at once: it may be a sub-agent asked the same thing")
	}
	if d := endsIn(first); d > parkLongest {
		t.Fatalf("the run of a turn sent again waits %s more on its calls, past parkLongest (%s)", d.Round(time.Second), parkLongest)
	}
	if d := endsIn(h.started[1]); d < runTimeout-time.Minute {
		t.Fatalf("the run the turn went on in waits only %s", d.Round(time.Second))
	}
	// its own results come all the same: it goes on, its wait a turn's
	first.mu.Lock()
	waiter := first.pending["call_1"]
	first.mu.Unlock()
	go func() {
		if _, ok := <-waiter; ok {
			h.says(first, "the first one's answer")
		}
	}()
	h.next = ""
	if got := h.ask(`[` + supUser("review the change") + `,` + supCall("call_1") + `,` + supResult("call_1", "ok") + `]`); got != "text the first one's answer" || len(h.started) != 2 {
		t.Fatalf("the first run's own results: %q from %d runs", got, len(h.started))
	}
	if closedRun(first) {
		t.Fatal("the first run was ended though its results came")
	}
}
