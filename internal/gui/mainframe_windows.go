//go:build windows && !nogui

package gui

import (
	"sync/atomic"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// The frameless main window (mainFrameless) keeps Windows' own resize
// border on its left, right and bottom. Puremilk_chun on X: after 0.1.4274
// the window couldn't be resized. Wails' frameless window answers
// WM_NCCALCSIZE with the whole window as its client area, so nothing is
// left of the frame, and only Wails' page script resizes it: a band
// SM_CXSIZEFRAME pixels wide (4 at 100%) inside the page's edge, which on
// the right is under the page's scrollbar and does nothing. Windows
// 11 is resized from the invisible band outside a window's visible edge,
// where 0.1.4273's framed window answered HTRIGHT and the like; on 4281
// it answered nothing and a drag there did nothing (tried on the Windows
// box). mainFrameCalc lets Windows work out the frame as for any framed
// window, then puts the client area's top back at the window's top, so
// the header is still the title bar and the page runs to the top edge;
// the top is resized by Wails' band inside the page. Maximised, the window
// has no frame to keep and Wails fits it to the work area; full screen it
// has no WS_THICKFRAME.

// mainHwnd is the main window's handle once it is made; 0 before.
var mainHwnd atomic.Uintptr

// mainFrameCalc answers WM_NCCALCSIZE for the main window (appWndProc).
func mainFrameCalc(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	if msg != w32.WM_NCCALCSIZE || wParam == 0 || hwnd == 0 || hwnd != mainHwnd.Load() {
		return 0, false
	}
	if uint32(w32.GetWindowLong(hwnd, w32.GWL_STYLE))&w32.WS_THICKFRAME == 0 || w32.IsZoomed(hwnd) {
		return 0, false
	}
	r := *(**w32.RECT)(unsafe.Pointer(&lParam)) // rgrc[0], the window rect, first in NCCALCSIZE_PARAMS
	top := r.Top
	w32.DefWindowProc(hwnd, msg, wParam, lParam)
	r.Top = top
	return 0, true
}

// keepResizeFrame has the main window w keep its resize border: it is told
// apart from magpie's other windows by its handle, and its frame worked
// out again once the handle is known. That waits for the page to be
// ready: the first main window is made before Wails' main loop runs.
func keepResizeFrame(w *application.WebviewWindow) {
	w.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		application.InvokeAsync(func() {
			p := uintptr(w.NativeWindow())
			if p == 0 || mainHwnd.Swap(p) == p {
				return
			}
			w32.SetWindowPos(p, 0, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_FRAMECHANGED)
		})
	})
}
