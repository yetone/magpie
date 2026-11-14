package cli

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A magpie whose exit runs into a DLL detach that never returns (WebView2's
// or COM's, waiting on a thread ExitProcess already ended) still ends:
// BESTLJP0412 on X, 0.1.4179 to 0.1.4193, the magpie an update restarted
// sat with one thread on the gateway's port and the WebView2 profile, and
// the new one's window was "Not Responding". The stuck detach here is a
// fiber-local storage callback, which ExitProcess runs on its last thread
// and TerminateProcess doesn't: kernel32's Sleep, with INFINITE as its
// value.
const testExitEnv = "MAGPIE_TEST_EXIT_STUCK"

func init() {
	how := os.Getenv(testExitEnv)
	if how == "" {
		return
	}
	runtime.LockOSThread() // the callback is this thread's, which exits
	if err := stuckDetach(); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(9)
	}
	if how == "os.Exit" {
		os.Exit(3)
	}
	endProcess(3)
}

// stuckDetach has this thread's exit wait forever, as a DLL's detach that
// waits for an ended thread does.
func stuckDetach() error {
	k := windows.NewLazySystemDLL("kernel32.dll")
	sleep := k.NewProc("Sleep")
	if err := sleep.Find(); err != nil {
		return err
	}
	idx, _, err := k.NewProc("FlsAlloc").Call(sleep.Addr())
	if idx == 0xFFFFFFFF {
		return err
	}
	if ok, _, err := k.NewProc("FlsSetValue").Call(idx, 0xFFFFFFFF); ok == 0 {
		return err
	}
	return nil
}

func exitStuck(t *testing.T, how string) (*exec.Cmd, chan error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(), testExitEnv+"="+how)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ended := make(chan struct{})
	go func() { done <- cmd.Wait(); close(ended) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-ended
	})
	return cmd, done
}

func TestEndProcessSkipsAStuckDetach(t *testing.T) {
	// the stand-in is real: os.Exit there hangs with one thread left
	cmd, done := exitStuck(t, "os.Exit")
	select {
	case err := <-done:
		t.Fatalf("os.Exit with a stuck detach ended (%v): the stand-in proves nothing", err)
	case <-time.After(2 * time.Second):
	}
	if n := threadsOf(uint32(cmd.Process.Pid)); n != 1 {
		t.Fatalf("the process stuck in os.Exit has %d threads, want 1", n)
	}

	_, done = exitStuck(t, "endProcess")
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3 {
			t.Fatalf("endProcess(3) ended with %v, want exit code 3", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("endProcess hung in a DLL's detach, as os.Exit does")
	}
}

func threadsOf(pid uint32) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == pid {
			return e.Threads
		}
	}
	return 0
}
