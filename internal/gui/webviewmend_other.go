//go:build !windows && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// WKWebView and WebKitGTK have no WebView2 process failures to handle
// (webviewmend.go); the Mac lets a window's layers go on its own (#5530).
func onProcessFailed(*application.WebviewWindow, func(uint32)) error { return nil }

func webViewVisible(*application.WebviewWindow, bool) error { return nil }

func reloadWebView(*application.WebviewWindow) error { return nil }

func awaitBrowserGone() {}

// The screen coming back is watched on Windows only (screenback_windows.go).
func watchScreenBack() {}

var appWndProc func(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool)
