package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// CLJY (Discord): npm couldn't remove claude.exe while a Claude Code run
// magpie kept for a conversation's next turn still ran it (EPERM), so an
// update stopped partway and left %APPDATA%\npm with the sh shim alone.
// LetGoIdleClaudeCode, which an npm update on Windows asks first, ends
// every waiting run and returns once its process has exited: the exe it
// ran can then be removed. Its conversation is shelved for its next turn;
// a run in a turn is left to finish it.
func TestLetGoIdleClaudeCodeFreesTheExe(t *testing.T) {
	const child = "MAGPIE_TEST_LETGO_CHILD"
	if os.Getenv(child) != "" {
		time.Sleep(2 * time.Minute) // a Claude Code waiting for its next turn
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude"+filepath.Ext(self))
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, body, 0o755); err != nil {
		t.Fatal(err)
	}

	b := idleTestBridge()
	start := func() *subscriptionRun {
		cmd := proc.Command(exe, "-test.run=^TestLetGoIdleClaudeCodeFreesTheExe$", "-test.timeout=3m")
		cmd.Env = append(os.Environ(), child+"=1")
		tree, err := proc.StartTree(cmd)
		if err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		r := &subscriptionRun{bridge: b, cmd: cmd, tree: tree, exited: exited, pending: map[string]chan mcpToolResult{}}
		go func() {
			tree.Wait()
			r.mu.Lock()
			close(exited)
			r.mu.Unlock()
		}()
		t.Cleanup(func() { <-tree.Stop(0) })
		return r
	}
	waiting := start()
	waiting.sessionID, waiting.sessions = "s1", []string{filepath.Join(dir, "project")}
	b.keepIdle(waiting, "conversation", "conversation")
	b.runs["waiting"] = waiting
	busy := &subscriptionRun{bridge: b, pending: map[string]chan mcpToolResult{}}
	b.runs["busy"] = busy
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, s := range b.shelf {
			if s.timer != nil {
				s.timer.Stop()
			}
		}
	})

	bridges.Store(b, true)
	t.Cleanup(func() { bridges.Delete(b) })
	if n := LetGoIdleClaudeCode(); n < 1 {
		t.Fatalf("let go of %d runs; one was waiting", n)
	}
	select {
	case <-waiting.exited:
	default:
		t.Fatal("LetGoIdleClaudeCode returned before the waiting run's Claude Code exited")
	}
	if len(b.idle) != 0 {
		t.Fatalf("%d runs still kept for a next turn", len(b.idle))
	}
	if s := b.shelf["conversation"]; s == nil || s.session != "s1" {
		t.Fatal("the waiting run's conversation was not shelved for its next turn")
	}
	busy.mu.Lock()
	closed := busy.closed
	busy.mu.Unlock()
	if closed {
		t.Fatal("a run in a turn was ended")
	}
	// the exe npm replaces: on Windows a running copy refuses this (EPERM)
	if err := os.Remove(exe); err != nil {
		t.Fatalf("the exe can't be removed after the let-go (%s): %v", runtime.GOOS, err)
	}
}
