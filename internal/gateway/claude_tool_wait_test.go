package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// A turn's run waits on magpie's MCP calls for as long as the caller's tool
// takes, by -p and by the interactive mode: Claude Code neither moves one
// still running after 120s to the background nor aborts one silent for 30
// minutes. A value the user set in magpie's environment stays as they set
// it. Moved to the background, a client's ask_user_question that waited on
// its user had a bash call written past it, and its answer then waited the
// bash call's own 120s to reach the model (#6034, FunnyPartMent on X).
func TestClaudeRunWaitsOnToolCalls(t *testing.T) {
	for _, tui := range []bool{false, true} {
		for _, c := range []struct {
			name string
			env  map[string]string
			want string
		}{
			{name: "nothing set", want: "background 0 idle 0"},
			{name: "magpie's env", env: map[string]string{"CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS": "600000"}, want: "background 600000 idle 0"},
		} {
			t.Run(fmt.Sprintf("tui=%v/%s", tui, c.name), func(t *testing.T) {
				for _, kv := range claudeToolWaitVars {
					k, _, _ := strings.Cut(kv, "=")
					t.Setenv(k, "")
					os.Unsetenv(k)
				}
				for k, v := range c.env {
					t.Setenv(k, v)
				}
				helped := filepath.Join(t.TempDir(), "helped")
				if tui {
					// the interactive mode, its helper a stand-in running
					// the Claude Code it was handed
					claudeTUIRuns = true
					t.Cleanup(func() { claudeTUIRuns = false })
					helper := filepath.Join(t.TempDir(), "magpie")
					testenv.Program(t, helper, "#!/bin/sh\n[ \"$1\" = \""+claudetui.Command+"\" ] || exit 9\necho start >> '"+helped+"'\nshift; claude=$1; shift\nexec \"$claude\" \"$@\"\n")
					claudeTUIHelper = helper
					t.Cleanup(func() { claudeTUIHelper = "" })
				}
				h := newScriptHarness(t, "ok", `echo "background ${CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS-unset} idle ${CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT-unset}" >> $log`)
				h.ask("rules", "hello", "next")
				runs := h.runs()
				if len(runs) != 1 {
					t.Fatalf("runs: %d", len(runs))
				}
				if tui && helperStarts(helped) != 1 {
					t.Fatalf("the helper started %d times", helperStarts(helped))
				}
				for _, log := range runs {
					if !strings.Contains(log, c.want) {
						t.Fatalf("run's environment, want %q:\n%s", c.want, log)
					}
				}
			})
		}
	}
}

// A run in WSL is handed the same variables: claudeEnvVars names them for
// wslrun's Env.
func TestClaudeToolWaitVarsReachWSL(t *testing.T) {
	for _, kv := range claudeToolWaitVars {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(claudeEnvVars, k) {
			t.Fatalf("%s isn't in claudeEnvVars, so a run in WSL waits on its calls 120s at most", k)
		}
	}
}
