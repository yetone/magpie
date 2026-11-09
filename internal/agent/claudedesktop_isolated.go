package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/desktopiso"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// The isolated Claude Desktop (#5671, baranwang on X): on a Mac magpie can
// run its 3p mode in a folder of its own, desktopdir.Isolated
// (~/Library/Application Support/Claude-Magpie-3p), started by
// ~/Applications/Claude Magpie.app (desktopiso), so the user's own
// Claude.app stays signed in with Anthropic and both run side by side.
// Off by default (settings.DesktopIsolated): a Desktop on magpie stays as
// it was for everyone who doesn't turn it on.

// desktopIsolatedKey ends the isolated Desktop's stash keys, beside the
// usual sets' "" and ".package".
const desktopIsolatedKey = ".isolated"

// desktopIsolatedOn says the user turned the isolated Desktop on; a var so
// a test needs no settings file.
var desktopIsolatedOn = func() bool { return settings.Load().DesktopIsolated }

// desktopIsolatedSet is the isolated Desktop's folders on goos: its
// userData and its 3p folder are one, Claude-Magpie-3p. The language and
// the MCP servers it is given are those of the user's own Desktop (Find's
// Data), as for Claude-3p. false where Desktop can't run so (desktopdir.
// Isolated: only macOS).
func desktopIsolatedSet(goos, home string, getenv func(string) string) (desktopPaths, bool) {
	iso := desktopdir.Isolated(goos, home)
	if iso == "" {
		return desktopPaths{}, false
	}
	p := desktopPathsOf(iso, iso)
	p.key = desktopIsolatedKey
	own := desktopdir.Find(goos, home, getenv).Data
	p.app = filepath.Join(own, "config.json")
	p.mcpFrom = filepath.Join(own, "claude_desktop_config.json")
	return p, true
}

// DesktopIsolated is the isolated Desktop as it is here: whether it can
// be (only on a Mac), whether it is on, and the app that starts it, ""
// while magpie hasn't made it.
type DesktopIsolated struct {
	On       bool   `json:"on"`
	Launcher string `json:"launcher,omitempty"`
}

// DesktopIsolatedHere is the isolated Desktop on this computer; false
// where Desktop can't run so.
func DesktopIsolatedHere() (DesktopIsolated, bool) {
	home, _ := os.UserHomeDir()
	if desktopdir.Isolated(desktopdir.OS, home) == "" {
		return DesktopIsolated{}, false
	}
	return DesktopIsolated{On: desktopIsolatedOn(), Launcher: desktopiso.Here(home)}, true
}

// Why the isolated Desktop can't be turned on.
var (
	ErrDesktopIsolatedOS = errors.New("Claude Desktop runs in a folder of its own on macOS only")
	ErrDesktopNoApp      = errors.New("Claude Desktop (Claude.app) isn't installed")
)

// desktopClaudeApp is the user's Claude.app, "" when none is installed; a
// var for tests.
var desktopClaudeApp = func() string {
	for _, b := range macBundles(claudeDesktopApp) {
		if bundleExecutable(b, claudeDesktopApp.id) != "" {
			return b
		}
	}
	return ""
}

// desktopIsoAdd is desktopiso.Add; a test sets it, so no app is
// registered with Launch Services.
var desktopIsoAdd = desktopiso.Add

// saveDesktopIsolated keeps the user's choice; a var for tests.
var saveDesktopIsolated = func(on bool) error {
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	s.DesktopIsolated = on
	return settings.Save(s)
}

// SetDesktopIsolated turns the isolated Desktop on or off for home. On:
// Claude Magpie.app is made, and a Desktop on magpie moves there: the
// user's own Desktop is put back as it was before magpie (desktopOff),
// and Claude-Magpie-3p gets magpie's 3p mode. Off: the other way, and the
// app is taken away; Claude-Magpie-3p itself, with the chats in it, stays.
// It returns the app's path when on.
func SetDesktopIsolated(home string, on bool) (string, error) {
	iso, ok := desktopIsolatedSet(desktopdir.OS, home, os.Getenv)
	if !ok {
		return "", ErrDesktopIsolatedOS
	}
	usual := desktopUsualSets(desktopdir.OS, home, os.Getenv)
	if on {
		app := desktopClaudeApp()
		if app == "" {
			return "", ErrDesktopNoApp
		}
		if err := os.MkdirAll(iso.dir3p, 0o755); err != nil {
			return "", err
		}
		p, err := desktopIsoAdd(home, app, iso.dir3p)
		if err != nil {
			return "", err
		}
		if desktopWiredAny(usual) && !desktopWired(iso) {
			desktopAdoptStash(usual)
			if src := desktopLocaleFrom(usual); src != "" {
				iso.app = src
			}
			if err := desktopOn(iso); err != nil {
				return "", err
			}
			for _, u := range usual {
				if err := desktopOff(u); err != nil {
					return "", err
				}
			}
		}
		if err := saveDesktopIsolated(true); err != nil {
			return "", err
		}
		_ = claude(home).Sync()
		return p, nil
	}
	if desktopWired(iso) {
		desktopAdoptStash(usual)
		src := desktopLocaleFrom(usual)
		for _, u := range usual {
			if src != "" {
				u.app = src
			}
			if err := desktopOn(u); err != nil {
				return "", err
			}
		}
		if err := desktopOff(iso); err != nil {
			return "", err
		}
	}
	if err := saveDesktopIsolated(false); err != nil {
		return "", err
	}
	if _, err := desktopiso.Remove(home); err != nil {
		return "", err
	}
	_ = claude(home).Sync()
	return "", nil
}

// desktopInstance says whether a Claude.app process's command line is the
// Desktop magpie wires: the isolated one's (--user-data-dir on
// Claude-Magpie-3p) while it is on, else one on its own folders (no
// --user-data-dir at all). The other runs too, and isn't magpie's to
// restart.
func desktopInstance(cmd string) bool {
	home, _ := os.UserHomeDir()
	iso := desktopdir.Isolated(desktopdir.OS, home)
	if iso != "" && desktopIsolatedOn() {
		return desktopOnDir(cmd, iso)
	}
	return !strings.Contains(cmd, " --user-data-dir=")
}

// desktopOnDir says cmd runs with --user-data-dir set to dir.
func desktopOnDir(cmd, dir string) bool {
	i := strings.Index(cmd, " --user-data-dir="+dir)
	if i < 0 {
		return false
	}
	rest := cmd[i+len(" --user-data-dir=")+len(dir):]
	return rest == "" || rest[0] == ' '
}

// desktopIsoQuit asks the isolated Desktop, whose main executable is exe,
// to quit: SIGTERM to that process, which Electron takes as a quit
// (before-quit, will-quit; ~2s on the Mac mini). macOS's quit by bundle
// can't tell the two Desktops apart, both being Claude.app. A var for
// tests.
var desktopIsoQuit = func(ctx context.Context, exe, dir string) error {
	pat := "^" + regexp.QuoteMeta(exe) + " .*--user-data-dir=" + regexp.QuoteMeta(dir) + "( |$)"
	return proc.CommandContext(ctx, "/usr/bin/pkill", "-TERM", "-f", pat).Run()
}

// reopenIsolated quits the isolated Desktop and opens Claude Magpie.app
// again, which starts it on its folder.
func reopenIsolated(ctx context.Context, a *Agent, r restartable, exe string) error {
	home, _ := os.UserHomeDir()
	launcher := desktopiso.Here(home)
	if launcher == "" {
		return restartErr(noticeRestartNotHere, a)
	}
	if err := desktopIsoQuit(ctx, exe, desktopdir.Isolated(desktopdir.OS, home)); err != nil {
		return restartErr(noticeRestartAsk, a)
	}
	end := time.Now().Add(appQuitWait)
	for {
		if _, ok := findRestartable(r); !ok {
			break
		}
		if time.Now().After(end) {
			return restartErr(noticeRestartStillOpen, a)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(appPoll):
		}
	}
	if err := appOpen(ctx, launcher); err != nil {
		return restartErr(noticeRestartNoOpen, a)
	}
	return nil
}
