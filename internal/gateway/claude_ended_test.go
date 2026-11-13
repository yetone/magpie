package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// endedFixture is a run of the bridge's that parked on the client's
// AskUserQuestion, its session as Claude Code 2.1.316 leaves it when it is
// killed there: the reply's words, then its call, and no result (a real
// run's file, SIGTERMed while an MCP call was held, gained only cost-state
// lines).
type endedFixture struct {
	b       *subscriptionBridge
	run     *subscriptionRun
	dir     string
	sid     string
	own     string
	history []Message
	reply   Message
}

func newEndedFixture(t *testing.T, b *subscriptionBridge, token string) *endedFixture {
	t.Helper()
	dir := t.TempDir()
	sid := "lead-" + token
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"` + sid + `"}`,
		`{"type":"assistant","uuid":"a-words","sessionId":"` + sid + `","message":{"id":"msg_1","content":[{"type":"text","text":"I will ask."}]}}`,
		`{"type":"assistant","uuid":"a-call","sessionId":"` + sid + `","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_ask","name":"mcp__magpie__AskUserQuestion"}]}}`,
		`{"type":"cost-state","sessionId":"` + sid + `"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := Message{Role: "user", Parts: []Part{{Kind: Text, Text: "the conversation so far, told at length: " + token}}}
	reply := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "I will ask."}, {Kind: ToolCall, ID: "toolu_ask", Name: "AskUserQuestion", Args: json.RawMessage(`{}`)}}}
	own := "claude\x00u"
	run := &subscriptionRun{bridge: b, token: token, sessionID: sid, sessions: []string{dir}, tui: true}
	run.owner.Store(&own)
	run.lines = map[string]callLine{"toolu_ask": {uuid: "a-call", seq: 1, told: historyKey([]Message{user})}}
	b.mu.Lock()
	b.runs[token] = run
	b.mu.Unlock()
	return &endedFixture{b: b, run: run, dir: dir, sid: sid, own: own, history: []Message{user}, reply: reply}
}

// answered is the client's request with the question's answer.
func (f *endedFixture) answered() *Request {
	return &Request{Messages: []Message{f.history[0], f.reply, {Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "toolu_ask", Text: "Use the second option"}}}}}
}

// summary is Claude Code's AgentSummary of the conversation, which leaves
// out the reply's call, its result not in yet when it was asked.
func (f *endedFixture) summary() *Request {
	words := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "I will ask."}}}
	return &Request{Messages: []Message{f.history[0], words, {Role: "user", Parts: []Part{{Kind: Text, Text: "Describe your most recent action in 3-5 words"}}}}}
}

func (f *endedFixture) saved() bool {
	_, err := os.Stat(filepath.Join(f.dir, f.sid+".jsonl"))
	return err == nil
}

// recih (#6528, case 1): the run waited on the client's AskUserQuestion
// past runTimeout and was let go ("it waited as long as it may"); the
// answer came two minutes later. Its session went with it, and a new
// Claude Code was told all 95 messages, 118K tokens written to the cache.
// The session is kept, with where the call is in it, and the answer goes
// on in a branch cut after the call, told only the result. A delayed
// AgentSummary on the history before the answer branches off it too: the
// kept session is never taken, each branch is a copy of its own.
func TestClaudeAnswerAfterTheRunEndedBranchesOffItsKeptSession(t *testing.T) {
	logs := keepLog(t)
	b := newSubscriptionBridge()
	t.Cleanup(b.abortAll)
	f := newEndedFixture(t, b, "r1")

	f.run.abortFor("it waited as long as it may") // runTimeout's own call
	b.mu.Lock()
	left := len(b.runs)
	b.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d runs left, want the ended run gone", left)
	}
	if !f.saved() {
		t.Fatal("the ended run's session was removed with it")
	}

	got, why := b.branch(f.answered(), f.own)
	if got == nil {
		t.Fatalf("the answer found no branch: %s", why)
	}
	if got.cut != "a-call" || got.cutBefore || len(got.since) != 1 || got.since[0].Parts[0].Kind != ToolResult {
		t.Fatalf("branch = cut %q before %v, %d messages told; want cut after a-call, told the result only", got.cut, got.cutBefore, len(got.since))
	}
	id, err := cutSession(got.sessions, got.session, got.cut)
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := os.ReadFile(filepath.Join(f.dir, id+".jsonl"))
	if s := string(copied); !strings.Contains(s, `"uuid":"a-call"`) || strings.Contains(s, "cost-state") || !strings.Contains(s, `"sessionId":"`+id+`"`) {
		t.Fatalf("the branch is:\n%s", s)
	}
	if !strings.Contains(logs.String(), "it waited as long as it may") || !strings.Contains(logs.String(), "kept since, is branched") {
		t.Errorf("the log doesn't say the branch is of an ended run's session:\n%s", logs.String())
	}

	// the delayed AgentSummary, on the history before the answer
	sum, why := b.branch(f.summary(), f.own)
	if sum == nil {
		t.Fatalf("the delayed summary found no branch: %s", why)
	}
	if !sum.cutBefore || sum.cut != "a-call" {
		t.Fatalf("summary branch = cut %q before %v, want before a-call", sum.cut, sum.cutBefore)
	}
	if !f.saved() {
		t.Fatal("a branch took the kept session")
	}

	// another account's request is not branched off it
	if got, _ := b.branch(f.answered(), "claude\x00v"); got != nil {
		t.Fatal("another account's request was branched off the kept session")
	}
}

// What is kept is bounded: each ended run's session goes after
// endedLongest, the oldest past endedMost, and every one as the gateway
// stops; a request that comes after finds none.
func TestClaudeEndedRunsSessionsAreBounded(t *testing.T) {
	t.Run("time", func(t *testing.T) {
		was := endedLongest
		endedLongest = 50 * time.Millisecond
		t.Cleanup(func() { endedLongest = was })
		b := newSubscriptionBridge()
		t.Cleanup(b.abortAll)
		f := newEndedFixture(t, b, "r1")
		f.run.abortFor("it waited as long as it may")
		deadline := time.Now().Add(5 * time.Second)
		for f.saved() {
			if time.Now().After(deadline) {
				t.Fatal("the ended run's session outlived endedLongest")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got, _ := b.branch(f.answered(), f.own); got != nil {
			t.Fatal("a branch of a session let go")
		}
	})
	t.Run("count", func(t *testing.T) {
		b := newSubscriptionBridge()
		t.Cleanup(b.abortAll)
		var fs []*endedFixture
		for i := range endedMost + 1 {
			f := newEndedFixture(t, b, fmt.Sprintf("r%d", i))
			f.run.abortFor("the client went away during a turn it can't take back")
			fs = append(fs, f)
			time.Sleep(time.Millisecond) // each ends after the one before
		}
		if fs[0].saved() {
			t.Fatal("the oldest session was kept past endedMost")
		}
		for _, f := range fs[1:] {
			if !f.saved() {
				t.Fatalf("session %s went, want the newest %d kept", f.sid, endedMost)
			}
		}
		b.mu.Lock()
		n := len(b.ended)
		b.mu.Unlock()
		if n != endedMost {
			t.Fatalf("%d kept, want %d", n, endedMost)
		}
	})
	t.Run("stop", func(t *testing.T) {
		b := newSubscriptionBridge()
		f := newEndedFixture(t, b, "r1")
		f.run.abortFor("it waited as long as it may")
		live := newEndedFixture(t, b, "r2")
		b.abortAll()
		if f.saved() || live.saved() {
			t.Fatalf("sessions left as the gateway stopped: ended %v, live %v", f.saved(), live.saved())
		}
	})
	t.Run("unkept", func(t *testing.T) {
		b := newSubscriptionBridge()
		t.Cleanup(b.abortAll)
		// a run whose turn was taken back, or the client marks its own
		// cache in, has nothing a branch could be cut from
		f := newEndedFixture(t, b, "r1")
		f.run.rewound = true
		f.run.abortFor("x")
		g := newEndedFixture(t, b, "r2")
		g.run.clientMarks = true
		g.run.abortFor("x")
		if f.saved() || g.saved() {
			t.Fatalf("kept: rewound %v, client's marks %v", f.saved(), g.saved())
		}
	})
}

// recih (#6528, case 2): a tunnel dropped mid-reply, magpie heard the
// client go away and ended its interactive run (letGo: the interactive
// mode can't be taken back), and the client's own magpie retried the
// request. The retry was told the whole conversation by a new Claude Code
// (125K written); it goes on in a branch of the ended run's session, cut
// before the turn, told only the turn.
func TestClaudeTurnSentAgainAfterItsRunEndedBranchesOffItsSession(t *testing.T) {
	h := newBusyRetryHarness(t, "interactive")
	pickFiveMinutes(t)
	logs := keepLog(t)
	first := h.ask(`[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `]`)
	if !strings.Contains(first, "turn 1") {
		t.Fatalf("the first turn: %q", first)
	}
	conv := `[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "and run the tests") + `]`

	// the turn is sent, its run holds mid-reply, and the client goes away
	firstTold := h.heard()
	body := h.body(conv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var u Usage
		h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	}()
	t.Cleanup(func() {
		cancel()
		h.s.subscription.abortAll()
		<-done
	})
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.read(), "and run the tests") || h.heard() == firstTold {
		if time.Now().After(deadline) {
			t.Fatalf("the run was never told the turn:\n%s\nmagpie log:\n%s", h.read(), logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	// letGo ends the run once the request returned
	for {
		h.s.subscription.mu.Lock()
		n := len(h.s.subscription.runs)
		h.s.subscription.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run given up on was never ended:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// the client's magpie sends it again
	if again := h.ask(conv); again == "" {
		t.Fatal("the retry was answered nothing")
	}
	branch, lead := 0, 0
	for name, told := range h.toldBy() {
		if !strings.Contains(told, "--resume") {
			lead++
			continue
		}
		branch++
		if strings.Contains(told, "the conversation so far") {
			t.Errorf("the retry's run %s was told the whole conversation:\n%.600s", name, told)
		}
		if !strings.Contains(told, "and run the tests") {
			t.Errorf("the retry's run %s was not told the turn:\n%.600s", name, told)
		}
		if i := strings.Index(told, "--resume "); strings.HasPrefix(told[i+9:], "lead") {
			t.Errorf("the retry's run resumed the ended run's own session, want a copy")
		}
	}
	if branch != 1 || lead != 1 {
		t.Fatalf("%d runs branched, %d started anew, want one of each; magpie log:\n%s", branch, lead, logs.String())
	}
	if strings.Contains(logs.String(), "from a transcript of it") {
		t.Errorf("the retry's run was told the conversation from a transcript:\n%s", logs.String())
	}
	if want := "after its run ended answering it"; !strings.Contains(logs.String(), want) {
		t.Errorf("the log says:\n%s", logs.String())
	}
	everyRunKept5m(t, h.logs)
}
