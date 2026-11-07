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

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// busyRetryClaude stands in for Claude Code, keeping its session as it
// does: each turn is logged with the arguments and each line it was told,
// a user line and an assistant line of its own written to the session's
// file, and answered with its pid and turn. A run resumed from a session
// answers at once; the second turn of a run started anew holds mid-reply,
// as one whose client went away without magpie hearing of it does (#5405).
// It says where the per-run logs are, and the log of what it was told.
func busyRetryClaude(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir, logs := t.TempDir(), t.TempDir()
	told := filepath.Join(dir, "told.log")
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"u@example.com","subscriptionType":"max"}'; exit 0; fi
log=` + logs + `/$$
echo "args $*" > $log
echo "ttl ${CLAUDE_CODE_PROMPT_CACHE_TTL-unset}" >> $log
echo "mode ${FAKE_TUI:-print}" >> $log
echo "args $*" >> ` + told + `
sid=lead$$; resumed=; prev=
for a in "$@"; do
  if [ "$prev" = --resume ]; then sid=$a; resumed=1; fi
  prev=$a
done
proj="$CLAUDE_CONFIG_DIR/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
mkdir -p "$proj"
file="$proj/$sid.jsonl"
if [ -n "$resumed" ]; then
  [ -f "$file" ] || { echo "No conversation found with session ID: $sid" >&2; exit 1; }
fi
: >> "$file"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
n=0
while read -r line; do
  case "$line" in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue;;
  esac
  echo "told $line" >> $log
  echo "told $line" >> ` + told + `
  n=$((n+1))
  if [ -z "$resumed" ] && [ $n = 2 ]; then
    sleep 60
    continue
  fi
  echo '{"type":"user","uuid":"ask-'$n'","sessionId":"'$sid'"}' >> "$file"
  echo '{"type":"assistant","uuid":"line-'$n'","sessionId":"'$sid'","message":{"id":"m'$n'","model":"claude-sonnet-5"}}' >> "$file"
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$n'","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}}'
  echo '{"type":"assistant","uuid":"line-'$n'","message":{"id":"m'$n'","model":"claude-sonnet-5","content":[{"type":"text","text":"pid '$$' turn '$n'"}]}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logs, told
}

// busyRetryHarness is a rewindHarness whose Claude Code keeps its session,
// in a home of the test's own.
type busyRetryHarness struct {
	*rewindHarness
	logs string
	told string
}

func newBusyRetryHarness(t *testing.T, mode string) *busyRetryHarness {
	logs, told := busyRetryClaude(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	config := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(config, 0o755)
	os.WriteFile(filepath.Join(config, ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok", "refreshToken": "r", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"}}), 0o600)
	if mode != "print" {
		claudeTUIRuns = true
		t.Cleanup(func() { claudeTUIRuns = false })
		helper := filepath.Join(t.TempDir(), "magpie")
		testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift; claude=$1; shift
FAKE_TUI=interactive exec "$claude" "$@"
`)
		claudeTUIHelper = helper
		t.Cleanup(func() { claudeTUIHelper = "" })
	}
	return &busyRetryHarness{rewindHarness: &rewindHarness{t: t, s: New(), log: told,
		p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}}, logs: logs, told: told}
}

// heard is the request the bridge's one run last heard, "" with none.
func (h *busyRetryHarness) heard() string {
	h.s.subscription.mu.Lock()
	var runs []*subscriptionRun
	for _, run := range h.s.subscription.runs {
		runs = append(runs, run)
	}
	h.s.subscription.mu.Unlock()
	if len(runs) != 1 {
		return ""
	}
	runs[0].mu.Lock()
	defer runs[0].mu.Unlock()
	return runs[0].told
}

// toldBy reads what each run was told, keyed by its per-run log.
func (h *busyRetryHarness) toldBy() map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(h.logs)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(h.logs, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}

// send asks msgs without waiting for the answer, as a client whose
// connection is about to drop does.
func (h *busyRetryHarness) send(msgs string) chan struct{} {
	done := make(chan struct{})
	body := h.body(msgs)
	go func() {
		defer close(done)
		var u Usage
		rec := httptest.NewRecorder()
		code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
		h.t.Logf("the first attempt was answered %d %.400s", code, msg)
	}()
	return done
}

// A turn sent again while its run is still busy answering it goes on in a
// branch of the run's session, cut before the turn (#5405): the client's
// connection dropped mid-reply, the request was retried, and no cancel
// reached magpie — the run the first attempt started is still busy with
// the very turn. A new Claude Code told the whole conversation writes it
// all to the cache again; the branch's run reads the history from the
// cache, as Claude Code asked it of Anthropic, and is told only the turn.
func TestClaudeTurnSentAgainWhileItsRunIsBusyBranchesOffItsSession(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) { busyRetryBranches(t, mode) })
	}
}

func busyRetryBranches(t *testing.T, mode string) {
	h := newBusyRetryHarness(t, mode)
	pickFiveMinutes(t)
	logs := keepLog(t)
	first := h.ask(`[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `]`)
	if !strings.Contains(first, "turn 1") {
		t.Fatalf("the first turn: %q", first)
	}
	conv := `[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "and run the tests") + `]`

	// the turn is sent, its run holds mid-reply, and no cancel comes: the
	// request stays unanswered, as one whose client's connection dropped
	// mid-reply does at the server magpie, whose context knows of no cancel
	firstTold := h.heard()
	done := h.send(conv)
	t.Cleanup(func() {
		h.s.subscription.abortAll() // the first attempt holds to the end: stop it
		<-done
	})
	// the busy run has the turn when it was told it and heard the
	// request (heard), as the retry seconds later finds it
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.read(), "and run the tests") || h.heard() == firstTold {
		if time.Now().After(deadline) {
			t.Fatalf("the run was never told the turn:\n%s\nmagpie log:\n%s", h.read(), logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// the request is sent again, as a retry after the dropped connection
	if again := h.ask(conv); again == "" {
		t.Fatal("the retry was answered nothing")
	}
	h.s.subscription.mu.Lock()
	n := len(h.s.subscription.runs)
	state := fmt.Sprintf("%d runs, %d idle; told log:\n%s\nmagpie log:\n%s", n, len(h.s.subscription.idle), h.read(), logs.String())
	h.s.subscription.mu.Unlock()
	if n < 2 {
		t.Fatalf("%s", state)
	}
	branch, lead := 0, 0
	for name, told := range h.toldBy() {
		if !strings.Contains(told, "--resume") {
			lead++
			if !strings.Contains(told, "fix the bug") || !strings.Contains(told, "and run the tests") {
				t.Fatalf("the busy run %s was told:\n%.600s", name, told)
			}
			continue
		}
		branch++
		if strings.Contains(told, "the conversation so far") {
			t.Errorf("the retry's run %s was told the whole conversation:\n%.600s", name, told)
		}
		if !strings.Contains(told, "and run the tests") {
			t.Errorf("the retry's run %s was not told the turn:\n%.600s", name, told)
		}
		// its --resume names a copy of the busy run's session, never the
		// busy run's own (lead<pid>)
		i := strings.Index(told, "--resume ")
		if sid := told[i+9:]; strings.HasPrefix(sid, "lead") {
			t.Errorf("the retry's run resumed the busy run's session %.30s, want a copy of its own", sid)
		}
	}
	if branch != 1 || lead != 1 {
		t.Fatalf("%d runs branched, %d started anew, want one of each", branch, lead)
	}
	if want := "cut before the turn"; !strings.Contains(logs.String(), want) {
		t.Errorf("the log says:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "a new Claude Code is told the whole conversation: 3 messages") {
		t.Errorf("the retry told a new Claude Code the whole conversation:\n%s", logs.String())
	}
	// the retry's branch keeps the 5m cache the busy run had
	everyRunKept5m(t, h.logs)
}

// A turn that goes on past the one a busy run answers is no retry of it:
// the client has a reply to that turn, so the busy run's session, cut
// before it, would lose the turn and its reply, and the run be told only
// the new message. It is told the whole conversation by a new Claude
// Code, as before (#5405).
func TestClaudeTurnPastABusyRunsTurnIsNotBranchedOffIt(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) { busyRunPassedBy(t, mode) })
	}
}

func busyRunPassedBy(t *testing.T, mode string) {
	h := newBusyRetryHarness(t, mode)
	logs := keepLog(t)
	first := h.ask(`[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `]`)
	if !strings.Contains(first, "turn 1") {
		t.Fatalf("the first turn: %q", first)
	}
	conv := `[` + rmsg("user", "the conversation so far, told at length. fix the bug") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "and run the tests")
	firstTold := h.heard()
	done := h.send(conv + `]`)
	t.Cleanup(func() {
		h.s.subscription.abortAll() // the busy run holds to the end: stop it
		<-done
	})
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.read(), "and run the tests") || h.heard() == firstTold {
		if time.Now().After(deadline) {
			t.Fatalf("the run was never told the turn:\n%s\nmagpie log:\n%s", h.read(), logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// the client has a reply to that turn from elsewhere, and goes on
	past := conv + `,` + rmsg("assistant", "the tests pass") + `,` + rmsg("user", "now commit it") + `]`
	if again := h.ask(past); again == "" {
		t.Fatal("the next turn was answered nothing")
	}
	if strings.Contains(logs.String(), "cut before the turn") {
		t.Errorf("the turn past the busy run's was branched off it:\n%s", logs.String())
	}
	// told whole: in one message, or, in the interactive mode, as a
	// transcript of its own (replaySession)
	whole := "a new Claude Code is told the whole conversation: 5 messages"
	if mode == "interactive" {
		whole = "a new Claude Code goes on with the conversation from a transcript of it (4 messages"
	}
	if !strings.Contains(logs.String(), whole) {
		t.Errorf("the turn was not told whole:\n%s", logs.String())
	}
}
