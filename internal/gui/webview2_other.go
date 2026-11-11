//go:build !windows && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// webView2Check: only Windows' windows are WebView2.
func webView2Check(*application.App) bool { return true }
