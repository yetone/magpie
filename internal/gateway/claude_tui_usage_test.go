package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A /usage read never starts the interactive Claude Code, the
// interactive mode on (#5582): as it starts, before /usage is typed, it
// sends a counted model request ("quota", haiku, max_tokens 1; recorded
// from Claude Code 2.1.316), which opened the five-hour window of an
// idle account with warm-up off. The read runs as -p, which sends none,
// for the signed-in account and a saved one, and keeps
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC off, without which /usage
// loads nothing.
func TestClaudeUsageStartsNoInteractiveRun(t *testing.T) {
	text := "Current session: 13% used · resets 4pm (Asia/Shanghai)"
	plog := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"`+text+`"}`, 0)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	os.Unsetenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	started := filepath.Join(t.TempDir(), "started")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
echo "$@" >> '`+started+`'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"local_command":"usage","result":"`+text+`"}'
cat > /dev/null
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	if !claudeTUIOn() {
		t.Fatal("the interactive mode isn't on")
	}
	for _, cfg := range []string{"", t.TempDir()} {
		os.Remove(plog)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		got, err := claudeUsage(ctx, cfg)
		cancel()
		if err != nil || got != text {
			t.Fatalf("account %q: usage %q %v", cfg, got, err)
		}
		if b, err := os.ReadFile(started); err == nil {
			t.Fatalf("account %q: a /usage read started the interactive Claude Code:\n%s", cfg, b)
		}
		b, _ := os.ReadFile(plog)
		for _, want := range []string{"[-p][/usage][--output-format][json]", "traffic:\n", "telemetry:1\n"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("account %q: the -p read lacks %q:\n%s", cfg, want, b)
			}
		}
	}
}
