//go:build !darwin && (!linux || !cgo) && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// plainTitlebar: Windows keeps its own title bar.
func plainTitlebar(*application.WebviewWindow) {}

func nameWindow(*application.WebviewWindow, string) {}

func ownFrame(*application.WebviewWindow) {}
