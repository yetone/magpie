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

// The isolated Desktop's Code tab runs Claude Code, on ~/.claude like
// any other unless the user gives it a folder of its own (#6459, xMopf7RF
// on X): then Claude Magpie.app starts Desktop with CLAUDE_CONFIG_DIR set
// to desktopIsolatedClaudeDir (desktopiso.Script), which Desktop hands its
// sessions when its own Settings set no such variable (4.23118, 2.7032:
// its bundled Claude Code wrote .claude.json, backups, plugins and
// sessions there, ~/.claude untouched). The windows and capabilities of
// Desktop's models, which magpie writes into the settings.json of the
// Claude Code the Code tab runs (desktopOn in claude.go), go there too,
// and ~/.claude/settings.json gets none of them. The folder starts with
// none of ~/.claude's CLAUDE.md, skills, plugins or history. A Claude
// Magpie made new gets it unless the user chose otherwise
// (desktopIsoClaudeDefault); one made before keeps what it had.

// desktopIsolatedClaudeOn says the user gave the isolated Desktop's Code
// tab a folder of its own; a var so a test needs no settings file.
var desktopIsolatedClaudeOn = func() bool { return settings.Load().DesktopIsolatedClaude }

// desktopIsolatedClaudeChosen says the user set that themselves; a var
// so a test needs no settings file.
var desktopIsolatedClaudeChosen = func() bool { return settings.Load().DesktopIsolatedClaudeChosen }

// saveDesktopIsolatedClaude keeps that folder on or off, and chosen when
// the user set it; a var for tests.
var saveDesktopIsolatedClaude = func(on, chosen bool) error {
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	s.DesktopIsolatedClaude = on
	if chosen {
		s.DesktopIsolatedClaudeChosen = true
	}
	return settings.Save(s)
}

// desktopIsoClaudeDefault gives a Claude Magpie made new a Claude Code
// folder of its own, like a phone's app twin (xMopf7RF on X): its Code
// tab then reads and writes nothing of ~/.claude. Made new is the app
// off and no Claude-Magpie-3p yet; one made before, with chats in that
// folder, keeps the ~/.claude its Code tab had, and a choice the user
// made either way stays theirs.
func desktopIsoClaudeDefault(iso desktopPaths) error {
	if desktopIsolatedOn() || isDir(iso.dir3p) || desktopIsolatedClaudeOn() || desktopIsolatedClaudeChosen() {
		return nil
	}
	return saveDesktopIsolatedClaude(true, false)
}

// desktopIsolatedFrom marks a Claude Magpie that the user's own Desktop's
// wiring moved into when it was turned on: turning it off moves that
// back. One made beside a Desktop not on magpie (the twin) leaves the
// user's own Desktop off magpie when it goes.
const desktopIsolatedFrom = "claude-desktop.isolated-from-own"

// desktopIsolatedClaudeDir is that folder: ~/.claude-magpie-desktop,
// beside ~/.claude, so it is a Claude Code row of its own on the Agents
// page (claudeDirAgents) once it holds a settings.json.
func desktopIsolatedClaudeDir(home string) string {
	return filepath.Join(home, ".claude-magpie-desktop")
}

// desktopCodeDir is the Claude Code folder Desktop's Code tab runs on
// when it isn't the usual one: the isolated Desktop's own while both it
// and its folder are on, else "".
func desktopCodeDir(home string) string {
	if desktopdir.Isolated(desktopdir.OS, home) == "" || !desktopIsolatedOn() || !desktopIsolatedClaudeOn() {
		return ""
	}
	return desktopIsolatedClaudeDir(home)
}

// desktopIsoClaudeArg is the folder Claude Magpie.app is made with: its
// own while the user turned it on, "" for ~/.claude.
func desktopIsoClaudeArg(home string) string {
	if desktopIsolatedClaudeOn() {
		return desktopIsolatedClaudeDir(home)
	}
	return ""
}

// desktopClaudeRow is the Claude Code row of the isolated Desktop's
// folder, as claudeDirAgents makes it.
func desktopClaudeRow(home string) *Agent {
	dir := desktopIsolatedClaudeDir(home)
	return claudeIn(place{home: home, id: "claude#" + claudeDirName(filepath.Base(dir)), dir: dir})
}

// desktopClaudeSync writes the windows and capabilities of Desktop's
// models where its Code tab reads them now, and takes them out of the
// other folder: ~/.claude's Claude Code, and the isolated Desktop's own
// where it has one.
func desktopClaudeSync(home string) {
	_ = claude(home).Sync()
	if isDir(desktopIsolatedClaudeDir(home)) {
		_ = desktopClaudeRow(home).Sync()
	}
}

// desktopIsoClaudeMake makes the isolated Desktop's Claude Code folder,
// with an empty settings.json, which makes it a Claude Code's folder to
// the Agents page and gives magpie the file to write; one there already
// is left as it is.
func desktopIsoClaudeMake(home string) error {
	dir := desktopIsolatedClaudeDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f := filepath.Join(dir, "settings.json")
	if _, err := os.Lstat(f); errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(f, []byte("{}\n"), 0o600)
	}
	return nil
}

// DesktopIsolated is the isolated Desktop as it is here: whether it can
// be (only on a Mac), whether it is on, and the app that starts it, ""
// while magpie hasn't made it. Claude is whether its Code tab has a
// Claude Code folder of its own, ClaudeDir that folder.
type DesktopIsolated struct {
	On        bool   `json:"on"`
	Launcher  string `json:"launcher,omitempty"`
	Claude    bool   `json:"claude"`
	ClaudeDir string `json:"claudeDir"`
}

// DesktopIsolatedHere is the isolated Desktop on this computer; false
// where Desktop can't run so.
func DesktopIsolatedHere() (DesktopIsolated, bool) {
	home, _ := os.UserHomeDir()
	if desktopdir.Isolated(desktopdir.OS, home) == "" {
		return DesktopIsolated{}, false
	}
	return DesktopIsolated{On: desktopIsolatedOn(), Launcher: desktopiso.Here(home), Claude: desktopIsolatedClaudeOn(), ClaudeDir: desktopIsolatedClaudeDir(home)}, true
}

// SetDesktopIsolatedClaude gives the isolated Desktop's Code tab a Claude
// Code folder of its own, or takes it back to ~/.claude. On, the folder
// is made with a settings.json, so magpie writes Desktop's model windows
// there and the Agents page lists it. Claude Magpie.app, when magpie has
// made it, is made again with the new folder (or none); a Desktop running
// from it keeps the one it started with until it is quit and opened
// again. The folder itself, with what Claude Code keeps in it, stays when
// turned off.
func SetDesktopIsolatedClaude(home string, on bool) error {
	if desktopdir.Isolated(desktopdir.OS, home) == "" {
		return ErrDesktopIsolatedOS
	}
	if on {
		if err := desktopIsoClaudeMake(home); err != nil {
			return err
		}
	}
	if err := saveDesktopIsolatedClaude(on, true); err != nil {
		return err
	}
	if desktopiso.Here(home) != "" {
		app := desktopClaudeApp()
		if app == "" {
			return ErrDesktopNoApp
		}
		iso, _ := desktopIsolatedSet(desktopdir.OS, home, os.Getenv)
		if _, err := desktopIsoAdd(home, app, iso.dir3p, desktopIsoClaudeArg(home)); err != nil {
			return err
		}
	}
	desktopClaudeSync(home)
	return nil
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
// Claude Magpie.app is made (made new, with a Claude Code folder of its
// own: desktopIsoClaudeDefault), and a Desktop on magpie moves there: the
// user's own Desktop is put back as it was before magpie (desktopOff),
// and Claude-Magpie-3p gets magpie's 3p mode. Off: the app is taken away,
// and Claude-Magpie-3p goes off magpie; the wiring goes back to the
// user's own Desktop only if it came from there (desktopIsolatedFrom), so
// a twin made beside a Desktop not on magpie never puts that one on it.
// Claude-Magpie-3p itself, with the chats in it, stays. It returns the
// app's path when on.
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
		if err := desktopIsoClaudeDefault(iso); err != nil {
			return "", err
		}
		if err := os.MkdirAll(iso.dir3p, 0o755); err != nil {
			return "", err
		}
		if desktopIsolatedClaudeOn() {
			if err := desktopIsoClaudeMake(home); err != nil {
				return "", err
			}
		}
		p, err := desktopIsoAdd(home, app, iso.dir3p, desktopIsoClaudeArg(home))
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
			stash(map[string]string{desktopIsolatedFrom: "1"})
		}
		if err := saveDesktopIsolated(true); err != nil {
			return "", err
		}
		desktopClaudeSync(home)
		return p, nil
	}
	if desktopWired(iso) {
		if unstashPeek(desktopIsolatedFrom) != "" {
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
		}
		if err := desktopOff(iso); err != nil {
			return "", err
		}
	}
	forget(desktopIsolatedFrom)
	if err := saveDesktopIsolated(false); err != nil {
		return "", err
	}
	if _, err := desktopiso.Remove(home); err != nil {
		return "", err
	}
	desktopClaudeSync(home)
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
