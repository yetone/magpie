//go:build windows && !nogui

package gui

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// The windows are real, so this runs where Windows has a desktop the user
// sees: on the Windows box's signed-in session, not over ssh (session 0),
// where a window shown is never visible.
func TestWindowInForeground(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ownPID := windows.GetCurrentProcessId()
	if windowInForeground(0, ownPID) {
		t.Fatal("zero hwnd reported as foreground")
	}

	offscreen := uintptr(^uintptr(20000 - 1))
	cls, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("magpie efficiency test")
	top, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		w32.WS_OVERLAPPEDWINDOW,
		offscreen, offscreen, 120, 80,
		0, 0, 0, 0,
	)
	if top == 0 {
		t.Fatalf("CreateWindowExW top: %v", err)
	}
	t.Cleanup(func() { procDestroyWindow.Call(top) })

	// Hidden top-level window (tray/main-thread/closed window) is not in foreground.
	if windowInForeground(top, ownPID) {
		t.Fatal("hidden window reported as in foreground")
	}

	// Shown without activating offscreen: visible and unminimised -> true for ownPID, false for another PID.
	quietly(top, func() { w32.ShowWindow(w32.HWND(top), w32.SW_SHOWNOACTIVATE) })
	if !windowInForeground(top, ownPID) {
		t.Fatal("visible unminimised window of ownPID not recognised as foreground")
	}
	if windowInForeground(top, ownPID+1) {
		t.Fatal("window recognised as foreground for a different PID")
	}

	// A child control inside the window (such as WebView2's child HWND) resolves to the root window.
	const wsChild = 0x40000000
	const wsVisible = 0x10000000
	child, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		wsChild|wsVisible,
		0, 0, 50, 50,
		top, 0, 0, 0,
	)
	if child == 0 {
		t.Fatalf("CreateWindowExW child: %v", err)
	}
	t.Cleanup(func() { procDestroyWindow.Call(child) })
	if !windowInForeground(child, ownPID) {
		t.Fatal("child control inside visible window not recognised via GA_ROOT")
	}

	// Minimised onto the taskbar -> false.
	quietly(top, func() { w32.ShowWindow(w32.HWND(top), w32.SW_SHOWMINNOACTIVE) })
	if windowInForeground(top, ownPID) {
		t.Fatal("minimised window reported as in foreground")
	}
}

func TestAppWndProcTriggersEfficiencyCheckOnPowerAndActivation(t *testing.T) {
	calls := 0
	check := func() { calls++ }
	was := efficiencyCheck.Load()
	efficiencyCheck.Store(&check)
	t.Cleanup(func() { efficiencyCheck.Store(was) })

	for _, msg := range []uint32{wmPowerBroadcast, wmActivateApp, wmActivate, wmShowWindow, wmSize} {
		before := calls
		appWndProc(0, msg, 0, 0)
		if calls != before+1 {
			t.Fatalf("msg 0x%x: calls = %d, want %d", msg, calls, before+1)
		}
	}

	// Unrelated high-frequency messages do not trigger efficiency checks.
	before := calls
	for _, msg := range []uint32{w32.WM_NULL, w32.WM_MOUSEMOVE, w32.WM_PAINT, w32.WM_TIMER} {
		appWndProc(0, msg, 0, 0)
	}
	if calls != before {
		t.Fatalf("unrelated messages triggered %d checks, want 0", calls-before)
	}
}
