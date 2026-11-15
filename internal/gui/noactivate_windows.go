//go:build windows && !nogui

package gui

import (
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// A magpie window asks Windows for the foreground only when it is shown
// for the user. shangyuan0818 on X: after a restart to update by itself
// (Settings → AutoRestart), magpie's taskbar button flashed orange while
// they were in another app. Wails activates every window as it makes it,
// hidden ones too: the panel's frame change (Frameless) and the main
// window's centring are SetWindowPos calls without SWP_NOACTIVATE. The
// new magpie, started in the background by the old one, may not take the
// foreground, and Windows answers that ask by flashing the button until
// it is clicked; where it may, a hidden window took the keyboard from the
// app the user was in. The main window a restart by itself brings back
// was then shown with SW_SHOW, which activates it too.
//
// A hook on the thread Wails' windows live on turns those activations
// down: of a window not shown, and of one being shown quietly
// (showQuietly), or put on the taskbar (showMinimised: SW_SHOWMINNOACTIVE
// activates it all the same). SWP_NOACTIVATE added to the moves' WINDOWPOS doesn't
// stop them (tried on the Windows box). TestRestartBySelfAsksNoAttention.

const (
	whCBT         = 5
	hcbtActivate  = 5
	gaParent      = 1
	wailsWndClass = "WailsWebviewWindow"
)

var procGetAncestor = windows.NewLazySystemDLL("user32.dll").NewProc("GetAncestor")

// showingQuietly is the window being shown without activating it, 0 for
// none; on the main thread.
var showingQuietly atomic.Uintptr

var (
	noActivateOnce   sync.Once
	noActivateHooked atomic.Bool
	noActivateHook   atomic.Uintptr // the hook's handle, 0 once unhooked
)

// hookNoActivate puts the hook on hwnd's thread the first time a window
// of Wails' sees a message there: Wails' main thread, before a window it
// makes is moved.
func hookNoActivate(hwnd uintptr) {
	if hwnd == 0 || noActivateHooked.Load() {
		return
	}
	if tid, _ := w32.GetWindowThreadProcessId(hwnd); tid != w32.GetCurrentThreadId() {
		return
	}
	noActivateOnce.Do(func() {
		noActivateHook.Store(uintptr(w32.SetWindowsHookEx(whCBT, noActivateCBT, 0, w32.DWORD(w32.GetCurrentThreadId()))))
		noActivateHooked.Store(true)
	})
}

// unhookNoActivate takes the WH_CBT hook off Wails' main thread before the
// process exits (#6705): left on while ExitProcess runs LdrShutdownProcess,
// msctf's FLS cleanup destroys the thread's IME message window, which
// dispatches HCBT_DESTROYWND into noActivateCBT while g1 is inside
// runtime.exit(0)'s stdcall. noActivateCBT's CallNextHookEx syscall clears
// g1.syscallsp, so cgocallbackg's exitsyscall throws "exitsyscall: syscall
// frame is no longer valid", and fatalthrow's runtime.exit(2) self-deadlocks
// on runtime.finlock, leaving the process wedged with one thread holding the
// single-instance mutex and WebView2 profile (#5922, #6678). Once taken
// off it stays off: hookNoActivate hooks once a run (noActivateOnce). The
// handle is swapped, so of OnShutdown's, WM_DESTROY's and Run's calls
// only the first unhooks.
func unhookNoActivate() {
	if h := noActivateHook.Swap(0); h != 0 {
		w32.UnhookWindowsHookEx(w32.HHOOK(h))
	}
}

func noActivateCBT(code int, wParam w32.WPARAM, lParam w32.LPARAM) w32.LRESULT {
	if code == hcbtActivate && quietActivation(wParam) {
		return 1
	}
	return w32.CallNextHookEx(w32.HHOOK(noActivateHook.Load()), code, wParam, lParam)
}

// quietActivation says hwnd, about to be activated, is a magpie window
// that mustn't be: hidden, or being shown quietly. Wails' message-only
// window (the tray's, which its menu brings forward) and its disabled
// main-thread window are left alone.
func quietActivation(hwnd uintptr) bool {
	if hwnd == 0 || w32.GetClassName(hwnd) != wailsWndClass {
		return false
	}
	if hwnd == showingQuietly.Load() {
		return true
	}
	if w32.IsWindowVisible(hwnd) {
		return false
	}
	if parent, _, _ := procGetAncestor.Call(hwnd, gaParent); parent != w32.GetDesktopWindow() {
		return false
	}
	return uint32(w32.GetWindowLong(hwnd, w32.GWL_STYLE))&w32.WS_DISABLED == 0
}

// showQuietly shows w without activating it, behind the app the user is
// in; on the main thread.
func showQuietly(w *application.WebviewWindow) {
	quietly(uintptr(w.NativeWindow()), func() { showWebview(w) })
}

// quietly runs show, which shows hwnd, with hwnd's activation turned
// down; on the main thread.
func quietly(hwnd uintptr, show func()) {
	showingQuietly.Store(hwnd)
	defer showingQuietly.Store(0)
	show()
}
