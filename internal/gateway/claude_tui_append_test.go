package gateway

import (
	"fmt"
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

// Every interactive run has the caller's instructions after Claude Code's
// system prompt, an agent's turn (tools) and a conversation's (a reply
// before it) too: pasted, they lost their cache mark, and a run started
// anew read nothing past Claude Code's own prompt. -p takes the client's
// marks, and a client that places its own keeps them in the first
// message, so those are unchanged.
func TestClaudeAppendInTheInteractiveMode(t *testing.T) {
	for _, c := range []struct {
		name     string
		tui      bool
		appended bool
		body     string
		header   []string
	}{
		{"tools", true, true, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`, nil},
		{"reply", true, true, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`, nil},
		{"client marks", true, false, `{"model":"claude-sonnet-5","max_tokens":64,"system":[{"type":"text","text":"` + classifierRules + `","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`, []string{"X-Magpie-Cache-Marks", "client"}},
		{"print", false, false, `{"model":"claude-sonnet-5","max_tokens":64,"system":"` + classifierRules + `","messages":[{"role":"user","content":"hi"}]}`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, appended := appendHarness(t, c.tui)
			h.post(c.body, c.header...)
			runs := appended()
			if len(runs) != 1 {
				t.Fatalf("%d runs, want 1: %v", len(runs), runs)
			}
			for log, file := range runs {
				if !c.appended {
					if file != "<none>" {
						t.Errorf("given %q after Claude Code's system prompt, want nothing\n%s", file, log)
					}
					if !strings.Contains(log, "security monitor") {
						t.Errorf("the first message lost the caller's instructions:\n%s", log)
					}
					continue
				}
				if !strings.HasPrefix(file, classifierRules+"\n\n") {
					t.Errorf("given %q after Claude Code's system prompt, want the caller's instructions\n%s", file, log)
				}
				if strings.Contains(log, "security monitor") || strings.Contains(log, "external_system_instructions") {
					t.Errorf("the run was typed the caller's instructions too:\n%s", log)
				}
			}
			if c.appended {
				for _, f := range h.sessionFiles() {
					if strings.Contains(f, "security monitor") {
						t.Errorf("a session the run resumes holds the caller's instructions, which it has after Claude Code's prompt:\n%s", f)
					}
				}
			}
		})
	}
}

// sessionFiles is the words of each session file there is.
func (h *resumeHarness) sessionFiles() []string {
	files, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", "*.jsonl"))
	var out []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		out = append(out, string(b))
	}
	return out
}

// m4rs0n on Discord, straight to the gateway with one long cached system
// block and no tools: the conversation's first request, sent again as it
// was, read all of it from the cache, but the request after it, the same
// one with the reply and one more user message, read 1,273 of 8,565 and
// wrote the rest, as did every request of a loop that went on from there.
// The first was a one-off ask, its instructions after Claude Code's system
// prompt and its run let go after the reply; the second's run was given
// the conversation as its transcript with the instructions in its first
// message, so what it sent differed from the first's right after Claude
// Code's own prompt. Every run of the conversation is given the same
// system prompt now, the caller's instructions after Claude Code's, and
// none has them in its messages.
func TestClaudeTUIConversationKeepsItsInstructionsWhereTheFirstAskHadThem(t *testing.T) {
	h, appended := appendHarness(t, true)
	first := `{"role":"user","content":"Say ok."}`
	h.post(`{"model":"claude-sonnet-5","max_tokens":16,"system":"` + classifierRules + `","messages":[` + first + `]}`)
	h.post(`{"model":"claude-sonnet-5","max_tokens":16,"system":"` + classifierRules + `","messages":[` + first + `,{"role":"assistant","content":"ok"},{"role":"user","content":"Now say two words."}]}`)
	runs := appended()
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2: %v", len(runs), runs)
	}
	var files []string
	for log, file := range runs {
		files = append(files, file)
		if strings.Contains(log, "security monitor") || strings.Contains(log, "external_system_instructions") {
			t.Errorf("a run was typed the caller's instructions, which the conversation's first run had after Claude Code's prompt:\n%s", log)
		}
	}
	if files[0] != files[1] || !strings.HasPrefix(files[0], classifierRules+"\n\n") {
		t.Errorf("the conversation's runs were given %q and %q after Claude Code's system prompt, want the same instructions", files[0], files[1])
	}
	for _, f := range h.sessionFiles() {
		if strings.Contains(f, "security monitor") {
			t.Errorf("the transcript the second run goes on from holds the caller's instructions:\n%s", f)
		}
	}
}

// A conversation's saved session, resumed past idleMost, is given the
// caller's instructions after Claude Code's system prompt as its run had
// them: the session keeps no system prompt, and before, a run that had
// them so was never shelved, so its next turn was a new run that wrote
// the whole conversation to the cache again.
func TestClaudeTUIResumedSessionIsGivenItsInstructions(t *testing.T) {
	h, appended := appendHarness(t, true)
	h.ask("rules A", "a1")
	var sidA string
	for pid := range h.runs() {
		sidA = "s" + pid
	}
	for i := range idleMost {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	eventually(t, "A's run still going", func() bool { return len(h.s.subscription.idle) == idleMost })
	before := h.runs()
	h.ask("rules A", "a1", "a2")
	for log, file := range appended() {
		if _, ok := before[logPID(h, log)]; ok {
			continue
		}
		if !strings.Contains(log, "--resume "+sidA) {
			t.Fatalf("A's next turn did not resume its session:\n%s", log)
		}
		if !strings.HasPrefix(file, "rules A\n\n") {
			t.Errorf("the resumed run was given %q after Claude Code's system prompt, want A's instructions\n%s", file, log)
		}
		told := log[strings.Index(log, "\ntold "):]
		if !strings.Contains(told, "a2") || strings.Contains(told, "a1") || strings.Contains(told, "rules A") {
			t.Fatalf("the resumed run was told more than the turn:\n%s", told)
		}
		return
	}
	t.Fatal("A's next turn started no run")
}

// logPID is the run whose log is log.
func logPID(h *resumeHarness, log string) string {
	for pid, l := range h.runs() {
		if l == log {
			return pid
		}
	}
	return ""
}
