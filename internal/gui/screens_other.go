//go:build !windows && !nogui

package gui

// screensWndProc is Windows' only (screens_windows.go).
var screensWndProc func(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool)
