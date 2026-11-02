package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	extra  string // fields each request has besides its own, each with a comma after it
	from   string // the requests' address, "" for httptest's (another computer's)
}

func newResumeHarness(t *testing.T) *resumeHarness {
	t.Helper()
	return newReplyHarness(t, "ok")
}

// newReplyHarness is a resumeHarness whose Claude Code answers reply.
func newReplyHarness(t *testing.T, reply string) *resumeHarness {
	t.Helper()
	return newScriptHarness(t, reply, "")
}

// newScriptHarness is a resumeHarness whose Claude Code answers reply,
// after it has run start, shell lines of its own ($sid is its session,
// $file its file, $log its log).
func newScriptHarness(t *testing.T, reply, start string) *resumeHarness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_TMPDIR", t.TempDir())
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
` + start + `
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in *'"type":"control_request"'*)
    echo "control $line" >> $log
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    # acknowledged as Claude Code does: initialize, a settings change, a
    # rewind of the conversation it was asked to take back
    case "$line" in *rewind_conversation*)
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}'
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'","response":{"rewound":true}}}';;
    *) echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}';;
    esac
    continue ;;
  esac
  echo "told $line" >> $log
  if [ -n "$file" ]; then printf '%s\n' "$line" >> "$file"; fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(reply) + `}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
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
	body := `{"model":"claude-sonnet-5","max_tokens":100,` + h.extra + `"system":"` + system + `","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
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

// A run that answered the conversation's prompt suggestion since its last
// turn, and was let go past idleMost, leaves its session where the
// conversation's next turn comes by: that turn resumes a copy of it cut
// before the suggestion, which the client's conversation never had, and
// Claude Code goes on from it told the new message alone.
// Before, the conversation's own turn had taken the run back from the
// suggestion (#1638), its session marked taken back, and the shelf took
// none of it: the next turn started a new Claude Code told the whole
// conversation in one message, written to the prompt cache again (#4914).
func TestClaudeLetGoConversationAfterSuggestionResumesItsSession(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	var sidA string
	for pid := range runs {
		sidA = "s" + pid
	}
	// the prompt suggestion after each turn: the conversation and one
	// message more
	h.ask("rules A", "a1", suggestionMode)
	h.ask("rules A", "a1", "a2")
	h.ask("rules A", "a1", "a2", suggestionMode)
	if n := len(h.runs()); n != 1 {
		t.Fatalf("%d runs, the conversation's own all along", n)
	}
	// let go past idleMost, its session stays for its conversation's next
	// turn, whatever suggestions it answered since
	for i := range idleMost {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	h.s.subscription.mu.Lock()
	shelf := len(h.s.subscription.shelf)
	h.s.subscription.mu.Unlock()
	if shelf != 1 {
		t.Fatalf("shelf: %d, the session was not kept for the conversation's next turn", shelf)
	}
	before := h.runs()
	h.ask("rules A", "a1", "a2", "a3")
	var resumed string
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			resumed = log
		}
	}
	_, after, ok := strings.Cut(resumed, "--resume ")
	cut := ""
	if f := strings.Fields(after); len(f) > 0 {
		cut = f[0]
	}
	if !ok || cut == "" || cut == sidA {
		t.Fatalf("the conversation's next turn did not resume a copy of its session cut before the suggestion:\n%s", resumed)
	}
	file := ""
	dirs, _ := filepath.Glob(filepath.Join(h.config, "projects", "*"))
	for _, dir := range dirs {
		if b, err := os.ReadFile(filepath.Join(dir, cut+".jsonl")); err == nil {
			file = string(b)
		}
		if _, err := os.Stat(filepath.Join(dir, sidA+".jsonl")); err == nil {
			t.Errorf("the shelved session's own file is left beside its copy")
		}
	}
	// the copy is the session as it stood before the second suggestion,
	// a3 told after it; the first suggestion's exchange, taken back,
	// sits above a2, off the line Claude Code resumes
	lines := strings.Split(strings.TrimSpace(file), "\n")
	if n := len(lines); n < 2 || !strings.Contains(lines[n-2], `"a2"`) || !strings.Contains(lines[n-1], `"a3"`) || strings.Count(file, "SUGGESTION") != 1 {
		t.Fatalf("the resumed session wasn't cut before the second suggestion:\n%s", file)
	}
	told := resumed[strings.Index(resumed, "\ntold "):]
	if !strings.Contains(told, `"a3"`) || strings.Contains(told, `"a1"`) || strings.Contains(told, `"a2"`) || strings.Contains(told, "SUGGESTION") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
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
	// The sweep removes the session's file, then its folder: wait for both.
	gone := func(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }
	eventually(t, "the old session stayed", func() bool { return gone(old) && gone(filepath.Join(dir, "old")) })
	for _, f := range []string{recent, filepath.Join(dir, "notes.txt")} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
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

// A client Claude Code's billing line changes every turn (cch,
// cc_prompt_id, cc_prev_req…), and a run is never told it. Its next turn
// still goes on in the run that had the conversation, told the new message
// alone; with the line in the run's key each turn started a new Claude Code
// told the whole conversation, all of it written to the prompt cache again
// (#1638, chicring).
func TestClaudeBillingLineKeepsTheRun(t *testing.T) {
	h := newResumeHarness(t)
	ask := func(billing string, msgs ...string) {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.114.a1b; cc_entrypoint=cli; ` + billing + `"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
		var u Usage
		if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	ask(`cch=1f2e3; cc_prompt_id=0b9e6c1a-1111-4a6b-9c55-1d2f3e4a5b6c;`, `{"role":"user","content":"first ask"}`)
	ask(`cch=9a8b7; cc_prev_req=req_011CU1; cc_prompt_id=7d1c2e3f-2222-4b7c-8d66-2e3f4a5b6c7d;`,
		`{"role":"user","content":"first ask"}`, `{"role":"assistant","content":"ok"}`, `{"role":"user","content":"second ask"}`)
	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("the next turn started a new run, %d runs:\n%v", len(runs), runs)
	}
	for _, log := range runs {
		told := log[strings.LastIndex(log, "\ntold "):]
		if !strings.Contains(told, "second ask") || strings.Contains(told, "first ask") {
			t.Fatalf("the run was not told the turn alone:\n%s", log)
		}
		if strings.Contains(log, "billing") || strings.Contains(log, "cch=") {
			t.Fatalf("the run was told the billing line:\n%s", log)
		}
	}
}

// Both keys a run is found again by leave the billing line out, the turn's
// (turnKey) and a rewritten turn's (newLooseTurn), and keep the rest of
// the system prompt in.
func TestClaudeRunKeysLeaveOutTheBillingLine(t *testing.T) {
	msgs := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "b1"}}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "ok"}}},
	}
	req := func(system string) *Request { return &Request{Model: "claude-sonnet-5", System: system} }
	a := req("x-anthropic-billing-header: cc_version=2.1.114.a1b; cc_entrypoint=cli; cch=1f2e3;You are Claude Code.")
	b := req("x-anthropic-billing-header: cc_version=2.1.114.a1b; cc_entrypoint=cli; cch=9a8b7; cc_prev_req=req_011CU1;You are Claude Code.")
	other := req("x-anthropic-billing-header: cc_version=2.1.114.a1b; cc_entrypoint=cli; cch=1f2e3;You are someone else.")
	if turnKey("o", a, msgs) != turnKey("o", b, msgs) {
		t.Fatal("turnKey changes with the billing line")
	}
	if newLooseTurn("o", a, msgs, false).key != newLooseTurn("o", b, msgs, false).key {
		t.Fatal("newLooseTurn's key changes with the billing line")
	}
	if turnKey("o", a, msgs) == turnKey("o", other, msgs) || newLooseTurn("o", a, msgs, false).key == newLooseTurn("o", other, msgs, false).key {
		t.Fatal("a key leaves out the system prompt itself")
	}
}
