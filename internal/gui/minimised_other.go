//go:build !windows && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// showsMinimised: here a window a restart by itself brings back minimised
// is shown quietly instead (#3495 covers Windows).
const showsMinimised = false

func showMinimised(*application.WebviewWindow, bool) {}
