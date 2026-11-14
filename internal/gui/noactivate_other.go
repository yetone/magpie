//go:build !windows && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// showQuietly shows w without taking focus from the app the user is in;
// on the main thread. On macOS makeKeyAndOrderFront doesn't activate a
// background app or bounce its Dock icon. On Linux gtk_window_present
// still asks for focus (#6634).
func showQuietly(w *application.WebviewWindow) { showWebview(w) }
