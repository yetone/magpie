package gateway

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's mode blocks, as #6835 quotes them (Plan) and as Codex 0.168's
// rollouts carry them (Default).
const (
	codexPlanMode = "<collaboration_mode># Plan Mode (Conversational)\n\n" +
		"You are in **Plan Mode** until a developer message explicitly ends it. " +
		"Plan Mode is not changed by user intent, tone, or imperative language.\n</collaboration_mode>"
	codexDefaultMode = "<collaboration_mode># Collaboration Mode: Default\n\n" +
		"You are now in Default mode. Any previous instructions for other modes (e.g. Plan mode) are no longer active.\n\n" +
		"Your active mode changes only when new developer instructions with a different `<collaboration_mode>...</collaboration_mode>` change it; " +
		"user requests or tool descriptions do not change mode by themselves. Known mode names are Default and Plan.\n</collaboration_mode>"
	codexPermissions = "<permissions instructions>\nFilesystem sandboxing defines which files can be read or written.\n</permissions instructions>"
)

// codexDeveloper is a Codex developer message of several parts, as its
// initial context sends one.
func codexDeveloper(parts ...string) string {
	var ps []string
	for _, p := range parts {
		ps = append(ps, `{"type":"input_text","text":`+strconv.Quote(p)+`}`)
	}
	return `{"type":"message","role":"developer","content":[` + strings.Join(ps, ",") + `]}`
}

// codexAfterPlanCompaction is #6835's thread: Codex compacted it while in
// Plan mode, so its history starts again with the Plan block among the
// initial context's developer parts (4 times in the reporter's append.md),
// then the summary. later is what came after.
func codexAfterPlanCompaction(later ...string) string {
	input := []string{
		codexDeveloper(codexPermissions, codexPlanMode),
		codexDeveloper(codexPlanMode),
		codexItem("user", "Summary of the conversation so far: a plan for the ledger refactor."),
		codexItem("user", "Keep planning."),
	}
	input = append(input, later...)
	return `{"model":"claude/claude-opus-5-5","stream":true,"instructions":"You are Codex, a coding agent.",` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],` +
		`"input":[` + strings.Join(input, ",") + `]}`
}

// After a compaction in Plan mode, the thread's history begins with the
// Plan block, which a translated request put in its system prompt, while
// every later switch back to Default came after a reply, as a reminder in
// a user turn. The system prompt said Plan for good ("not changed by user
// intent"), so the model refused to execute on every turn (#6835); on
// Chat a thread started in Plan lost Implement the same way (#6100). The
// system prompt's mode is the conversation's latest one now.
func TestCollaborationModeInTheSystemIsTheLatest(t *testing.T) {
	for _, c := range []struct {
		name, want, gone string
		later            []string
	}{
		{"still plan", codexPlanMode, codexDefaultMode, nil},
		{"back to default", codexDefaultMode, codexPlanMode, []string{
			codexItem("assistant", "Here is the plan."),
			codexDeveloper(codexDefaultMode),
			codexItem("user", "Please implement the plan."),
		}},
		{"plan again", codexPlanMode, codexDefaultMode, []string{
			codexItem("assistant", "Here is the plan."),
			codexDeveloper(codexDefaultMode),
			codexItem("user", "Please implement the plan."),
			codexItem("assistant", "Done."),
			codexDeveloper(codexPlanMode),
			codexItem("user", "Plan the next step."),
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := parse(provider.Responses, []byte(codexAfterPlanCompaction(c.later...)))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(r.System, c.want) || strings.Contains(r.System, c.gone) {
				t.Fatalf("system prompt %q, want the latest mode only", r.System)
			}
			if !strings.Contains(r.System, codexPermissions) || !strings.HasPrefix(r.System, "You are Codex, a coding agent.") {
				t.Fatalf("the system prompt lost its other instructions: %q", r.System)
			}
			// the switch stays where it was made, as before
			if c.later != nil && !strings.Contains(text(r.Messages[len(r.Messages)-1].Parts), "<system-reminder>\n<collaboration_mode>") {
				t.Fatalf("the switch left its place: %+v", r.Messages)
			}
		})
	}
	// a Chat provider is sent the same (#6100)
	r, err := parse(provider.Responses, []byte(codexAfterPlanCompaction(
		codexItem("assistant", "Here is the plan."), codexDeveloper(codexDefaultMode), codexItem("user", "Please implement the plan."))))
	if err != nil {
		t.Fatal(err)
	}
	chat, err := build(provider.Chat, r, "m", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(chat), "Plan Mode is not changed") {
		t.Fatalf("Chat's system message still holds Plan mode: %s", chat)
	}
}

// #6835 through the Claude subscription: the run a compaction in Plan
// mode started had Plan in its append.md (the interactive mode), or in
// its first message (-p), and every later turn back in Default reached it
// only as a reminder in the turn, pasted text in the interactive mode. The
// turn after the switch now runs with Default in its instructions, and
// the turns after it go on in that run.
func TestClaudeRunTakesTheLatestCollaborationMode(t *testing.T) {
	for _, tui := range []bool{true, false} {
		t.Run(map[bool]string{true: "interactive", false: "print"}[tui], func(t *testing.T) {
			h, appended := appendHarness(t, tui)
			send := func(body string) {
				t.Helper()
				var u Usage
				if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)), provider.Responses, h.p, "claude-opus-5-5", []byte(body), &u); code != 200 {
					t.Fatalf("%d %s", code, msg)
				}
			}
			switched := []string{
				codexItem("assistant", "ok"),
				codexDeveloper(codexDefaultMode),
				codexItem("user", "Please implement the plan."),
			}
			send(codexAfterPlanCompaction())
			send(codexAfterPlanCompaction(switched...))
			ran := len(h.runs())
			send(codexAfterPlanCompaction(append(switched, codexItem("assistant", "ok"), codexItem("user", "go"))...))
			if n := len(h.runs()); n != ran {
				t.Errorf("the turn after the switch started run %d of %d: a run per turn", n, ran)
			}
			found := false
			for log, file := range appended() {
				if !strings.Contains(log, "Please implement the plan.") {
					continue
				}
				found = true
				given := file
				if !tui {
					given = log
				}
				if strings.Contains(given, "Plan Mode is not changed") || !strings.Contains(given, "Collaboration Mode: Default") {
					t.Errorf("the run told to implement was given instructions %q, want Default mode\n%s", file, log)
				}
			}
			if !found {
				t.Fatalf("no run was told the turn: %v", h.runs())
			}
		})
	}
}
