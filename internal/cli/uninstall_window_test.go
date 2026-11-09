//go:build !nogui

package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/applog"
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/shortcut"
)

// Windows' Installed apps runs magpie's Uninstall with no console
// (shortcut.UninstallArgs): the terminal's `magpie uninstall` would refuse
// for want of one to ask on, so it opens the app's window at Settings ›
// Uninstall magpie, which asks, and puts every agent back before magpie's
// files go. Nothing is removed by the command itself.
func TestInstalledAppsUninstallOpensTheWindow(t *testing.T) {
	was, wasRun := openApp, runUninstall
	t.Cleanup(func() { openApp, runUninstall = was, wasRun; gui.OpenUninstall = false; applog.Stop() })
	var shown []bool
	openApp = func(show bool, link string) error {
		if link != "" {
			t.Errorf("opened on a link %q", link)
		}
		shown = append(shown, show && gui.OpenUninstall)
		return nil
	}
	runUninstall = nil // called, it panics: the window asks, not this
	if err := run(shortcut.UninstallArgs); err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || !shown[0] {
		t.Fatalf("the app's window wasn't opened on Uninstall magpie: %v", shown)
	}
}
