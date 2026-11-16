package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startsLeft runs, through run, a shell that leaves a sleep running on its
// own (as `codex app-server daemon restart` leaves the daemon) and says its
// pid; the sleep ends with the test.
func startsLeft(t *testing.T, run func(*exec.Cmd) ([]byte, error)) int {
	t.Helper()
	cmd := CommandContext(context.Background(), "/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 </dev/null & echo $!")
	out, err := run(cmd)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("output %q", out)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	})
	return pid
}

// What a command magpie leaves running asks macOS's privacy prompts in
// its own name, not magpie's (reformed9448 on Discord: the first codex in
// ~/Desktop, attached to the Codex daemon magpie had restarted, had macOS
// say magpie wants the Desktop folder).
func TestCombinedOutputOwnAnswersForItself(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS's responsibility for a process")
	}
	self := Responsible(os.Getpid())
	if self == 0 {
		t.Skip("macOS doesn't say who is responsible for this process")
	}
	// started the plain way, what is left running is this process's
	// responsibility
	if got := Responsible(startsLeft(t, (*exec.Cmd).CombinedOutput)); got != self {
		t.Fatalf("a plain child's sleep answers to %d, want %d as this process does", got, self)
	}
	got := Responsible(startsLeft(t, func(c *exec.Cmd) ([]byte, error) { return CombinedOutputOwn(context.Background(), c) }))
	if got == 0 || got == self {
		t.Fatalf("the sleep answers to %d, this process to %d: want one of its own", got, self)
	}
}

// noShell skips a test that drives /bin/sh where there is none (Windows).
func noShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh on Windows")
	}
}

func TestCombinedOutputOwnRuns(t *testing.T) {
	noShell(t)
	dir := t.TempDir()
	cmd := Command("/bin/sh", "-c", "pwd; echo \"$MAGPIE_T\"; echo err >&2; if [ -p /dev/stdin ]; then echo pipe; else echo other; fi; exit 3")
	cmd.Env = append(os.Environ(), "MAGPIE_T=v")
	cmd.Dir = dir
	out, err := CombinedOutputOwn(context.Background(), cmd)
	if err == nil || err.Error() != "exit status 3" {
		t.Errorf("err %v, want exit status 3", err)
	}
	real, _ := os.Readlink(dir)
	lines := strings.Fields(string(out))
	if len(lines) != 4 || (lines[0] != dir && lines[0] != real && !strings.HasSuffix(lines[0], dir)) || lines[1] != "v" || lines[2] != "err" || lines[3] != "other" {
		t.Errorf("output %q", out)
	}
	if out, err := CombinedOutputOwn(context.Background(), Command("/bin/sh", "-c", "echo ok")); err != nil || string(out) != "ok\n" {
		t.Errorf("%q, %v", out, err)
	}
	if _, err := CombinedOutputOwn(context.Background(), Command("/nonexistent/magpie-x")); err == nil {
		t.Error("a missing program ran")
	}
}

// The context ending ends the command, and the run with it, and the run
// says the context ended, whether the command had started or not (#5967:
// a started one's Wait said "signal: killed").
func TestCombinedOutputOwnContext(t *testing.T) {
	noShell(t)
	for _, started := range []bool{false, true} {
		mark := filepath.Join(t.TempDir(), "started")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := CombinedOutputOwn(ctx, CommandContext(ctx, "/bin/sh", "-c", `: > "$1"; exec sleep 30`, "sh", mark))
			done <- err
		}()
		if started {
			for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(mark); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the command never started")
				}
			}
		}
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Errorf("started %v: err %v, want canceled", started, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("started %v: still running after its context ended", started)
		}
	}
}
