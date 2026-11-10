package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A PATH folder on a mount that doesn't answer is left out of a look, and
// the command is found in the folders after it: a look walking PATH past
// such a folder blocked magpie's start-up for over a minute, after `open
// -a` (ifccod7391564 on X). The gone mount is a probe that doesn't return
// and a walk of PATH that reports itself; the old lookup walked it.
func TestHungPathFolderIsLeftOut(t *testing.T) {
	hung, good := t.TempDir(), t.TempDir()
	tool := "magpie-reach-tool"
	file := tool
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(filepath.Join(good, file), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", joinList([]string{hung, good}))
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "1")

	release := make(chan struct{})
	var probes atomic.Int32
	oldProbe, oldWait := probeDir, reachWait
	probeDir = func(d string) {
		if d == hung {
			probes.Add(1)
			<-release
		}
		oldProbe(d)
	}
	reachWait = 200 * time.Millisecond
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	resetReach := func() {
		reach.Lock()
		reach.m = nil
		reach.Unlock()
	}
	resetReach()
	t.Cleanup(func() {
		free()
		probeDir, reachWait = oldProbe, oldWait
		resetReach()
	})
	var walked atomic.Int32
	oldLook := execLookPath
	execLookPath = func(name string) (string, error) {
		if !strings.ContainsAny(name, `/\`) {
			walked.Add(1) // exec.LookPath along all of PATH, the gone mount too
		}
		return oldLook(name)
	}
	t.Cleanup(func() { execLookPath = oldLook })

	for i := 0; i < 3; i++ {
		Forget()
		done := make(chan struct{})
		var p string
		var err error
		go func() {
			p, err = Look(tool)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the look waited on the folder that doesn't answer")
		}
		if err != nil || filepath.Dir(p) != good {
			t.Fatalf("look %d: %q, %v; want it in %s", i, p, err, good)
		}
	}
	if n := walked.Load(); n != 0 {
		t.Fatalf("PATH was walked %d times with a folder on it that doesn't answer", n)
	}
	// the folder is asked once while its first probe is out, not per look
	if n := probes.Load(); n != 1 {
		t.Fatalf("the folder was probed %d times, want 1", n)
	}

	// the mount answers again: PATH is walked as it was
	free()
	deadline := time.Now().Add(30 * time.Second)
	for !answers(hung) {
		if time.Now().After(deadline) {
			t.Fatal("the folder never counted as answering again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	Forget()
	if p, err := Look(tool); err != nil || filepath.Dir(p) != good {
		t.Fatalf("after: %q, %v", p, err)
	}
	if walked.Load() == 0 {
		t.Fatal("a PATH whose folders all answer wasn't walked as exec.LookPath does")
	}
}
