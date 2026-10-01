//go:build !nogui

package main

import (
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/proc"
)

const hasGUI = true

// the desktop app may have been opened from the Finder, with none of the
// PATH a terminal has
func runGUI(showMain bool, link string) error {
	proc.UserPath()
	gui.Started = ownSignals // once Wails handles them: it quits through OnShutdown
	return gui.Run(version, showMain, link)
}

// runWindow starts the app with its window open on view ("" for the first
// tab).
func runWindow(view string) error {
	gui.OpenView = view
	return runGUI(true, "")
}

// runPanel starts the app with its quick panel open, or toggles the panel
// of the one already running.
func runPanel() error {
	proc.UserPath()
	gui.OpenPanel = true
	gui.Started = ownSignals // once Wails handles them: it quits through OnShutdown
	return gui.Run(version, false, "")
}
