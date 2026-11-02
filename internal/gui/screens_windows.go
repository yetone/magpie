//go:build windows && !nogui

package gui

import (
	"errors"
	"log"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// Wails reads the screens again on every WM_DISPLAYCHANGE, and on a
// WM_SETTINGCHANGE for the work area, through w32.GetAllScreens. That
// makes a new Win32 callback each time (syscall.NewCallback over a
// closure), and Go never frees one: past about 2000 the runtime throws
// "too many callback functions", which nothing can recover, and the app
// and the gateway it serves die (1111_13879 on Discord, after a few hours
// of display changes). Wails 3.1.0 and the 3.2 nightlies still do it.
//
// screensWndProc takes those two messages on Wails' main-thread window
// before Wails does, and reads the screens with one callback made once
// for the process. Wails' own read at start-up is its only one left.

// screensWndProc is the app's WndProcInterceptor (windowsOptions).
var screensWndProc = rereadScreens

// layoutScreens hands the screens read to Wails, as its own read does.
var layoutScreens = func(s []*application.Screen) error {
	app := application.Get()
	if app == nil {
		return errors.New("no app")
	}
	return app.Screen.LayoutScreens(s)
}

// screensChanged says whether Wails would read the screens again for msg.
func screensChanged(msg uint32, wParam uintptr) bool {
	return msg == w32.WM_DISPLAYCHANGE || msg == w32.WM_SETTINGCHANGE && wParam == w32.SPI_SETWORKAREA
}

func rereadScreens(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	if !screensChanged(msg, wParam) || w32.GetWindowText(hwnd) != wailsMainThread {
		return 0, false
	}
	screens, err := allScreens()
	if err == nil {
		err = layoutScreens(screens)
	}
	if err != nil {
		// Wails hands its own failure to the app's error handler, which
		// logs it; the screens it had stay
		log.Printf("screens: %v", err)
	}
	// What Wails answers after its read: WM_DISPLAYCHANGE goes on to
	// DefWindowProc, and every WM_SETTINGCHANGE is answered 0 (its other
	// use, ImmersiveColorSet, never comes with SPI_SETWORKAREA).
	if msg == w32.WM_DISPLAYCHANGE {
		return w32.DefWindowProc(hwnd, msg, wParam, lParam), true
	}
	return 0, true
}

// The one EnumDisplayMonitors callback, and what it reads into, under
// screensMu. The callback runs on the caller's thread, inside the call.
var (
	screensMu    sync.Mutex
	screensRead  []*application.Screen
	screensErr   error
	screensEnumF = syscall.NewCallback(func(hMonitor, _ uintptr, _ *w32.RECT, _ uintptr) uintptr {
		info := w32.MONITORINFOEX{MONITORINFO: w32.MONITORINFO{CbSize: uint32(unsafe.Sizeof(w32.MONITORINFOEX{}))}}
		if !w32.GetMonitorInfoEx(hMonitor, &info) {
			screensErr = errors.New("GetMonitorInfo failed")
			return 0
		}
		var dpiX, dpiY uint
		if w32.GetDPIForMonitor(hMonitor, w32.MDT_EFFECTIVE_DPI, &dpiX, &dpiY) != w32.S_OK {
			screensErr = errors.New("GetDpiForMonitor failed")
			return 0
		}
		screensRead = append(screensRead, wailsScreen(hMonitor, &info, float32(dpiX)/96))
		return 1
	})
)

// allScreens is w32.GetAllScreens with the callback made once.
func allScreens() ([]*application.Screen, error) {
	screensMu.Lock()
	defer screensMu.Unlock()
	screensRead, screensErr = nil, nil
	ok := w32.EnumDisplayMonitors(0, nil, screensEnumF, nil)
	screens, err := screensRead, screensErr
	screensRead, screensErr = nil, nil
	if !ok {
		if err == nil {
			err = errors.New("EnumDisplayMonitors failed")
		}
		return nil, err
	}
	return screens, nil
}

// wailsScreen is one monitor as Wails' processAndCacheScreens makes it.
func wailsScreen(hMonitor uintptr, info *w32.MONITORINFOEX, scale float32) *application.Screen {
	m, w := info.RcMonitor, info.RcWork
	x, y := int(m.Left), int(m.Top)
	width, height := int(m.Right)-x, int(m.Bottom)-y
	work := application.Rect{X: int(w.Left), Y: int(w.Top), Width: int(w.Right - w.Left), Height: int(w.Bottom - w.Top)}
	return &application.Screen{
		ID:               strconv.Itoa(int(hMonitor)),
		Name:             syscall.UTF16ToString(info.SzDevice[:]),
		X:                x,
		Y:                y,
		Size:             application.Size{Width: width, Height: height},
		Bounds:           application.Rect{X: x, Y: y, Width: width, Height: height},
		PhysicalBounds:   application.Rect{X: x, Y: y, Width: width, Height: height},
		WorkArea:         work,
		PhysicalWorkArea: work,
		IsPrimary:        info.DwFlags == w32.MONITORINFOF_PRIMARY,
		ScaleFactor:      scale,
	}
}
