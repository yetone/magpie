package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// tuiHelperHarness is a script harness whose runs in Claude Code's
// interactive mode start a stand-in for magpie's claude-tui-helper: it
// notes each start in started, waits as hold says (a shell command), and
// runs the stand-in Claude Code it was handed.
func tuiHelperHarness(t *testing.T, hold string) (*resumeHarness, string) {
	t.Helper()
	h := newReplyHarness(t, "ok")
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	started := filepath.Join(t.TempDir(), "started")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
echo start >> '`+started+`'
`+hold+`
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	return h, started
}

func helperStarts(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "start")
}

// An interactive Claude Code that takes longer than a control request's
// 10s to show its prompt is waited for: the bridge gave the helper 10s to
// answer initialize and ended it as it waited, before it became -p on its
// own (claudetui.ReadyWait, 45s), and every try was answered 502 "Claude
// Code has not answered magpie's initialize request in 10s" (#4504,
// derrors: 2 vCPU, Claude Code in Docker).
func TestClaudeTUISlowStartAnswered(t *testing.T) {
	h, started := tuiHelperHarness(t, "sleep 12")
	h.ask("rules", "hi") // fails the test on anything but 200
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the helper started %d times, want once", n)
	}
}

// A helper that never answers initialize is let go, and the turn fails
// saying the interactive mode didn't start: it is never answered by -p,
// which the plan counts as another client's and which has none of the
// interactive mode's cache (the owner, 11-01). Nothing is marked: the next
// turn tries the interactive mode again.
func TestClaudeTUIHungHelperNeverPrint(t *testing.T) {
	was := tuiInitWait
	tuiInitWait = time.Second
	t.Cleanup(func() { tuiInitWait = was })
	h, started := tuiHelperHarness(t, "exec sleep 600")
	code, msg := h.askCode("rules", "hi")
	if code == 200 || !strings.Contains(msg, "interactive mode didn't start") || !strings.Contains(msg, "never runs it as claude -p") {
		t.Fatalf("%d %s", code, msg)
	}
	if runs := h.runs(); len(runs) != 0 {
		t.Fatalf("a -p Claude Code ran: %v", runs)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the helper started %d times, want once", n)
	}
	h.askCode("other rules", "hello")
	if n := helperStarts(started); n != 2 {
		t.Fatalf("the helper started %d times, want twice: the next turn didn't try the interactive mode", n)
	}
}

// A caller's folder the user hasn't trusted in their own Claude Code is
// one only they answer for: the interactive mode asked and ended there,
// and the turn was a 502, which a Claude Code client (paseo) asked again
// and again, so the turn hung (lwjef on X). The turn goes in magpie's own
// folder, told the caller's, and that folder is passed over for the
// conversation's next turns rather than asked about again.
//
// The user's answers say the folder is trusted here, so the run goes
// there first (untrustedHere); Claude Code asking all the same is the case
// this test is about.
func TestClaudeTUIUntrustedFolderAnswered(t *testing.T) {
	caller := t.TempDir()
	cwds := filepath.Join(t.TempDir(), "cwds")
	h, started := tuiHelperHarness(t, `if [ "$`+claudetui.TrustEnv+`" != 1 ]; then
  echo "claude-tui: `+claudetui.Untrusted+`, which only its user answers" >&2
  exit 1
fi
pwd >> '`+cwds+`'`)
	trustInProfile(t, h, caller)
	h.ask("Primary working directory: "+caller, "hi") // fails the test on anything but 200
	b, _ := os.ReadFile(cwds)
	ran := strings.Fields(string(b))
	if len(ran) != 1 || evalSymlinks(ran[0]) == evalSymlinks(caller) {
		t.Fatalf("the answered run ran in %q, the caller's folder %s", ran, caller)
	}
	if n := helperStarts(started); n != 2 {
		t.Fatalf("the helper started %d times, want twice: in the caller's folder, then magpie's", n)
	}
	h.ask("Primary working directory: "+caller, "another")
	if n := helperStarts(started); n != 3 {
		t.Fatalf("the helper started %d times, want three: the caller's folder was asked about again", n)
	}
}

// An interactive mode that doesn't come up for what Claude Code waits on
// is the same for a new Claude Code told the whole conversation: the turn
// fails once, saying why. It was tried again, waited out another 45s, and
// was answered 502 after twice that (sowhynot). A Claude Code that ended
// itself before its prompt, as one that can't load the session it was
// told to go on with does, is still tried again told the whole
// conversation.
func TestClaudeTUINoPromptNotTriedAgain(t *testing.T) {
	for _, c := range []struct {
		name, said string
		starts     int
	}{
		{"no prompt", "Claude Code showed no prompt in 45s: nothing on its screen (0 bytes read)", 1},
		{"ended itself", claudetui.EndedEarly + " (exit status 1): No conversation found", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, started := tuiHelperHarness(t, `echo "claude-tui: `+c.said+`" >&2
exit 1`)
			// a conversation no run here has had: replayed from a transcript
			code, msg := h.askCode("rules", "hi", "again", "more")
			if code == 200 || !strings.Contains(msg, c.said) {
				t.Fatalf("%d %s", code, msg)
			}
			if n := helperStarts(started); n != c.starts {
				t.Fatalf("the helper started %d times, want %d", n, c.starts)
			}
			if runs := h.runs(); len(runs) != 0 {
				t.Fatalf("a -p Claude Code ran: %v", runs)
			}
		})
	}
}
