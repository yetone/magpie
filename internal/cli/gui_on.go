//go:build !nogui

package cli

import (
	"log"

	"github.com/yetone/magpie/internal/applog"
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/proc"
)

const hasGUI = true

// runGUI starts the desktop app, writing its log to a file (startLog).
func runGUI(showMain bool, link string) error {
	startLog("app")
	return openApp(showMain, link)
}

// openApp starts the desktop app; a var so a test can stand in for it.
// The app may have been opened from the Finder, with none of the PATH a
// terminal has.
var openApp = func(showMain bool, link string) error {
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

// runUninstallWindow starts the app with its window on Settings ›
// Uninstall magpie, asking, or brings the running one's there: what
// Windows' Installed apps runs for magpie's Uninstall.
func runUninstallWindow() error {
	gui.OpenUninstall = true
	return runGUI(true, "")
}

// runPanel starts the app with its quick panel open, or toggles the panel
// of the one already running.
func runPanel() error {
	startLog("panel")
	return openPanel()
}

// openPanel starts the app with its panel open; a var so a test can stand
// in for it.
var openPanel = func() error {
	proc.UserPath()
	gui.OpenPanel = true
	gui.Started = ownSignals // once Wails handles them: it quits through OnShutdown
	return gui.Run(version, false, "")
}

// startLog has the app write its log to a file as well as stderr
// (applog): opened from the Finder, the Dock or the Start menu it has no
// terminal, and what it logged went nowhere (rayem1w on X). A var so a
// test can tell runGUI starts it without starting the app.
var startLog = func(how string) {
	if _, err := applog.Start(version, how); err != nil {
		log.Println("log:", err)
	}
}
