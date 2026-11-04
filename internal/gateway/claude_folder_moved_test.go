package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EricWvi on X (Claude Code → a local magpie → a magpie on a server →
// a Claude subscription): the cache read flipped between under 10% and
// over 90%, and every drop was a turn whose folder had changed
// (…/frontend, then …/frontend/src). Claude Code says a move as an
// "# Environment update" message, "Primary working directory: /y (was
// /x)", after the environment it gave first. The bridge took the last
// folder named, so a run started again for the conversation (its saved
// session resumed, a branch, a replay) was given another system prompt
// than the conversation's first run: envTurnsNote names the folder, and
// the caller's own folder on this computer is the one the run works in,
// which is in Claude Code's own environment and decides which project its
// session is saved in. Either way the prompt differed right after Claude
// Code's own part, and the whole conversation was written to the cache
// again. A conversation keeps the folder it named first.
func TestClaudeTUIConversationKeepsItsFolderWhenTheCallerMoves(t *testing.T) {
	env := func(dir string) string {
		return `# Environment\nYou have been invoked in the following environment: \n - Primary working directory: ` + dir + `\n - Is a git repository: true\n - Platform: darwin\n - Shell: zsh\n - OS Version: Darwin 25.2.0`
	}
	moved := func(dir, was string) string {
		return `# Environment update\n - Primary working directory: ` + dir + ` (was ` + was + `)`
	}
	body := func(msgs ...string) string {
		return `{"model":"claude-sonnet-5","max_tokens":100,` +
			`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.316.cfe; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"Rules."}],` +
			`"messages":[` + strings.Join(msgs, ",") + `],` +
			`"tools":[{"name":"Bash","description":"Run a command.","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`
	}
	user := func(s string) string { return `{"role":"user","content":[{"type":"text","text":"` + s + `"}]}` }
	system := func(s string) string { return `{"role":"system","content":[{"type":"text","text":"` + s + `"}]}` }
	for _, c := range []struct {
		name string
		here bool // the caller's folders are on this computer
	}{{"a caller elsewhere", false}, {"a caller on this computer", true}} {
		t.Run(c.name, func(t *testing.T) {
			h, appended := appendHarness(t, true)
			root := "/Users/eric/work/app"
			if c.here {
				root = filepath.Join(t.TempDir(), "app")
			}
			frontend, src := filepath.Join(root, "frontend"), filepath.Join(root, "frontend", "src")
			if c.here {
				if err := os.MkdirAll(src, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			first := []string{user("a1"), system(env(frontend))}
			h.post(body(first...))
			var sid, file string
			for pid, log := range h.runs() {
				sid, file = "s"+pid, appended()[log]
			}
			// the conversation's run is let go and shelved, as one is past
			// idleMost others or idleLongest
			for i := range idleMost {
				h.ask(fmt.Sprintf("rules %d", i), "x")
			}
			eventually(t, "the other runs still going", func() bool { return len(h.s.subscription.idle) == idleMost })
			before := h.runs()
			h.post(body(append(first, `{"role":"assistant","content":"ok"}`, system(moved(src, frontend)), user("a2"))...))
			for log, again := range appended() {
				if _, ok := before[logPID(h, log)]; ok {
					continue
				}
				if !strings.Contains(log, "--resume "+sid) {
					t.Fatalf("the turn after the move did not resume the conversation's session %s, so it was told the whole conversation again:\n%s", sid, log)
				}
				if again != file {
					t.Fatalf("the turn after the move was given another system prompt than the conversation's first run, a prefix the cache has never seen:\nfirst %q\nthen  %q", file, again)
				}
				return
			}
			t.Fatal("the turn after the move started no run")
		})
	}
}
