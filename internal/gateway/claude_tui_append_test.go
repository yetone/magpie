package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// appendHarness is a script harness whose Claude Code keeps the file it is
// given by --append-system-prompt-file, in Claude Code's interactive mode
// (through a stand-in helper) when tui says so. appended is each run's
// file by the run's log.
func appendHarness(t *testing.T, tui bool) (*resumeHarness, func() map[string]string) {
	t.Helper()
	kept := t.TempDir()
	h := newScriptHarness(t, "ok", `prev=
for a in "$@"; do
  if [ "$prev" = "--append-system-prompt-file" ]; then cp "$a" `+kept+`/$$; fi
  prev=$a
done`)
	if tui {
		claudeTUIRuns = true
		t.Cleanup(func() { claudeTUIRuns = false })
		helper := filepath.Join(t.TempDir(), "magpie")
		testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift; claude=$1; shift
exec "$claude" "$@"
`)
		claudeTUIHelper = helper
		t.Cleanup(func() { claudeTUIHelper = "" })
	}
	return h, func() map[string]string {
		out := map[string]string{}
		for pid, log := range h.runs() {
			b, err := os.ReadFile(filepath.Join(kept, pid))
			if err != nil {
				out[log] = "<none>"
				continue
			}
			out[log] = string(b)
		}
		return out
	}
}

const classifierRules = "You are a security monitor. Block destructive actions; the rules run long."

// The auto mode classifier's asks, one-off asks under the same long
// instructions, are pasted into the interactive mode as one text, so the
// instructions lost their cache mark and every ask wrote them to the
// cache anew: 80k tokens written and 1,197 read an ask, the same account
// and conversation, no 429 (#2432, laozhang713 and j1374615791-lab). They
// go after Claude Code's own system prompt now, in the block it marks
// itself, and the ask's first message has the action only.
func TestClaudeTUIOneOffAskAppendsInstructions(t *testing.T) {
	h, appended := appendHarness(t, true)
	for _, action := range []string{"rm -rf build", "git push --force"} {
		h.post(`{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","messages":[{"role":"user","content":"<transcript>` + action + `</transcript>"}]}`)
	}
	runs := appended()
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2: %v", len(runs), runs)
	}
	for log, file := range runs {
		// a test's request comes from another computer: where its tools
		// run follows the instructions, as in a lean run's
		if !strings.HasPrefix(file, classifierRules+"\n\n") || !strings.HasSuffix(file, strings.TrimSpace(envUnknownNote)) {
			t.Errorf("a one-off ask's run was given %q after Claude Code's system prompt, want the caller's instructions\n%s", file, log)
		}
		if strings.Contains(log, "external_system_instructions") || strings.Contains(log, "security monitor") {
			t.Errorf("a one-off ask's first message still has the caller's instructions:\n%s", log)
		}
		if !strings.Contains(log, "rm -rf build") && !strings.Contains(log, "git push --force") {
			t.Errorf("a one-off ask's first message lost the action:\n%s", log)
		}
	}
}

// A turn of an agent (tools, or a reply before it) keeps its instructions
// in its first message: its run is resumed and branched, and a session
// resumed without the appended file would go on with none. -p takes the
// client's marks, so a -p run is unchanged too.
func TestClaudeAppendOnlyForInteractiveOneOffs(t *testing.T) {
	for _, c := range []struct {
		name string
		tui  bool
		// told: the instructions are in what the run is typed; a
		// conversation's are in the transcript it resumes
		told bool
		body string
	}{
		{"tools", true, true, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"reply", true, false, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`},
		{"print", false, true, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, appended := appendHarness(t, c.tui)
			h.post(c.body)
			for log, file := range appended() {
				if file != "<none>" {
					t.Errorf("given %q after Claude Code's system prompt, want nothing\n%s", file, log)
				}
				if c.told && !strings.Contains(log, "security monitor") {
					t.Errorf("the first message lost the caller's instructions:\n%s", log)
				}
			}
		})
	}
}
