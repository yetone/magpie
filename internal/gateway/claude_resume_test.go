package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// resumeHarness runs Claude subscription requests through a script
// standing in for Claude Code, which keeps its session as Claude Code
// does: a file under its config folder's project for the folder it works
// in, the one --resume names when it is given one. Each run's arguments and
// the messages it was told go to its own log.
type resumeHarness struct {
	t      *testing.T
	s      *Server
	p      provider.Provider
	logs   string
	config string
}

func newResumeHarness(t *testing.T) *resumeHarness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	config := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	dir, logs := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
log=` + logs + `/$$
echo "args $*" > $log
sid=s$$
prev=
for a in "$@"; do
  if [ "$prev" = "--resume" ]; then sid=$a; fi
  prev=$a
done
case " $* " in
  *" --no-session-persistence "*) file= ;;
  *) proj=$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')
     mkdir -p "$CLAUDE_CONFIG_DIR/projects/$proj"
     file="$CLAUDE_CONFIG_DIR/projects/$proj/$sid.jsonl" ;;
esac
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  echo "told $line" >> $log
  if [ -n "$file" ]; then echo "$line" >> "$file"; fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := New()
	t.Cleanup(s.subscription.abortAll)
	return &resumeHarness{t: t, s: s, p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}, logs: logs, config: config}
}

// ask sends a conversation's turn: its system prompt and the user's
// messages, each answered "ok" before the next.
func (h *resumeHarness) ask(system string, said ...string) {
	h.t.Helper()
	var msgs []string
	for i, m := range said {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"ok"}`)
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, m))
	}
	body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"` + system + `","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
}

// runs is each run's log, the arguments it started with first.
func (h *resumeHarness) runs() map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(h.logs)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(h.logs, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}

// sessions is the session files there are.
func (h *resumeHarness) sessions() []string {
	files, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", "*.jsonl"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".jsonl"))
	}
	return out
}

// eventually waits for what the runs' processes do once let go.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200 && !ok(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ok() {
		t.Fatal(what)
	}
}

// A conversation whose run was let go past idleMost goes on from its run's
// saved session: Claude Code is started with --resume on it and told the
// turn's message alone, where a run started anew is told the whole
// conversation in one message, all of it written to the prompt cache again
// (X, AncientTwo: a Claude subscription's limits went much faster through
// magpie than in Claude Code itself).
func TestClaudeLetGoConversationResumesItsSession(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	first := h.runs()
	if len(first) != 1 {
		t.Fatalf("runs: %v", first)
	}
	var sidA string
	for pid, log := range first {
		sidA = "s" + pid
		if strings.Contains(log, "--no-session-persistence") {
			t.Fatalf("its session isn't kept: %s", log)
		}
	}
	for i := range idleMost {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	// A, the longest waiting, was let go; its session stays for its next turn
	eventually(t, "A's run still going", func() bool { return len(h.s.subscription.idle) == idleMost })
	if got := h.sessions(); len(got) != idleMost+1 || !containsStr(got, sidA) {
		t.Fatalf("sessions %v, want A's %s among %d", got, sidA, idleMost+1)
	}

	before := h.runs()
	h.ask("rules A", "a1", "a2")
	var resumed string
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			resumed = log
		}
	}
	if !strings.Contains(resumed, "--resume "+sidA) {
		t.Fatalf("A's next turn did not resume its session:\n%s", resumed)
	}
	told := resumed[strings.Index(resumed, "\ntold "):]
	if !strings.Contains(told, "a2") || strings.Contains(told, "a1") || strings.Contains(told, "rules A") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
	}
	if len(h.s.subscription.shelf) != 1 {
		// the conversation that waited longest after A was let go in turn
		t.Fatalf("shelf: %d", len(h.s.subscription.shelf))
	}

	// a resumed conversation goes on in its run, as any other
	h.ask("rules A", "a1", "a2", "a3")
	if n := len(h.runs()); n != idleMost+2 {
		t.Fatalf("runs: %d, want %d", n, idleMost+2)
	}
}

// Sessions are files of their conversations: one is removed with its run
// when nothing will resume it (a one-off ask, a run that ended), and a
// saved one once its conversation's next turn takes it, or it waited as
// long as its run would have.
func TestClaudeSessionFilesAreRemoved(t *testing.T) {
	h := newResumeHarness(t)
	// a one-off ask: no tools, no reply in it
	body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"title this"}]}`
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	eventually(t, "a one-off ask's session stayed", func() bool { return len(h.sessions()) == 0 })

	for i := range idleMost + 1 {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	eventually(t, "the shelf", func() bool {
		h.s.subscription.mu.Lock()
		defer h.s.subscription.mu.Unlock()
		return len(h.s.subscription.shelf) == 1
	})
	var saved *savedSession
	for _, s := range h.s.subscription.shelf {
		saved = s
	}
	if !saved.saved() {
		t.Fatal("the shelved session has no file")
	}
	saved.discard()
	if saved.saved() {
		t.Fatal("a discarded session's file stayed")
	}

	// the runs waiting end: their sessions go with them
	h.s.subscription.abortAll()
	eventually(t, "sessions left", func() bool { return len(h.sessions()) == 0 })
}

// A gateway that stopped leaves the sessions of its shelf: the next one
// removes those older than a session is kept, and leaves the newer, which
// may be another gateway's on the same account.
func TestClaudeOldSessionsAreSwept(t *testing.T) {
	dir := t.TempDir()
	old, recent := filepath.Join(dir, "old.jsonl"), filepath.Join(dir, "recent.jsonl")
	os.WriteFile(old, []byte("{}"), 0o600)
	os.Mkdir(filepath.Join(dir, "old"), 0o700)
	os.WriteFile(recent, []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	long := time.Now().Add(-2 * idleLongest)
	os.Chtimes(old, long, long)
	newSubscriptionBridge().sweepSessions([]string{dir})
	eventually(t, "the old session stayed", func() bool { _, err := os.Stat(old); return os.IsNotExist(err) })
	for _, f := range []string{recent, filepath.Join(dir, "notes.txt")} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Fatal("the old session's folder stayed")
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
