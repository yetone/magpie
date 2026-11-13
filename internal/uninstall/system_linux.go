package uninstall

import (
	"errors"
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
	_, err := removeEntry("magpie-ccswitch.desktop", exe)
	gone, err2 := removeEntry("magpie.desktop", exe)
	if gone {
		_ = os.Remove(filepath.Join(dataHome(), "icons", "hicolor", "256x256", "apps", "magpie.png"))
	}
	return errors.Join(err, err2)
}

// removeEntry takes out one of the app's desktop entries while it runs exe,
// or an exe that is gone.
func removeEntry(name, exe string) (bool, error) {
	entry := filepath.Join(dataHome(), "applications", name)
	b, err := os.ReadFile(entry)
	if err != nil {
		return false, nil
	}
	for _, l := range strings.Split(string(b), "\n") {
		if run, ok := strings.CutPrefix(l, "Exec="); ok {
			run = strings.TrimSuffix(run, " %u")
			if run != exe {
				if _, err := os.Stat(run); err == nil {
					return false, nil // another magpie's
				}
			}
		}
	}
	return true, os.Remove(entry)
}

// webData is WebKitGTK's folder for the app's pages (named after the
// program, gui's programName).
func webData() []string {
	if appdir.Portable() != "" {
		return nil
	}
	return []string{filepath.Join(dataHome(), "magpie")}
}
