//go:build !windows && !nogui

package gui

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// webView2Check: only Windows' windows are WebView2.
func webView2Check(*application.App) bool { return true }

// webView2Prepare: only Windows' windows are WebView2.
func webView2Prepare() {}

// webView2Failed: Wails exits after this on every OS; say why.
func webView2Failed(_ *application.App, err error) { log.Println("magpie:", err) }
