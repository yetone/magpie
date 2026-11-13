//go:build windows && !nogui

package gui

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
	"golang.org/x/sys/windows"
)

// webView2Check says whether app's windows can be made. Without the
// runtime it logs it and shows the box (webView2Notice), opening the
// download page on Yes, and Run ends: Wails would exit on the first window
// anyway. It is read before app.Run: the lookup reads the registry and
// makes no window. The box can't come from ErrorHandler, which runs on the
// thread Wails makes its windows on (ufec tried it in #1806).
func webView2Check(app *application.App) bool {
	if !webView2Missing(app.Env.Info().PlatformInfo) {
		return true
	}
	log.Printf("magpie: no Microsoft Edge WebView2 Runtime, which magpie's windows need; install it from %s, or use magpie web / magpie tui", webView2Page)
	title, body := webView2Notice(trayLang(settings.Load().Lang, systemLang))
	if webView2Box(title, body) == idYes {
		if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(webView2Page), nil, nil, windows.SW_SHOWNORMAL); err != nil {
			log.Println("magpie: opening the WebView2 page:", err)
		}
	}
	return false
}

const (
	mbYesNo          = 0x00000004
	mbIconError      = 0x00000010
	mbSetForeground  = 0x00010000
	mbTopmost        = 0x00040000
	idYes            = 6
	webView2BoxStyle = mbYesNo | mbIconError | mbSetForeground | mbTopmost
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// webView2Box is Windows' own message box, answering the button pressed.
func webView2Box(title, body string) int {
	r, _, _ := procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(body))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))),
		webView2BoxStyle)
	return int(r)
}

// webView2Dir is the profile folder WebView2 will use.
func webView2Dir() string {
	exe, _ := os.Executable()
	return webView2Folder(appdir.WebView(), os.Getenv("AppData"), exe)
}

// webView2Free says whether no other process holds the profile folder.
// Chromium keeps EBWebView\lockfile open without sharing while its browser
// process runs, so opening it fails with a sharing violation until the
// previous magpie's msedgewebview2 has gone.
func webView2Free(dir string) bool {
	if dir == "" {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, "EBWebView", "lockfile"), os.O_RDWR, 0)
	if err != nil {
		return !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	f.Close()
	return true
}

// webView2Prepare runs before the first window. A start after a failed
// one backs off first; then it waits, up to 30s, for the previous
// process' WebView2 to let go of the profile folder.
func webView2Prepare() {
	try := webView2Try(os.Getenv(webView2TryEnv))
	if try > 0 {
		log.Printf("magpie: WebView2 start %d of %d, after a failure", try+1, webView2Tries)
		time.Sleep(webView2Backoff(try))
	}
	webView2Ready(webView2Dir(), instanceID(), 30*time.Second)
}

// webView2Ready waits, up to max, for no other process to hold the
// profile folder dir. A second launch waits for nothing: the magpie
// running on this config (instance id) holds the folder for as long as it
// runs, and Wails hands the launch over to it as the app is made
// (singleInstance). Waiting there kept the Start menu's magpie from
// showing the window for 30s (shangyuan0818 on X).
func webView2Ready(dir, id string, max time.Duration) {
	if instanceHeld(id) {
		return
	}
	start := time.Now()
	if !awaitWebView2Folder(func() bool { return webView2Free(dir) }, max, 250*time.Millisecond, time.Sleep) {
		log.Printf("magpie: WebView2 folder %s is still held by another process after %s; going on", dir, max)
	} else if d := time.Since(start); d > time.Second {
		log.Printf("magpie: waited %s for WebView2 folder %s to be free", d.Round(100*time.Millisecond), dir)
	}
}

// instanceHeld says whether a magpie with the single instance id runs:
// Wails holds a mutex named for it from the app's making until the
// process ends (its single_instance_windows.go).
func instanceHeld(id string) bool {
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, windows.StringToUTF16Ptr(instanceMutex(id)))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// instanceMutex is the name Wails gives the single instance's mutex.
func instanceMutex(id string) string { return "wails-app-" + id + "-sim" }

// webView2Failed is Wails' ErrorHandler; Wails exits as soon as it
// returns. It logs the error with what is needed to read it, and for a
// WebView2 failure starts magpie again (up to webView2Tries starts), the
// last failure shown in a box with the log's place.
func webView2Failed(app *application.App, err error) {
	dir := webView2Dir()
	ver := "unknown"
	if app != nil {
		ver = fmt.Sprint(app.Env.Info().PlatformInfo["WebView2"])
	}
	log.Printf("magpie: %v (WebView2 runtime %q, profile folder %s, held by another process: %t)", err, ver, dir, !webView2Free(dir))
	if !webView2Failure(err) {
		return
	}
	try := webView2Try(os.Getenv(webView2TryEnv)) + 1
	if try < webView2Tries {
		log.Printf("magpie: starting again (start %d of %d)", try+1, webView2Tries)
		if rerr := update.Respawn(fmt.Sprintf("%s=%d", webView2TryEnv, try)); rerr != nil {
			log.Println("magpie: starting again:", rerr)
		} else {
			return
		}
	}
	title, body := webView2FailedNotice(trayLang(settings.Load().Lang, systemLang), err.Error(), ver)
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(body))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))),
		mbIconError|mbSetForeground|mbTopmost)
}
