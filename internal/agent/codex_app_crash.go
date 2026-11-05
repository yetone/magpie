package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The Codex app's own logs, read for the page it shows when it can't go on
// after magpie restarted its app-server (provider.CodexAppCrashed, see
// provider/codex_app_crash.go). The app writes them as its file logger
// does in 26.1021 (file-based-logger: macOS ~/Library/Logs/<bundle id>,
// Windows %LOCALAPPDATA%\Codex\Logs, Linux $XDG_STATE_HOME/codex/logs,
// each with a YYYY/MM/DD folder for the UTC day a file was opened), one
// line per entry: `<RFC 3339 UTC> <level> [<scope>] <event> <key=value…>`.
// The Store app on Windows writes the same folder under its package's
// LocalCache (seen on the Windows box).

// codexLogEnv reads the environment for the log folders; a var for tests.
var codexLogEnv = os.Getenv

// codexAppLogRoots are the folders the Codex app writes its logs in here.
func codexAppLogRoots() []string {
	home := codexLogEnv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	switch {
	case restartOnMac:
		return []string{filepath.Join(home, "Library", "Logs", desktopApps["codex"].id)}
	case restartOnWindows:
		local := codexLogEnv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{
			filepath.Join(local, "Codex", "Logs"),
			filepath.Join(local, "Packages", "OpenAI.Codex_2p2nqsd0c76g0", "LocalCache", "Local", "Codex", "Logs"),
		}
	case restartOnLinux:
		state := codexLogEnv("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(home, ".local", "state")
		}
		return []string{filepath.Join(state, "codex", "logs")}
	}
	return nil
}

// codexLogDays is how many of a root's newest day folders are read: a file
// stays in the day it was opened in while the app writes on to it.
const codexLogDays = 7

// codexLogTail is how much of the end of a log file is read.
const codexLogTail = 512 << 10

// codexFatalLine is what the app's AppServerConnection logs as it shows the
// page (broadcastFatalError).
const codexFatalLine = " [AppServerConnection] fatal_error_broadcasted"

// codexAppCrashed is provider.CodexAppCrashed: the first page the app
// logged at or after since (less a second for the clock's grain), read in
// the files written to since then. known is false when no root can be
// read; a root that isn't there is the app never having run on it.
func codexAppCrashed(since time.Time) (at time.Time, crashed, known bool) {
	from := since.Add(-time.Second)
	for _, root := range codexAppLogRoots() {
		files, ok := codexLogFiles(root, from)
		known = known || ok
		for _, f := range files {
			if t, ok := codexFatalIn(f, from); ok && (!crashed || t.Before(at)) {
				at, crashed = t, true
			}
		}
	}
	return at, crashed, known
}

// codexLogFiles are the .log files under root's newest day folders written
// to at or after from; ok is false when root can't be read.
func codexLogFiles(root string, from time.Time) (files []string, ok bool) {
	if _, err := os.ReadDir(root); errors.Is(err, os.ErrNotExist) {
		return nil, true
	} else if err != nil {
		return nil, false
	}
	var days []string
	for _, y := range codexLogDirs(root) {
		for _, m := range codexLogDirs(y) {
			days = append(days, codexLogDirs(m)...)
		}
	}
	slices.Sort(days)
	if len(days) > codexLogDays {
		days = days[len(days)-codexLogDays:]
	}
	for _, d := range days {
		es, _ := os.ReadDir(d)
		for _, e := range es {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			if info, err := e.Info(); err == nil && !info.ModTime().Before(from) {
				files = append(files, filepath.Join(d, e.Name()))
			}
		}
	}
	return files, true
}

// codexLogDirs are dir's folders named by digits alone (a year, month or
// day), in full.
func codexLogDirs(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		if e.IsDir() && e.Name() != "" && strings.Trim(e.Name(), "0123456789") == "" {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// codexFatalIn is the time of the first page logged in the file at path
// at or after from, read in its last codexLogTail bytes. A line the app
// is still writing (no newline yet) and the part line the tail cuts into
// are left out.
func codexFatalIn(path string, from time.Time) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, false
	}
	off := max(info.Size()-codexLogTail, 0)
	b, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return time.Time{}, false
	}
	if off > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return time.Time{}, false
		}
		b = b[i+1:]
	}
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[:i+1]
	} else {
		return time.Time{}, false
	}
	for line := range bytes.Lines(b) {
		if !bytes.Contains(line, []byte(codexFatalLine)) {
			continue
		}
		stamp, _, _ := bytes.Cut(line, []byte(" "))
		t, err := time.Parse(time.RFC3339Nano, string(stamp))
		if err == nil && !t.Before(from) {
			return t, true
		}
	}
	return time.Time{}, false
}

// codexAppRelaunch is provider.CodexAppRelaunch: it quits the Codex app
// and opens it again, as the Agents row's Restart does for an app: on
// macOS ChatGPT.app (com.openai.codex) asked to quit as ⌘Q does and opened
// once every process of its bundle has ended, never forced (an app still
// open then is left, and the row says to quit it); on Windows the Store
// app's windows closed, its tree ended when it stays in the tray, and
// opened again (codexAppReopen). Nothing is done for an app not running:
// it was quit meanwhile. Linux isn't reopened.
func codexAppRelaunch(ctx context.Context) error {
	switch {
	case restartOnWindows:
		return codexAppReopen(ctx)
	case restartOnMac:
		c, ok := findRestartable(restartable{mac: desktopApps["codex"]})
		if !ok {
			return nil
		}
		a, err := Find("codex")
		if err != nil {
			a = &Agent{ID: "codex", Name: "Codex"}
		}
		return reopenBundle(ctx, a, c.bundle, c.exe)
	}
	return errors.New("magpie reopens the Codex app on macOS and Windows only")
}
