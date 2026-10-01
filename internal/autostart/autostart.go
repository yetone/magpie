// Package autostart opens magpie when the user logs in, in the tray with
// no window, so the gateway every agent is pointed at is there from the
// start. Whether it does is the system's own record — a launch agent on
// the Mac, a Run value on Windows, an autostart entry on Linux — not a
// setting of magpie's, so it can't say one thing while the system does
// another.
package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Arg is what magpie is started with at login: the tray, no window.
const Arg = "tray"

// Enabled reports whether magpie opens at login.
func Enabled() bool { return enabled() }

// Set has magpie open at login, or not, as this copy of it.
func Set(on bool) error {
	if !on {
		return disable()
	}
	exe, err := self()
	if err != nil {
		return err
	}
	return enable(exe)
}

// Refresh brings the system's record up to date when magpie opens at
// login, for a record an older version wrote; it never turns it on.
func Refresh() error { return refresh() }

// self is the program to start: this one, where it will be at login.
func self() (string, error) {
	// an AppImage runs from a folder mounted anew each time; the file
	// itself is where it's kept
	if p := os.Getenv("APPIMAGE"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	// a Mac app opened where it was downloaded runs from a copy the system
	// makes somewhere random, gone at the next login
	if strings.Contains(exe, "/AppTranslocation/") {
		return "", errors.New("move magpie to Applications first, then turn this on")
	}
	return exe, nil
}
