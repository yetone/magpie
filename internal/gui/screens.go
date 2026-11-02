//go:build !nogui

package gui

// wailsMainThread is the title Wails gives the hidden window its Windows
// message loop runs on. That window is the one whose WM_DISPLAYCHANGE
// Wails answers by reading the screens again (see screens_windows.go);
// TestWailsScreensAreReadOnTheMainThreadWindow fails when an upgrade
// renames it or stops asking the interceptor first.
const wailsMainThread = "__wails_hidden_mainthread"
