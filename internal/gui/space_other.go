//go:build !darwin && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// Spaces are the Mac's: elsewhere a window is shown where it was.
func setMovesToActiveSpace(*application.WebviewWindow, bool) {}

// Only the Mac's tray puts the panel at a level of its own.
func setPanelLevel(*application.WebviewWindow) {}

func windowOpen(w *application.WebviewWindow) bool { return w.IsVisible() && !w.IsMinimised() }

// Elsewhere a window is visible covered or minimised alike.
func windowUp(w *application.WebviewWindow) bool { return w.IsVisible() }

func activateApp() {}

func watchHide() {}
