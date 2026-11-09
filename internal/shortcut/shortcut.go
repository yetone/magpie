// Package shortcut puts magpie in the Start menu on Windows. There it is
// one exe with no installer, so nothing else would: the first start of the
// desktop app makes a Magpie shortcut in the user's Start menu opening the
// exe it runs as, and Windows search finds it by that. It is made once:
// magpie remembers it did, and a shortcut the user deleted stays deleted
// (#508); one still there is pointed at the exe again when that was moved.
// A portable magpie (a data folder beside it) makes none and registers
// nothing, leaving no trace on the machine. Elsewhere it does nothing —
// the Mac has the app in Applications, Linux its own ways.
package shortcut

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// Name is what the shortcut is called, and what Windows search finds.
const Name = "Magpie"

// StateName is the file in magpie's folder recording that the shortcut was
// made, and for which exe.
const StateName = "start-menu-shortcut"

// what Ensure asks of the system; the tests put fakes in
var (
	// startMenu is where the shortcut goes (…\Start Menu\Programs\Magpie.lnk)
	startMenu = startMenuLink
	// writeLink has the shortcut at lnk open exe, and reports whether it had to
	writeLink = writeShortcut
	// register has Win+R and `start magpie` find exe
	register = registerAppPath
	// registered reports whether a magpie registered itself there before
	registered = registeredAppPath
)

// Ensure makes the shortcut on the first start, keeps it pointed at this
// magpie after that while it is there, and never makes it again once the
// user deleted it. A portable magpie makes none. It fails quietly — magpie
// runs the same without one — and is skipped with MAGPIE_NO_SHORTCUT=1
// (tests, sandboxes).
func Ensure() {
	if os.Getenv("MAGPIE_NO_SHORTCUT") == "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if err := ensure(Target(exe), appdir.Portable(), filepath.Join(appdir.Config(), StateName)); err != nil {
		log.Println("start menu shortcut:", err)
	}
}

// errNoStartMenu is startMenu's answer where there is none (off Windows).
var errNoStartMenu = errors.New("no Start menu here")

// ensure is Ensure for exe, portable being the data folder of a portable
// magpie ("" installed) and state the file remembering the shortcut was
// made.
func ensure(exe, portable, state string) error {
	if portable != "" {
		return nil
	}
	lnk, err := startMenu()
	if errors.Is(err, errNoStartMenu) {
		return nil
	}
	// a magpie from before the state file registered itself at every start
	before := registered()
	register(exe)
	if err != nil {
		return err
	}
	_, gone := os.Stat(lnk)
	if gone != nil {
		if _, err := os.Stat(state); err == nil {
			// made once already: the user deleted it, and it stays deleted
			return nil
		}
		if before {
			// an older magpie made it at its every start, and it is gone:
			// the user deleted it, so it isn't made again now either
			return remember(state, exe)
		}
	}
	if _, err := writeLink(lnk, exe); err != nil {
		return err
	}
	return remember(state, exe)
}

// remember records in state that the shortcut was made, for exe.
func remember(state, exe string) error {
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		return err
	}
	return os.WriteFile(state, []byte(exe+"\n"), 0o644)
}

// aside is what an update moves the exe it replaces to, or stages the new
// one as: magpie.exe.old, magpie.exe.old-2…, magpie.exe.new
var aside = regexp.MustCompile(`(?i)(\.exe)\.(old(-\d+)?|new)$`)

// Target is what the shortcut opens for exe: exe itself, but for a magpie
// left running from where an update moved it aside, the exe in its place,
// which is what the next start runs. An update replaces the exe where it
// is, so the shortcut stays good through it.
func Target(exe string) string {
	return aside.ReplaceAllString(exe, "$1")
}

// Stale reports whether a shortcut opening target in dir is not one for
// exe: there is none yet, or the exe was moved. Windows paths are the same
// in any case.
func Stale(target, dir, exe string) bool {
	same := func(a, b string) bool {
		return a != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return !same(target, exe) || !same(dir, filepath.Dir(exe))
}

// UninstallArgs are what Windows' Installed apps runs magpie with for its
// Uninstall: the app's window on Settings › Uninstall magpie, which asks
// first and puts every agent back before magpie's files go. It is started
// with no console, so the terminal's `magpie uninstall` couldn't ask.
var UninstallArgs = []string{"uninstall", "--window"}

// UninstallCommand is the command line Installed apps runs for exe.
func UninstallCommand(exe string) string {
	return `"` + exe + `" ` + strings.Join(UninstallArgs, " ")
}

// what Remove asks of the system besides startMenu; the tests put fakes in
var unregister = unregisterAppPath

// Remove takes out what Ensure made, for magpie uninstall: the Start menu
// shortcut, the App Paths and Installed apps entries while they name exe
// (or an exe gone), and
// the record of having made them. A portable magpie made none, and an
// entry another copy of magpie registered stays that copy's.
func Remove(exe string) error {
	var errs []error
	if lnk, err := startMenu(); err == nil {
		if err := os.Remove(lnk); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	} else if !errors.Is(err, errNoStartMenu) {
		errs = append(errs, err)
	}
	if err := unregister(Target(exe)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
