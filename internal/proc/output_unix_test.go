//go:build !windows

package proc

import (
	"io"
	"syscall"
	"testing"
	"time"
)

// A command is waited for when it ends, while what it started still holds
// its output: read to EOF first, its Wait waited for the program it left
// behind, and it stayed <defunct> all that time.
func TestOutputWaitsPastWhatHoldsIt(t *testing.T) {
	old := OutputGrace
	OutputGrace = 200 * time.Millisecond
	t.Cleanup(func() { OutputGrace = old })

	cmd := Command("sh", "-c", "sleep 30 & sleep 30 >&2 & echo hi")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	o, err := NewOutput(cmd)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := o.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(o.Out)
		o.OutRead()
		got <- string(b)
	}()
	go func() {
		_, _ = io.Copy(io.Discard, o.Err)
		o.ErrRead()
	}()
	waited := make(chan error, 1)
	go func() { waited <- wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command wasn't waited for in 10s while what it started held its output")
	}
	// waited, so reaped: the process is gone, not a zombie
	if err := syscall.Kill(cmd.Process.Pid, 0); err != syscall.ESRCH {
		t.Fatalf("the shell is still there after wait: %v", err)
	}
	select {
	case s := <-got:
		if s != "hi\n" {
			t.Fatalf("read %q, want what it wrote", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read didn't end once its pipe was closed")
	}
}

// What a command writes as it ends is read whole before its pipe is closed.
func TestOutputKeepsTheLastLines(t *testing.T) {
	cmd := Command("sh", "-c", "i=0; while [ $i -lt 2000 ]; do echo line $i; i=$((i+1)); done")
	o, err := NewOutput(cmd)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := o.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, o.Err); o.ErrRead() }()
	time.Sleep(200 * time.Millisecond) // it has ended, its output still unread
	b, _ := io.ReadAll(o.Out)
	o.OutRead()
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if n := len(b); n < 2000*len("line 0\n") {
		t.Fatalf("read %d bytes of what it wrote", n)
	}
}
