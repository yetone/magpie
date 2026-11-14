//go:build windows && !nogui

package gui

import (
	"log"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A WebView2 window shown while the screen is locked or the display off
// can stay white once the user is back: Chromium's own occlusion tracking
// takes every window as covered then, and a surface first drawn in that
// state may not be drawn again. aquaarboraurum on X saw a white window
// after every restart to update. AutoRestart makes that restart once no
// request is going through magpie and its window has sat unused, as when
// the user is away and the screen locked; a restart by hand, with the
// user there, came up drawn. Not reproduced: the screen of the Windows box
// the tests run on can't be locked for them. magpie's windows are drawn
// again when the session is unlocked or the display comes on (mendOnWake),
// as closing the window to the tray and opening it again did by hand, and
// windowsOptions turns Chromium's occlusion tracking off
// (webViewOcclusion).

// The messages that say the screen is back (WinUser.h, WtsApi32.h).
const (
	wmWTSSessionChange     = 0x02B1
	wtsSessionUnlock       = 0x8
	wmPowerBroadcast       = 0x0218
	pbtPowerSettingChange  = 0x8013
	notifyForThisSession   = 0
	deviceNotifyWindowHndl = 0
)

// guidConsoleDisplayState is GUID_CONSOLE_DISPLAY_STATE: the display off
// (0), on (1) or dimmed (2).
var guidConsoleDisplayState = windows.GUID{Data1: 0x6fe69556, Data2: 0x704a, Data3: 0x47a0,
	Data4: [8]byte{0x8f, 0x24, 0xc2, 0x8d, 0x93, 0x6f, 0xda, 0x47}}

// powerBroadcastSetting is POWERBROADCAST_SETTING with the display state's
// DWORD as its data.
type powerBroadcastSetting struct {
	PowerSetting windows.GUID
	DataLength   uint32
	Data         uint32
}

var (
	procWTSRegisterSessionNotification   = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSRegisterSessionNotification")
	procRegisterPowerSettingNotification = windows.NewLazySystemDLL("user32.dll").NewProc("RegisterPowerSettingNotification")
	procFindWindowExW                    = windows.NewLazySystemDLL("user32.dll").NewProc("FindWindowExW")
)

// displayOff: the display was last said to be off.
var displayOff atomic.Bool

// watchScreenBack has Wails' main-thread window told when the session is
// unlocked and when the display goes off or on; on the main thread, once.
func watchScreenBack() {
	hwnd := ownMainThreadWindow()
	if hwnd == 0 {
		log.Println("webview: Wails' main-thread window not found; an unlock doesn't draw the windows again")
		return
	}
	if r, _, err := procWTSRegisterSessionNotification.Call(hwnd, notifyForThisSession); r == 0 {
		log.Printf("webview: an unlock doesn't draw the windows again: %v", err)
	}
	if r, _, err := procRegisterPowerSettingNotification.Call(hwnd, uintptr(unsafe.Pointer(&guidConsoleDisplayState)), deviceNotifyWindowHndl); r == 0 {
		log.Printf("webview: the display coming on doesn't draw the windows again: %v", err)
	}
}

// ownMainThreadWindow is this process's Wails main-thread window: another
// magpie's has the same title, during a restart's handover too.
func ownMainThreadWindow() uintptr {
	title, err := windows.UTF16PtrFromString(wailsMainThread)
	if err != nil {
		return 0
	}
	pid := windows.GetCurrentProcessId()
	for after := uintptr(0); ; {
		w, _, _ := procFindWindowExW.Call(0, after, 0, uintptr(unsafe.Pointer(title)))
		if w == 0 {
			return 0
		}
		var p uint32
		if _, err := windows.GetWindowThreadProcessId(windows.HWND(w), &p); err == nil && p == pid {
			return w
		}
		after = w
	}
}

// screenBackFrom says why msg brings the screen back ("" when it doesn't):
// the session unlocked, or the display on after it was off. Windows sends
// the display's state as it is on registering, which, on, is no change.
func screenBackFrom(msg uint32, wParam, lParam uintptr) string {
	switch {
	case msg == wmWTSSessionChange && wParam == wtsSessionUnlock:
		return "the session was unlocked"
	case msg == wmPowerBroadcast && wParam == pbtPowerSettingChange && lParam != 0:
		// lParam is a pointer Windows owns for the message's length.
		s := *(**powerBroadcastSetting)(unsafe.Pointer(&lParam))
		if s.PowerSetting != guidConsoleDisplayState || s.DataLength < 4 {
			return ""
		}
		switch s.Data {
		case 0:
			displayOff.Store(true)
		case 1:
			if displayOff.Swap(false) {
				return "the display came on"
			}
		}
	}
	return ""
}

// appWndProc is the app's WndProcInterceptor (windowsOptions): a window
// not being shown for the user isn't activated (hookNoActivate), the screen
// coming back draws the windows again, and the screens are read
// (screensWndProc). None keeps a message from Wails but the screens'.
var appWndProc = func(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	hookNoActivate(hwnd)
	if why := screenBackFrom(msg, wParam, lParam); why != "" {
		if f := screenBack.Load(); f != nil {
			(*f)(why)
		}
	}
	return screensWndProc(hwnd, msg, wParam, lParam)
}
