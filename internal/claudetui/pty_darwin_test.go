package claudetui

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPTYMasterBlocking: the master is a blocking descriptor outside Go's
// poller, so a read waits for Claude Code's output instead of answering
// EAGAIN (#4834), and a child's 4000 bytes, more than the terminal holds,
// are all read (the reporter's reproduction).
func TestPTYMasterBlocking(t *testing.T) {
	m, name, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	fl, err := unix.FcntlInt(m.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fl&unix.O_NONBLOCK != 0 {
		t.Fatalf("the ptmx master is non-blocking (flags %#x): a read can answer EAGAIN", fl)
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "sleep 0.2; head -c 4000 /dev/zero | tr '\\0' x")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		tty.Close()
		t.Fatal(err)
	}
	tty.Close()
	got := make(chan int, 1)
	go func() {
		total, buf := 0, make([]byte, 64<<10)
		for {
			n, err := m.Read(buf)
			total += n
			if err != nil {
				got <- total
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		t.Fatal("the child hung writing to a terminal no one read")
	}
	select {
	case n := <-got:
		if n < 4000 {
			t.Fatalf("read %d bytes, want 4000", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read never ended after the child did")
	}
}
