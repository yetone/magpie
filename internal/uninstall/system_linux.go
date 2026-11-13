package uninstall

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// dataHome is where the app's desktop entry, icon and WebKitGTK's data
// are (gui's dataHome).
func dataHome() string {
	if x := appdir.Getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
		return x
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share")
}

// unregisterScheme takes out the desktop entry the app made for magpie://
// links while it runs exe, or an exe that is gone, and its icon with it.
func unregisterScheme(exe string) error {
	entry := filepath.Join(dataHome(), "applications", "magpie.desktop")
	b, err := os.ReadFile(entry)
	if err != nil {
		return nil
	}
	for _, l := range strings.Split(string(b), "\n") {
		if run, ok := strings.CutPrefix(l, "Exec="); ok {
			run = strings.TrimSuffix(run, " %u")
			if run != exe {
				if _, err := os.Stat(run); err == nil {
					return nil // another magpie's
				}
			}
		}
	}
	if err := os.Remove(entry); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dataHome(), "icons", "hicolor", "256x256", "apps", "magpie.png"))
	return nil
}

// webData is WebKitGTK's folder for the app's pages (named after the
// program, gui's programName).
func webData() []string {
	if appdir.Portable() != "" {
		return nil
	}
	return []string{filepath.Join(dataHome(), "magpie")}
}
