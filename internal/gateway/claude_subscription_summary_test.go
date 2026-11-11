package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recih on X (#6128): Claude Code's AgentSummary sends the sub-agent's
// conversation whole, without the assistant messages whose calls have no
// result yet, and keeps the words before them. The last reply then has no
// call, branch found no line to cut at, and a new Claude Code was told the
// whole transcript: 90K to 160K tokens written to the cache for a label.
// The run that waits on that reply is found by the conversation before it,
// and the branch is cut before its first call, after its words.
func TestClaudeBranchForSummaryWithoutThePendingCall(t *testing.T) {
	dir := t.TempDir()
	const sid = "lead-session"
	lines := []string{
		`{"uuid":"u1","sessionId":"` + sid + `","type":"user"}`,
		`{"uuid":"a-words","sessionId":"` + sid + `","type":"assistant"}`,
		`{"uuid":"a-call","sessionId":"` + sid + `","type":"assistant"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := func(s string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}} }
	words := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "I will run the tests."}}}
	history := []Message{user("task")}

	b := newSubscriptionBridge()
	run := &subscriptionRun{sessionID: sid, sessions: []string{dir}, tui: true}
	own := "claude\x00u"
	run.owner.Store(&own)
	run.lines = map[string]callLine{"toolu_1": {uuid: "a-call", seq: 1, told: historyKey(history)}}
	b.runs["r"] = run

	req := &Request{Messages: append(append(append([]Message{}, history...), words), user("Describe your progress in 3-5 words."))}
	got, why := b.branch(req, own)
	if got == nil {
		t.Fatalf("no branch for the summary request: %s", why)
	}
	if !got.cutBefore || got.cut != "a-call" || len(got.since) != 1 {
		t.Fatalf("branch = cutBefore %v at %q, %d messages told; want cut before a-call, told the summary only", got.cutBefore, got.cut, len(got.since))
	}
	id, err := cutSessionBefore(got.sessions, got.session, got.cut)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if s := string(b2); !strings.Contains(s, "a-words") || strings.Contains(s, "a-call") {
		t.Fatalf("the branch keeps %q, want the words and not the call", s)
	}

	// another conversation's summary finds no run, and says why
	other := &Request{Messages: []Message{user("another task"), words, user("summary")}}
	if got, why := b.branch(other, own); got != nil || !strings.Contains(why, "no run waits") {
		t.Fatalf("another conversation: %v %q", got, why)
	}
	// another account's run is not taken
	if got, _ := b.branch(req, "claude\x00v"); got != nil {
		t.Fatal("a run of another account was branched")
	}
	// a run past its session is told why
	run.mu.Lock()
	run.closed = true
	run.mu.Unlock()
	if got, why := b.branch(req, own); got != nil || !strings.Contains(why, "closed") {
		t.Fatalf("closed run: %v %q", got, why)
	}
}
