//go:build !windows

package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// probeScript is a CLI that is a script starting another program and
// waiting on it, as a version manager's wrapper does; it writes down the
// pid of what it started.
func probeScript(t *testing.T) (sh, pidFile string) {
	dir := t.TempDir()
	sh, pidFile = filepath.Join(dir, "cli"), filepath.Join(dir, "child.pid")
	testenv.Program(t, sh, "#!/bin/sh\nsleep 30 &\necho $! > "+pidFile+"\nwait\n")
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
	return sh, pidFile
}

// runProbe runs the probe in the background; the channel gives what its
// run ended with.
func runProbe(cmd *exec.Cmd) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := cmd.Output()
		done <- err
	}()
	return done
}

// childPid waits for the script to have started its child. A script just
// written can take seconds to start on a busy Mac, so the wait is long; a
// probe that ended before is said with its error.
func childPid(t *testing.T, pidFile string, done chan error) int {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	end := time.After(30 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				return pid
			}
		}
		select {
		case err := <-done:
			t.Fatalf("the probe ended (%v) before the script started anything", err)
		case <-end:
			t.Fatal("the script started nothing in 30s")
		case <-tick.C:
		}
	}
}

// a probe whose context ends (its timeout) ends with what it started:
// killing the script alone left its child running. The context is ended
// once the script has started its child, not on a timer that a slow start
// could beat.
func TestProbeTimeoutEndsWhatItStarted(t *testing.T) {
	sh, pidFile := probeScript(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	done := runProbe(ProbeContext(ctx, sh))
	pid := childPid(t, pidFile, done)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the probe went on after its context ended")
	}
	if !gone(pid) {
		t.Fatalf("what the probe started (pid %d) outlived its context", pid)
	}
}

// a probe still asking when magpie exits is ended with it, rather than
// left to init
func TestEndProbes(t *testing.T) {
	sh, pidFile := probeScript(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	done := runProbe(ProbeContext(ctx, sh))
	pid := childPid(t, pidFile, done)
	start := time.Now()
	EndProbes()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the probe went on after EndProbes")
	}
	if !gone(pid) {
		t.Fatalf("what the probe started (pid %d) outlived EndProbes", pid)
	}
	if d := time.Since(start); d > endWait+time.Second {
		t.Fatalf("EndProbes took %v", d)
	}
}

// a probe that answered is forgotten once its context ends, so EndProbes
// doesn't wait on it
func TestProbeForgotten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	if _, err := ProbeContext(ctx, "true").Output(); err != nil {
		t.Fatal(err)
	}
	cancel()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		probes.Lock()
		n := len(probes.m)
		probes.Unlock()
		if n == 0 {
			return
		}
	}
	t.Fatal("a probe that answered is still listed")
}
