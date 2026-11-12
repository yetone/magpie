package gateway

import (
	"os"
	"strings"
	"testing"
	"time"
)

// runStartedSince is the log of the run that started after before.
func (h *resumeHarness) runStartedSince(before map[string]string) string {
	h.t.Helper()
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			return log
		}
	}
	h.t.Fatal("no new run started")
	return ""
}

// A conversation whose run waited for its next turn when magpie stopped
// (an update, a restart) goes on from its saved session in the gateway that
// starts next: Claude Code is started with --resume on it and told the turn
// alone. Before, the run's session was removed as the gateway ended, and
// the new gateway told a new Claude Code the whole conversation, ~100k
// tokens written to the cache again (#6292, recih on X).
func TestClaudeIdleRunSurvivesARestart(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	var sid string
	for pid := range h.runs() {
		sid = "s" + pid
	}
	h.s.subscription.abortAll() // magpie stops
	if got := h.sessions(); !containsStr(got, sid) {
		t.Fatalf("the idle run's session was removed as the gateway ended: %v", got)
	}

	h.s = New() // the next start
	t.Cleanup(h.s.subscription.abortAll)
	before := h.runs()
	h.ask("rules A", "a1", "a2")
	resumed := h.runStartedSince(before)
	if !strings.Contains(resumed, "--resume "+sid) {
		t.Fatalf("the next start did not resume the session:\n%s", resumed)
	}
	told := resumed[strings.Index(resumed, "\ntold "):]
	if !strings.Contains(told, "a2") || strings.Contains(told, "a1") || strings.Contains(told, "rules A") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
	}
}

// A shelf file that doesn't parse whole is unknown: the conversation is told
// to a new Claude Code, as it is after a start with no file.
func TestClaudeShelfFileUnreadableFallsBackToReplay(t *testing.T) {
	for name, content := range map[string]string{"cut off": `{"version":1,"entries":[{"session":"s1","sess`, "not json": "x", "other version": `{"version":99,"entries":[]}`} {
		t.Run(name, func(t *testing.T) {
			h := newResumeHarness(t)
			h.ask("rules A", "a1")
			h.s.subscription.abortAll()
			if err := os.WriteFile(shelfPath(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			h.s = New()
			t.Cleanup(h.s.subscription.abortAll)
			before := h.runs()
			h.ask("rules A", "a1", "a2")
			started := h.runStartedSince(before)
			if strings.Contains(started, "--resume") {
				t.Fatalf("resumed from a file that wasn't read whole:\n%s", started)
			}
			if !strings.Contains(started, "a1") {
				t.Fatalf("the new run wasn't told the whole conversation:\n%s", started)
			}
		})
	}
}

// A record whose session file is gone is not resumed, and a folder that is not
// one of Claude Code's project folders is not trusted.
func TestClaudeShelfEntryNeedsItsSession(t *testing.T) {
	e := shelfEntry{Session: "s1", Key: "k", Sessions: []string{"/etc"}, At: time.Now()}
	if e.saved() != nil {
		t.Fatal("a folder that isn't a project folder was trusted")
	}
	e.Sessions = []string{t.TempDir() + "/projects/p"}
	if e.saved() != nil {
		t.Fatal("a record with no session file was trusted")
	}
}
