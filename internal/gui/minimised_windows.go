//go:build windows && !nogui

package gui

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// showsMinimised: Windows can put a window not shown yet straight onto the
// taskbar, to restore at its place and size (#3495).
const showsMinimised = true

// wpfRestoreToMaximized is WINDOWPLACEMENT's WPF_RESTORETOMAXIMIZED: the
// window, restored from the taskbar, comes back maximised.
const wpfRestoreToMaximized = 0x0002

// showMinimised puts w, placed and not shown yet, onto the taskbar as a
// window minimised there: neither shown on screen nor activated, so the
// app the user is in keeps the keyboard. Wails' Show (SW_SHOW) would
// activate it, and so does SW_SHOWMINNOACTIVE, which quietly turns
// down. Restored, it comes back where it was put, maximised if maximise,
// its page shown (TestRestartMinimisedComesBackMinimised). On the main
// thread.
func showMinimised(w *application.WebviewWindow, maximise bool) {
	hwnd := w32.HWND(uintptr(w.NativeWindow()))
	wp := w32.WINDOWPLACEMENT{}
	wp.Length = uint32(unsafe.Sizeof(wp))
	if hwnd == 0 || !w32.GetWindowPlacement(hwnd, &wp) {
		showHere(w, false) // shown quietly, as before #3495
		if maximise {
			w.Maximise()
		}
		return
	}
	wp.ShowCmd = w32.SW_SHOWMINNOACTIVE
	if maximise {
		wp.Flags |= wpfRestoreToMaximized
	} else {
		wp.Flags &^= wpfRestoreToMaximized
	}
	var placed bool
	quietly(uintptr(hwnd), func() { placed = w32.SetWindowPlacement(hwnd, &wp) })
	if !placed {
		showHere(w, false)
		if maximise {
			w.Maximise()
		}
	}
}
