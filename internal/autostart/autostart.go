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

// State is whether magpie opens at login, as the system will act on it.
type State string

const (
	// Off: no record, so nothing opens magpie at login.
	Off State = "off"
	// On: the record is there, and nothing the system says holds it back.
	On State = "on"
	// NeedsAllowing: the record is there, but the user switched it off in
	// the system's own settings (on the Mac, System Settings › General ›
	// Login Items › Allow in the Background), so it opens nothing until
	// it is allowed there again; writing the record anew doesn't do that.
	NeedsAllowing State = "allow"
)

// LoginItemsURL opens System Settings' Login Items pane, where a record
// that NeedsAllowing is allowed again.
const LoginItemsURL = "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"

// Enabled reports whether magpie opens at login. A system that can't be
// asked, or that answers something else, is not taken for a no: the
// record's own word stands (On).
func Enabled() State {
	if !enabled() {
		return Off
	}
	if heldBack() {
		return NeedsAllowing
	}
	return On
}

// Set has magpie open at login, or not, as this copy of it; on Linux,
// where a package's own autostart entry starts magpie, as that package's.
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

// Repoint has the record that opens magpie at login open to instead, when
// it opens from: the Windows app installed itself from where it was
// downloaded (update.Settle). Nothing else in it changes, so a record the
// user switched off in Task Manager stays off, and none is made when there
// was none.
func Repoint(from, to string) error { return repoint(from, to) }

// repointed is the Windows Run value v opening to in place of from, with
// its arguments kept; ok is false when v doesn't open from.
func repointed(v, from, to string) (string, bool) {
	v = strings.TrimSpace(v)
	var exe, rest string
	if strings.HasPrefix(v, `"`) {
		i := strings.Index(v[1:], `"`)
		if i < 0 {
			return "", false
		}
		exe, rest = v[1:1+i], v[2+i:]
	} else {
		exe, rest, _ = strings.Cut(v, " ")
		if rest != "" {
			rest = " " + rest
		}
	}
	if exe == "" || !strings.EqualFold(filepath.Clean(exe), filepath.Clean(from)) {
		return "", false
	}
	return `"` + to + `"` + rest, true
}
