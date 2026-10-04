package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// remapHarness serves requests through serveSubscription with runs whose
// agent the test plays: each reply calls the client's tools or says a word,
// as the rewrite test's do.
type remapHarness struct {
	t       *testing.T
	s       *Server
	b       *subscriptionBridge
	mu      sync.Mutex
	started []*subscriptionRun
	// first is what a new run's agent does with its first turn
	first func(run *subscriptionRun, req *Request)
}

func newRemapHarness(t *testing.T) *remapHarness {
	s := New()
	h := &remapHarness{t: t, s: s, b: s.subscription}
	t.Cleanup(s.subscription.abortAll)
	return h
}

// calls has the run's reply call each of cs, Claude Code's id, the tool and
// its arguments; its agent waits on the first registered of them, as
// Claude Code makes its MCP calls one after another (all of them when all).
func (h *remapHarness) calls(run *subscriptionRun, all bool, cs ...shownCall) []chan mcpToolResult {
	waiters := make([]chan mcpToolResult, len(cs))
	for n, c := range cs {
		if n > 0 && !all {
			break
		}
		waiters[n] = make(chan mcpToolResult, 1)
		run.mu.Lock()
		run.pending[c.id] = waiters[n]
		run.mu.Unlock()
		h.b.mu.Lock()
		h.b.calls[c.id] = run
		h.b.mu.Unlock()
	}
	run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
	for _, c := range cs {
		run.emit(Event{Kind: KToolStart, ID: c.id, Name: c.name})
		run.emit(Event{Kind: KToolArgs, Text: c.args})
	}
	run.emit(Event{Kind: KStop, Stop: "tool"})
	run.endSegment()
	return waiters
}

func (h *remapHarness) says(run *subscriptionRun, text string) {
	run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-sonnet-5"})
	run.emit(Event{Kind: KText, Text: text})
	run.emit(Event{Kind: KStop, Stop: "stop"})
	run.endSegment()
}

func (h *remapHarness) start(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
	h.mu.Lock()
	run := &subscriptionRun{bridge: h.b, token: "run" + strconv.Itoa(len(h.started)), model: "claude-sonnet-5", pending: map[string]chan mcpToolResult{}}
	h.started = append(h.started, run)
	h.mu.Unlock()
	h.b.mu.Lock()
	h.b.runs[run.token] = run
	h.b.mu.Unlock()
	events := run.attach()
	if h.first != nil {
		h.first(run, req)
	} else {
		h.says(run, "a run started anew")
	}
	return run, events, nil
}

func (h *remapHarness) runs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.started)
}

// ask sends msgs and returns the reply's blocks: "tool_use <id>" or
// "text <text>".
func (h *remapHarness) ask(msgs ...string) []string {
	h.t.Helper()
	body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := h.s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, "Claude Code", "claude-sonnet-5", []byte(body), &u, h.start); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Type, Text, ID string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	var out []string
	for _, c := range res.Content {
		out = append(out, c.Type+" "+c.Text+c.ID)
	}
	return out
}

func remapUser(text string) string { return `{"role":"user","content":` + strconv.Quote(text) + `}` }

// remapCalls is an assistant message calling read with each of args, under
// the ids given, "id=args".
func remapCalls(calls ...string) string {
	var blocks []string
	for _, c := range calls {
		id, args, _ := strings.Cut(c, "=")
		blocks = append(blocks, `{"type":"tool_use","id":"`+id+`","name":"read","input":`+args+`}`)
	}
	return `{"role":"assistant","content":[` + strings.Join(blocks, ",") + `]}`
}

// remapResults is a user message of results, "id=text".
func remapResults(results ...string) string {
	var blocks []string
	for _, r := range results {
		id, text, _ := strings.Cut(r, "=")
		blocks = append(blocks, `{"type":"tool_result","tool_use_id":"`+id+`","content":`+strconv.Quote(text)+`}`)
	}
	return `{"role":"user","content":[` + strings.Join(blocks, ",") + `]}`
}

func resultOf(t *testing.T, w chan mcpToolResult) string {
	t.Helper()
	select {
	case r, ok := <-w:
		if !ok {
			return "(run ended)"
		}
		return r.Content[0]["text"].(string)
	case <-time.After(3 * time.Second):
		return "(no result)"
	}
}

// AmpCode names every tool call it was told by an id of its own — the
// tool_use id and its result's tool_use_id, in every request after — so
// none of its results named a call the run's Claude Code waits on: each
// request started a Claude Code anew, told the whole conversation, its
// cache written again (Chapin, magpie 0.1.757). The run whose reply made
// the same calls, in a conversation the request goes on from, has them,
// and goes on in the same Claude Code turn after turn.
func TestRewrittenToolIDsKeepTheRun(t *testing.T) {
	h := newRemapHarness(t)
	var first []chan mcpToolResult
	h.first = func(run *subscriptionRun, _ *Request) {
		first = h.calls(run, false, shownCall{"toolu_01A", "read", `{"path":"a"}`})
	}
	conv := []string{remapUser("read a, then b")}
	if got := h.ask(conv...); len(got) != 1 || got[0] != "tool_use toolu_01A" {
		t.Fatalf("first turn: %v", got)
	}
	run := h.started[0]
	second := make(chan []chan mcpToolResult, 1)
	go func() {
		if got := resultOf(t, first[0]); got != "A" {
			t.Errorf("toolu_01A answered %q", got)
		}
		second <- h.calls(run, false, shownCall{"toolu_01B", "read", `{"path":"b"}`})
	}()
	// the client's own ids, and its arguments spaced its own way
	conv = append(conv, remapCalls(`TU-1={ "path": "a" }`), remapResults("TU-1=A"))
	if got := h.ask(conv...); len(got) != 1 || got[0] != "tool_use toolu_01B" || h.runs() != 1 {
		t.Fatalf("second turn: %v from %d runs", got, h.runs())
	}
	go func(w []chan mcpToolResult) {
		if got := resultOf(t, w[0]); got != "B" {
			t.Errorf("toolu_01B answered %q", got)
		}
		h.says(run, "a and b read")
	}(<-second)
	conv = append(conv, remapCalls(`TU-2={"path":"b"}`), remapResults("TU-2=B"))
	if got := h.ask(conv...); len(got) != 1 || got[0] != "text a and b read" || h.runs() != 1 {
		t.Fatalf("third turn: %v from %d runs", got, h.runs())
	}
}

// A reply's calls run together in the client, and their results come back
// in any order: each goes to the call in its place in the reply — the one
// the agent waits on at once, the other kept for when it makes it — and
// two calls of the same tool with the same arguments each get their own.
func TestRewrittenToolIDsParallelAndRepeated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls []shownCall
		all   bool // the agent waits on every call at once
	}{
		{"parallel", []shownCall{{"toolu_a", "read", `{"path":"a"}`}, {"toolu_b", "read", `{"path":"b"}`}}, false},
		{"repeated", []shownCall{{"toolu_1", "read", `{"path":"a"}`}, {"toolu_2", "read", `{"path":"a"}`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRemapHarness(t)
			var waiters []chan mcpToolResult
			h.first = func(run *subscriptionRun, _ *Request) { waiters = h.calls(run, tc.all, tc.calls...) }
			h.ask(remapUser("read twice"))
			run := h.started[0]
			got := make(chan [2]string, 1)
			go func() {
				var r [2]string
				r[0] = resultOf(t, waiters[0])
				if tc.all {
					r[1] = resultOf(t, waiters[1])
				}
				got <- r
				h.says(run, "done")
			}()
			reply := h.ask(remapUser("read twice"),
				remapCalls("TU-x="+tc.calls[0].args, "TU-y="+tc.calls[1].args),
				remapResults("TU-y=second", "TU-x=first"))
			if len(reply) != 1 || reply[0] != "text done" || h.runs() != 1 {
				t.Fatalf("reply %v from %d runs", reply, h.runs())
			}
			r := <-got
			if r[0] != "first" {
				t.Errorf("%s answered %q", tc.calls[0].id, r[0])
			}
			if tc.all {
				if r[1] != "second" {
					t.Errorf("%s answered %q", tc.calls[1].id, r[1])
				}
				return
			}
			run.mu.Lock()
			early := run.early[tc.calls[1].id]
			run.mu.Unlock()
			if len(early.Content) == 0 || early.Content[0]["text"] != "second" {
				t.Errorf("the call not made yet is kept %v", early)
			}
		})
	}
}

// Two conversations whose replies made the same call, each waiting on its
// own run: a result goes to the run of the conversation it goes on, never
// the other's; and when the two are the same conversation so far, there's
// no telling which, so neither is handed it and a run is started anew.
func TestRewrittenToolIDsNotCrossWired(t *testing.T) {
	h := newRemapHarness(t)
	waiters := map[string]chan mcpToolResult{}
	var mu sync.Mutex
	h.first = func(run *subscriptionRun, req *Request) {
		w := h.calls(run, false, shownCall{"toolu_" + run.token, "read", `{"path":"x"}`})
		mu.Lock()
		waiters[req.Messages[0].Parts[0].Text+" "+run.token] = w[0]
		mu.Unlock()
	}
	h.ask(remapUser("conversation A"))
	h.ask(remapUser("conversation B"))
	runA, runB := h.started[0], h.started[1]
	go func() {
		if got := resultOf(t, waiters["conversation B run1"]); got != "for B" {
			t.Errorf("B's call answered %q", got)
		}
		h.says(runB, "B goes on")
	}()
	if got := h.ask(remapUser("conversation B"), remapCalls(`TU-9={"path":"x"}`), remapResults("TU-9=for B")); len(got) != 1 || got[0] != "text B goes on" || h.runs() != 2 {
		t.Fatalf("B: %v from %d runs", got, h.runs())
	}
	go func() {
		if got := resultOf(t, waiters["conversation A run0"]); got != "for A" {
			t.Errorf("A's call answered %q", got)
		}
		h.says(runA, "A goes on")
	}()
	if got := h.ask(remapUser("conversation A"), remapCalls(`TU-9={"path":"x"}`), remapResults("TU-9=for A")); len(got) != 1 || got[0] != "text A goes on" || h.runs() != 2 {
		t.Fatalf("A: %v from %d runs", got, h.runs())
	}

	// two the same so far
	h.ask(remapUser("conversation C"))
	h.ask(remapUser("conversation C"))
	c1, c2 := waiters["conversation C run2"], waiters["conversation C run3"]
	h.first = nil
	req := &Request{Model: "claude-sonnet-5", Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "conversation C"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "TU-9", Name: "read", Args: json.RawMessage(`{"path":"x"}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "TU-9", Text: "for C"}}},
	}}
	if run, _, why := h.b.match(req); run != nil || why != ambiguousCalls {
		t.Fatalf("two conversations the same so far: %v, %q", run != nil, why)
	}
	if got := h.ask(remapUser("conversation C"), remapCalls(`TU-9={"path":"x"}`), remapResults("TU-9=for C")); len(got) != 1 || got[0] != "text a run started anew" || h.runs() != 5 {
		t.Fatalf("C: %v from %d runs", got, h.runs())
	}
	for _, w := range []chan mcpToolResult{c1, c2} {
		select {
		case r := <-w:
			t.Errorf("a C run was handed %v", r)
		default:
		}
	}
}

// A conversation edited since the run's reply (compacted, rewound), or
// calls that aren't the reply's — another tool, other arguments, one more
// or less — find no run: a new one is told the conversation as it is, and
// the run waiting is handed nothing.
func TestRewrittenToolIDsNeedTheSameConversationAndCalls(t *testing.T) {
	h := newRemapHarness(t)
	var w []chan mcpToolResult
	h.first = func(run *subscriptionRun, _ *Request) {
		w = h.calls(run, false, shownCall{"toolu_1", "read", `{"path":"a"}`})
	}
	h.ask(remapUser("read a"))
	h.first = nil
	for _, tc := range []struct {
		name string
		msgs []string
		why  string
	}{
		{"compacted", []string{remapUser("a summary"), remapCalls(`TU-1={"path":"a"}`), remapResults("TU-1=A")}, historyChanged},
		{"other arguments", []string{remapUser("read a"), remapCalls(`TU-1={"path":"b"}`), remapResults("TU-1=A")}, noRunWaiting},
		{"one more call", []string{remapUser("read a"), remapCalls(`TU-1={"path":"a"}`, `TU-2={"path":"a"}`), remapResults("TU-1=A", "TU-2=A")}, noRunWaiting},
	} {
		body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[` + strings.Join(tc.msgs, ",") + `]}`
		req, err := parse(provider.Anthropic, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Model = "claude-sonnet-5"
		if run, _, why := h.b.match(req); run != nil || why != tc.why {
			t.Errorf("%s: found %v, %q", tc.name, run != nil, why)
		}
	}
	before := h.runs()
	if got := h.ask(remapUser("a summary"), remapCalls(`TU-1={"path":"a"}`), remapResults("TU-1=A")); len(got) != 1 || got[0] != "text a run started anew" || h.runs() != before+1 {
		t.Fatalf("compacted: %v from %d runs", got, h.runs())
	}
	select {
	case r := <-w[0]:
		t.Errorf("the run from before was handed %v", r)
	default:
	}
}

// A conversation's key is the same whatever ids its client names its tool
// calls by, and tells which call a result answers.
func TestHistoryKeyLeavesOutToolIDs(t *testing.T) {
	conv := func(call, result string) []Message {
		return []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "read a and b"}}},
			{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: call + "1", Name: "read"}, {Kind: ToolCall, ID: call + "2", Name: "read"}}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: result + "1", Text: "A"}, {Kind: ToolResult, CallID: result + "2", Text: "B"}}},
		}
	}
	if historyKey(conv("toolu_", "toolu_")) != historyKey(conv("TU-", "TU-")) {
		t.Error("the same conversation under other ids has another key")
	}
	swapped := conv("TU-", "TU-")
	swapped[2].Parts[0].CallID, swapped[2].Parts[1].CallID = "TU-2", "TU-1"
	if historyKey(swapped) == historyKey(conv("TU-", "TU-")) {
		t.Error("results answering the other calls have the same key")
	}
}
