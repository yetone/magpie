//go:build windows && !nogui

package gui

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// Go keeps every callback it makes and throws past about 2000, so a read
// that made one each time killed the app after enough display changes
// (1111_13879). 5000 reads stay in one callback.
func TestScreensReadFarPastTheCallbackLimit(t *testing.T) {
	wails, err := w32.GetAllScreens() // one callback of its own
	if err != nil {
		t.Fatal("Wails' read:", err)
	}
	var first []*application.Screen
	for i := range 5000 {
		got, err := allScreens()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if i == 0 {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Fatalf("read %d differs: %+v, then %+v", i, first, got)
		}
	}
	t.Logf("%d screens", len(first))
	if len(first) != len(wails) {
		t.Fatalf("%d screens, Wails reads %d", len(first), len(wails))
	}
	for i, s := range wails {
		want := wailsScreen(s.HMonitor, &s.MONITORINFOEX, s.ScaleFactor)
		if !reflect.DeepEqual(first[i], want) {
			t.Fatalf("screen %d: %+v, Wails reads %+v", i, first[i], want)
		}
	}
}

func TestDisplayChangesAreReadOnWailsWindowOnly(t *testing.T) {
	runtime.LockOSThread() // the windows' thread answers GetWindowText
	defer runtime.UnlockOSThread()
	window := func(title string) w32.HWND {
		h := w32.CreateWindowEx(0, w32.MustStringToUTF16Ptr("STATIC"), w32.MustStringToUTF16Ptr(title), 0, 0, 0, 10, 10, 0, 0, 0, nil)
		if h == 0 {
			t.Fatal("CreateWindowEx failed")
		}
		t.Cleanup(func() { w32.DestroyWindow(h) })
		return h
	}
	wails, other := window(wailsMainThread), window("magpie")
	laid := 0
	was := layoutScreens
	layoutScreens = func([]*application.Screen) error { laid++; return nil }
	t.Cleanup(func() { layoutScreens = was })

	for _, c := range []struct {
		name    string
		hwnd    w32.HWND
		msg     uint32
		wParam  uintptr
		handled bool
	}{
		{"display change", wails, w32.WM_DISPLAYCHANGE, 32, true},
		{"work area", wails, w32.WM_SETTINGCHANGE, w32.SPI_SETWORKAREA, true},
		{"another setting", wails, w32.WM_SETTINGCHANGE, 0, false},
		{"another message", wails, w32.WM_SIZE, 0, false},
		{"another window", other, w32.WM_DISPLAYCHANGE, 32, false},
	} {
		laid = 0
		_, handled := screensWndProc(c.hwnd, c.msg, c.wParam, 0)
		if handled != c.handled || laid != map[bool]int{true: 1}[c.handled] {
			t.Errorf("%s: handled %v, screens laid out %d times", c.name, handled, laid)
		}
	}
	laid = 0
	for range 3000 {
		screensWndProc(wails, w32.WM_DISPLAYCHANGE, 32, 0)
	}
	if laid != 3000 {
		t.Fatalf("3000 display changes laid the screens out %d times", laid)
	}
}

func TestAppReadsDisplayChangesItself(t *testing.T) {
	if windowsOptions().WndProcInterceptor == nil {
		t.Fatal("the app doesn't take display changes before Wails does")
	}
}
