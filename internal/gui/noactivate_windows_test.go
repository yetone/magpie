//go:build windows && !nogui

package gui

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// Left hooked on the main thread when the process exits, noActivateCBT is
// called by user32!DispatchHookW during ExitProcess's LdrShutdownProcess
// (msctf's CicFlsCallback destroying the thread's IME message window with
// HCBT_DESTROYWND). Because runtime.exit(0) entered ExitProcess through
// runtime.stdcall while holding runtime.finlock, noActivateCBT's nested
// w32.CallNextHookEx syscall clears g1.syscallsp; cgocallbackg's exitsyscall
// then throws "exitsyscall: syscall frame is no longer valid", and
// fatalthrow's runtime.exit(2) self-deadlocks on runtime.finlock, leaving the
// process wedged with one thread holding the single-instance mutex and
// WebView2 profile (#6705, root cause of #5922 and #6678).
const testNoActivateExitEnv = "MAGPIE_TEST_NOACTIVATE_EXIT"

var procDestroyWindowHookTest = windows.NewLazySystemDLL("user32.dll").NewProc("DestroyWindow")

func init() {
	how := os.Getenv(testNoActivateExitEnv)
	if how == "" {
		return
	}
	runtime.LockOSThread()
	cls, _ := windows.UTF16PtrFromString("STATIC")
	mainTitle, _ := windows.UTF16PtrFromString(wailsMainThread)
	mainW, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(mainTitle)),
		w32.WS_DISABLED, 0, 0, 0, 0, 0, 0, 0, 0)
	if mainW == 0 {
		os.Stderr.WriteString("CreateWindowExW mainW: " + err.Error())
		os.Exit(9)
	}
	title, _ := windows.UTF16PtrFromString("magpie noactivate exit")
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		w32.WS_OVERLAPPEDWINDOW, 0, 0, 100, 100, 0, 0, 0, 0)
	if hwnd == 0 {
		os.Stderr.WriteString("CreateWindowExW: " + err.Error())
		os.Exit(9)
	}
	appWndProc(hwnd, w32.WM_NULL, 0, 0)
	if noActivateHook == 0 {
		os.Stderr.WriteString("noActivateHook was not installed")
		os.Exit(9)
	}
	// An FLS callback that destroys hwnd on ExitProcess's last thread,
	// reproducing msctf!CicFlsCallback -> DestroyThreadMessageWindow ->
	// NtUserDestroyWindow -> DispatchHookW(HCBT_DESTROYWND) -> noActivateCBT.
	k := windows.NewLazySystemDLL("kernel32.dll")
	if err := procDestroyWindowHookTest.Find(); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(9)
	}
	idx, _, err := k.NewProc("FlsAlloc").Call(procDestroyWindowHookTest.Addr())
	if idx == 0xFFFFFFFF {
		os.Stderr.WriteString("FlsAlloc: " + err.Error())
		os.Exit(9)
	}
	if ok, _, err := k.NewProc("FlsSetValue").Call(idx, hwnd); ok == 0 {
		os.Stderr.WriteString("FlsSetValue: " + err.Error())
		os.Exit(9)
	}
	switch how {
	case "unhook":
		unhookNoActivate()
	case "destroy-main":
		appWndProc(mainW, w32.WM_DESTROY, 0, 0)
		procDestroyWindowHookTest.Call(mainW)
	}
	os.Exit(0)
}

func runNoActivateExit(t *testing.T, how string) (*exec.Cmd, *bytes.Buffer, chan error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(), testNoActivateExitEnv+"="+how)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
	return cmd, &stderr, done
}

func TestUnhookNoActivatePreventsExitDeadlock(t *testing.T) {
	// Without unhooking, destroying a window on the hooked thread during
	// ExitProcess's FLS cleanup throws "exitsyscall: syscall frame is no
	// longer valid" and self-deadlocks on runtime.finlock with 1 thread left.
	cmd, stderr, done := runNoActivateExit(t, "hooked")
	select {
	case err := <-done:
		t.Fatalf("exiting with noActivateHook still installed ended (%v, stderr=%q): stand-in did not reproduce the #6705 deadlock", err, stderr.String())
	case <-time.After(2 * time.Second):
	}
	if n := noActivateThreads(uint32(cmd.Process.Pid)); n != 1 {
		t.Fatalf("the process wedged in ExitProcess has %d threads, want 1", n)
	}
	if got := stderr.String(); !strings.Contains(got, "exitsyscall: syscall frame is no longer valid") {
		t.Fatalf("wedged process stderr = %q, want exitsyscall throw", got)
	}

	for _, how := range []string{"unhook", "destroy-main"} {
		t.Run(how, func(t *testing.T) {
			_, stderr, done := runNoActivateExit(t, how)
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s exit ended with %v (stderr=%q), want exit code 0", how, err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: process hung in ExitProcess (stderr=%q)", how, stderr.String())
			}
		})
	}
}

func noActivateThreads(pid uint32) uint32 {
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
