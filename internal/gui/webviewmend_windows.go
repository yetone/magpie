//go:build windows && !nogui

package gui

import (
	"errors"
	"fmt"
	"log"
	"math"
	"reflect"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

// onProcessFailed has f called with the kind of each failure of w's
// WebView2 processes (COREWEBVIEW2_PROCESS_FAILED_KIND), on the main
// thread. Wails' edge.Chromium registers for ProcessFailed and calls its
// ProcessFailedCallback, which Wails itself leaves unset; it is reached
// through the fields chromiumPart walks. On the main thread.
func onProcessFailed(w *application.WebviewWindow, f func(kind uint32)) error {
	c := chromiumStruct(w)
	if !c.IsValid() {
		return errors.New("no WebView2 found in this Wails")
	}
	cb := c.FieldByName("ProcessFailedCallback")
	if !cb.IsValid() || cb.Kind() != reflect.Func || cb.Type().NumIn() != 2 || cb.Type().NumOut() != 0 ||
		cb.Type().In(1).Kind() != reflect.Pointer {
		return errors.New("Wails' Chromium has no ProcessFailedCallback of the shape known")
	}
	if !cb.IsNil() {
		return errors.New("Wails handles ProcessFailed itself now")
	}
	cb.Set(reflect.MakeFunc(cb.Type(), func(in []reflect.Value) []reflect.Value {
		kind := uint32(math.MaxUint32)
		if a := in[1]; !a.IsNil() {
			kind = processFailedKind(a.UnsafePointer())
		}
		f(kind)
		return nil
	}))
	return nil
}

// processFailedKind is ICoreWebView2ProcessFailedEventArgs'
// get_ProcessFailedKind (slot 3, after IUnknown's), MaxUint32 when it
// fails.
func processFailedKind(args unsafe.Pointer) uint32 {
	kind := uint32(math.MaxUint32)
	if hr, _, _ := syscall.SyscallN(comMethod(args, 3), uintptr(args), uintptr(unsafe.Pointer(&kind))); int32(hr) < 0 {
		return math.MaxUint32
	}
	return kind
}

// webViewVisible is ICoreWebView2Controller's put_IsVisible (slot 4:
// IUnknown's three, then get_IsVisible), as Wails' Chromium Show and Hide
// call it; on the main thread.
func webViewVisible(w *application.WebviewWindow, visible bool) error {
	c := chromiumPart(w, "controller")
	if c == nil {
		return errors.New("no controller")
	}
	var v uintptr
	if visible {
		v = 1
	}
	if hr, _, _ := syscall.SyscallN(comMethod(c, 4), uintptr(c), v); int32(hr) < 0 {
		return fmt.Errorf("put_IsVisible(%t): %w", visible, windows.Errno(hr))
	}
	return nil
}

// reloadWebView is ICoreWebView2's Reload (slot 31 in ICoreWebView2Vtbl,
// WebView2.h), which loads the page again in a new renderer; on the main
// thread.
func reloadWebView(w *application.WebviewWindow) error {
	wv := chromiumPart(w, "webview")
	if wv == nil {
		return errors.New("no webview")
	}
	const reload = 31
	if hr, _, _ := syscall.SyscallN(comMethod(wv, reload), uintptr(wv)); int32(hr) < 0 {
		return fmt.Errorf("Reload: %w", windows.Errno(hr))
	}
	return nil
}

// chromiumStruct is Wails' edge.Chromium for w, as a value whose fields can
// be set; the zero Value before WebView2 is made or in a Wails without
// that path (chromiumPart's).
func chromiumStruct(w *application.WebviewWindow) reflect.Value {
	if w == nil {
		return reflect.Value{}
	}
	v := reflect.ValueOf(w).Elem()
	for _, name := range []string{"impl", "chromium"} {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}
		}
		v = v.FieldByName(name)
		for v.IsValid() && v.Kind() == reflect.Interface {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	// reached through unexported fields, v can't be set; the same memory
	// taken afresh can
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
}

// awaitBrowserGone waits, up to 15s, for the browser process that ended to
// have let go of the profile folder, and a moment more for its children: a
// controller asked for before then was refused ("not in the correct
// state") and Wails ended the app.
func awaitBrowserGone() {
	dir := webView2Dir()
	if !awaitWebView2Folder(func() bool { return webView2Free(dir) }, 15*time.Second, 250*time.Millisecond, time.Sleep) {
		log.Printf("webview: the profile folder %s is still held; making the window anyway", dir)
	}
	time.Sleep(time.Second)
}
