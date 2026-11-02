package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// With the interactive mode on, /usage is typed in it, through its
// helper, where -p's /usage tells no window of the plan (Claude Code
// 2.1.316): the helper is told the command, and what it tells is the
// reading. No claude -p runs. Turned off, it runs as -p.
func TestClaudeUsageTUIOn(t *testing.T) {
	plog := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"Total cost: $0.0000"}`, 0)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	os.Unsetenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	log := filepath.Join(t.TempDir(), "log")
	text := "Current session: 99% used · resets 7pm (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift 2
{ printf 'args:'; for a in "$@"; do printf '[%s]' "$a"; done; echo
  echo "trust:$`+claudetui.TrustEnv+`"; echo "traffic:$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"; echo "telemetry:$DISABLE_TELEMETRY"
  IFS= read -r line; echo "told:$line"; } > '`+log+`'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"local_command":"usage","result":"Current session: 99% used · resets 7pm (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"}'
cat > /dev/null
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, err := claudeUsage(ctx, "")
	if err != nil || got != text {
		t.Fatalf("usage %q %v", got, err)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"[-p][--output-format][stream-json][--input-format][stream-json]", "[--tools][]", "[--setting-sources][]",
		`[--settings][{"disableDeepLinkRegistration":"disable"}]`,
		"trust:1\n", "traffic:\n", "telemetry:1\n", `told:{"type":"command","command":"/usage"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the helper's run lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "/usage]") {
		t.Errorf("/usage was given as an argument, not typed:\n%s", b)
	}
	if _, err := os.Stat(plog); err == nil {
		t.Fatal("claude -p ran with the interactive mode on")
	}
	printMode(t, true)
	if _, err := claudeUsage(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(plog); !strings.Contains(string(b), "[-p][/usage]") {
		t.Fatalf("turned off, /usage didn't run as -p:\n%s", b)
	}
}

// A /usage read in the interactive mode works in magpie's own folder
// (claudeWorkDir), every time the same: Claude Code asks whether to trust
// a folder once for each state file, and a folder of each read's own was
// asked again each time and left one more trusted project in the account's
// state file (#4830, mydosu: 67 of them). A session the run told is
// removed after it.
func TestClaudeUsageTUIWorksInMagpiesFolder(t *testing.T) {
	fakeWarmClaude(t, `{}`, 0)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	cfg := t.TempDir()
	work, err := claudeWorkDir()
	if err != nil {
		t.Fatal(err)
	}
	sid := "4830aaaa-0000-4000-8000-000000000000"
	var left []string
	for _, d := range claudeSessions(cfg, work) {
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, sid+".jsonl"), []byte("{}\n"), 0o600)
		left = append(left, filepath.Join(d, sid+".jsonl"))
	}
	log := filepath.Join(t.TempDir(), "log")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
echo "pwd:$(pwd)" >> '`+log+`'
printf '%s\n' '{"type":"system","subtype":"init","session_id":"`+sid+`"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"local_command":"usage","session_id":"`+sid+`","result":"Current session: 1% used"}'
cat > /dev/null
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		got, err := claudeUsage(ctx, cfg)
		cancel()
		if err != nil || got != "Current session: 1% used" {
			t.Fatalf("usage %q %v", got, err)
		}
	}
	b, _ := os.ReadFile(log)
	real, _ := filepath.EvalSymlinks(work)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if d := strings.TrimPrefix(l, "pwd:"); d != work && d != real {
			t.Errorf("a read worked in %s, not magpie's folder %s:\n%s", d, work, b)
		}
	}
	for _, f := range left {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("the run's session %s was left", f)
		}
	}
}

// A read whose helper is killed at its caller's deadline says so, never
// only the exit status the kill left ("exit status 1" on Windows, #4830);
// and the helper is told the deadline, a margin before it, to end then on
// its own, saying why (claudetui.DeadlineEnv).
func TestClaudeUsageTUISaysItsDeadline(t *testing.T) {
	fakeWarmClaude(t, `{}`, 0)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	log := filepath.Join(t.TempDir(), "log")
	helper := filepath.Join(t.TempDir(), "magpie")
	// a helper that heeds no deadline and says nothing
	testenv.Program(t, helper, `#!/bin/sh
echo "by:$`+claudetui.DeadlineEnv+`" > '`+log+`'
exec sleep 30
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dl, _ := ctx.Deadline()
	_, err := claudeUsage(ctx, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "said nothing before magpie stopped waiting") {
		t.Fatalf("got %v", err)
	}
	b, _ := os.ReadFile(log)
	ms, perr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(string(b), "by:")), 10, 64)
	if perr != nil {
		t.Fatalf("the helper was told %q", b)
	}
	// a quarter of the 2s left, the margin less than its 3s
	if by := time.UnixMilli(ms); !by.Before(dl) || by.Before(dl.Add(-time.Second)) {
		t.Fatalf("the helper was told %s, the deadline %s", by, dl)
	}
}
