package update

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

// ErrCanceled is the administrator's password prompt dismissed.
var ErrCanceled = errors.New("the update was canceled")

// ErrNoAdmin is no administrator to be had: the system has no prompt for
// one, or it couldn't be shown. What was to run as one didn't run.
var ErrNoAdmin = errors.New("no administrator prompt")

// NeedsAdmin reports whether err is magpie not being allowed to change the
// folder it lives in, which the administrator's password gets past.
func NeedsAdmin(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EXDEV)
}

// CanElevate reports whether this system can ask for the administrator's
// password: always on a Mac, with polkit on Linux, not on Windows.
func CanElevate() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		_, err := exec.LookPath("pkexec")
		return err == nil
	}
	return false
}

// Stuck says why the app at bundle cannot be replaced where it is, or ""
// when it can: opened from its disk image, or moved aside by macOS
// (App Translocation) because it was never moved out of Downloads.
func Stuck(bundle string) string {
	switch {
	case strings.Contains(bundle, "/AppTranslocation/"):
		return "translocated"
	case readOnly(bundle):
		return "read-only"
	}
	return ""
}

// run runs a command as administrator; a test stands in for it.
var run = func(name string, args ...string) ([]byte, error) {
	return proc.Command(name, args...).CombinedOutput()
}

// asAdmin runs a shell script as root, after the system's password prompt.
func asAdmin(script string) error { return AsAdmin("magpie is updating itself.", script) }

// AsAdmin runs a shell script as root, after the system's password prompt,
// which says why (on a Mac). ErrCanceled is the prompt dismissed.
func AsAdmin(why, script string) error {
	var out []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		out, err = run("osascript", "-e", `do shell script "`+appleString(script)+`" with prompt "`+appleString(why)+`" with administrator privileges`)
		if err != nil && strings.Contains(string(out), "(-128)") {
			return ErrCanceled
		}
	case "linux":
		out, err = run("pkexec", "/bin/sh", "-c", script)
		if code, ok := err.(*exec.ExitError); ok && (code.ExitCode() == 126 || code.ExitCode() == 127) {
			return ErrCanceled // not authorised, or the dialog closed
		}
	default:
		return errors.New("cannot ask for the administrator's password here")
	}
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
	}
	return err
}

// swapScript moves bundle aside to old and staged into its place, putting
// bundle back if the second move fails. old is kept: the magpie running
// from it still starts programs (Install).
func swapScript(staged, bundle, old string) string {
	s, b, o, d := shellQuote(staged), shellQuote(bundle), shellQuote(old), shellQuote(filepath.Dir(old))
	return "mkdir -p " + d + " && mv " + b + " " + o + " && if mv " + s + " " + b + "; then :; else mv " + o + " " + b + "; exit 1; fi"
}

// ShellQuote quotes s for /bin/sh.
func ShellQuote(s string) string { return shellQuote(s) }

// shellQuote quotes s for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appleString escapes s for an AppleScript string literal.
func appleString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
