//go:build windows && !nogui

package gui

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// framelessTestProc answers as Wails' frameless window does: magpie's
// interceptor first (mainFrameCalc, which appWndProc asks first), then
// WM_NCCALCSIZE with the whole window as the client area.
var framelessTestProc = windows.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
	if r, ok := mainFrameCalc(hwnd, uint32(msg), wParam, lParam); ok {
		return r
	}
	if msg == w32.WM_NCCALCSIZE && wParam != 0 {
		return 0
	}
	return w32.DefWindowProc(hwnd, uint32(msg), wParam, lParam)
})

func framelessTestWindow(t *testing.T) uintptr {
	t.Helper()
	cls, _ := windows.UTF16PtrFromString("magpieFramelessTest")
	wc := w32.WNDCLASSEX{WndProc: framelessTestProc, Instance: w32.GetModuleHandle(""), ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	w32.RegisterClassEx(&wc) // once a process; a second call fails harmlessly
	title, _ := windows.UTF16PtrFromString("magpie frameless test")
	hwnd := w32.CreateWindowEx(0, cls, title, w32.WS_OVERLAPPEDWINDOW, 100, 100, 800, 600, 0, 0, wc.Instance, nil)
	if hwnd == 0 {
		t.Fatalf("CreateWindowEx: %v", windows.GetLastError())
	}
	t.Cleanup(func() { w32.DestroyWindow(hwnd) })
	return hwnd
}

func reframe(hwnd uintptr) {
	w32.SetWindowPos(hwnd, 0, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_FRAMECHANGED)
}

func hitAt(hwnd uintptr, x, y int) uintptr {
	return w32.SendMessage(hwnd, w32.WM_NCHITTEST, 0, uintptr(uint32(uint16(int16(y)))<<16|uint32(uint16(int16(x)))))
}

// Puremilk_chun on X: the frameless main window (0.1.4274 on) couldn't be
// resized from outside its edge. Its left, right and bottom are Windows'
// resize border again, while its top is still the page's (the header is
// the title bar); maximised it fills the window, and any other window is
// left as Wails made it.
func TestMainWindowKeepsResizeBorder(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer mainHwnd.Store(0)

	main := framelessTestWindow(t)
	mainHwnd.Store(main)
	reframe(main)
	r := w32.GetWindowRect(main)
	cx, cy := w32.ClientToScreen(main, 0, 0)
	if cy != int(r.Top) {
		t.Errorf("the page starts %dpx below the window's top, want 0: the header is the title bar", cy-int(r.Top))
	}
	if cx <= int(r.Left) {
		t.Errorf("the page starts at the window's left edge: no resize border is left there")
	}
	midX, midY := int(r.Left+r.Right)/2, int(r.Top+r.Bottom)/2
	for _, c := range []struct {
		name string
		x, y int
		want uintptr
	}{
		{"left", int(r.Left), midY, w32.HTLEFT},
		{"right", int(r.Right) - 1, midY, w32.HTRIGHT},
		{"bottom", midX, int(r.Bottom) - 1, w32.HTBOTTOM},
		{"bottom right", int(r.Right) - 1, int(r.Bottom) - 1, w32.HTBOTTOMRIGHT},
		{"top", midX, int(r.Top) + 1, w32.HTCLIENT},
	} {
		if got := hitAt(main, c.x, c.y); got != c.want {
			t.Errorf("%s edge of the main window hits %d, want %d", c.name, got, c.want)
		}
	}

	w32.ShowWindow(main, w32.SW_MAXIMIZE)
	reframe(main)
	mr, cr := w32.GetWindowRect(main), w32.GetClientRect(main)
	if cr.Right-cr.Left != mr.Right-mr.Left {
		t.Errorf("maximised, the page is %dpx narrower than the window: Wails fits a maximised window", (mr.Right-mr.Left)-(cr.Right-cr.Left))
	}

	other := framelessTestWindow(t)
	reframe(other)
	r = w32.GetWindowRect(other)
	if got := hitAt(other, int(r.Right)-1, int(r.Top+r.Bottom)/2); got != w32.HTCLIENT {
		t.Errorf("another window's right edge hits %d, want HTCLIENT as Wails made it", got)
	}
}
