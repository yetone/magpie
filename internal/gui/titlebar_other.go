//go:build (!linux || !cgo) && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// plainTitlebar: macOS insets the traffic lights into the page's header
// (MacTitleBarHiddenInset) and Windows keeps its own title bar.
func plainTitlebar(*application.WebviewWindow) {}

func nameWindow(*application.WebviewWindow, string) {}

func ownFrame(*application.WebviewWindow) {}
