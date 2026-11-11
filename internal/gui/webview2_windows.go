//go:build windows && !nogui

package gui

import (
	"log"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/settings"
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
