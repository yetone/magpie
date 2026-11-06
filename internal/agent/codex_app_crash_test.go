package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"testing"
	"time"
)

// codexLog writes a log file of the Codex app's under root's day folder
// for day, as the app's file logger names it, modified at mod.
func codexLog(t *testing.T, root string, day time.Time, text string, mod time.Time) string {
	t.Helper()
	dir := filepath.Join(root, day.UTC().Format("2006"), day.UTC().Format("01"), day.UTC().Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "codex-desktop-581d5b6a-a43b-4ec2-8343-17b4d18217bb-390-t0-i1-000015-0.log")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

// the lines as ChatGPT.app 26.1021 writes them (the owner's logs and the
// app's broadcastFatalError)
func exitedLine(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z") + " error [AppServerConnection] Codex CLI process exited classifiedAsExpected=true code=0 connectionId=93 signal=null transport=stdio\n"
}

func fatalLine(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z") + " error [AppServerConnection] fatal_error_broadcasted initialized=true listenerCount=2 pendingRequestCount=0 transportKind=stdio\n"
}

// The page the Codex app shows when it can't go on after magpie restarted
// its app-server (ysm119 on Discord) is read from the app's own logs: a
// complete fatal_error_broadcasted line at or after the end, in a file
// written to since, in the newest day folders (a file opened the day
// before is written on to past midnight UTC). The clean exit it logs
// first, a page from before, a line still being written and another
// app's folder are none of it.
func TestCodexAppCrashedReadsItsOwnLogs(t *testing.T) {
	home := restartSandbox(t)
	restartOnMac = true
	root := filepath.Join(home, "Library", "Logs", "com.openai.codex")
	since := time.Date(2026, 11, 5, 23, 59, 58, 0, time.UTC)

	if _, crashed, known := codexAppCrashed(since); crashed || !known {
		t.Fatalf("no logs at all: crashed %v, known %v (the app never ran)", crashed, known)
	}
	// the end's clean exit and a page from an hour before
	p := codexLog(t, root, since.Add(-2*time.Hour), fatalLine(since.Add(-time.Hour))+exitedLine(since.Add(time.Second)), since.Add(time.Second))
	if _, crashed, _ := codexAppCrashed(since); crashed {
		t.Fatal("the clean exit or an old page read as the page")
	}
	// the page, half-written
	half := fatalLine(since.Add(3 * time.Second))
	os.WriteFile(p, []byte(fatalLine(since.Add(-time.Hour))+exitedLine(since.Add(time.Second))+half[:len(half)-1]), 0o644)
	os.Chtimes(p, since.Add(3*time.Second), since.Add(3*time.Second))
	if _, crashed, _ := codexAppCrashed(since); crashed {
		t.Fatal("a line still being written read as the page")
	}
	// whole, in the file opened the day before
	os.WriteFile(p, []byte(fatalLine(since.Add(-time.Hour))+exitedLine(since.Add(time.Second))+half), 0o644)
	os.Chtimes(p, since.Add(3*time.Second), since.Add(3*time.Second))
	at, crashed, known := codexAppCrashed(since)
	if !crashed || !known || !at.Equal(since.Add(3*time.Second)) {
		t.Fatalf("the page: at %v, crashed %v, known %v", at, crashed, known)
	}
	// a file not written to since the end isn't read
	os.Chtimes(p, since.Add(-time.Minute), since.Add(-time.Minute))
	if _, crashed, _ := codexAppCrashed(since); crashed {
		t.Fatal("a file untouched since the end read")
	}
	// the next day's folder
	codexLog(t, root, since.Add(time.Hour), exitedLine(since.Add(time.Second))+fatalLine(since.Add(4*time.Second)), since.Add(5*time.Second))
	if at, crashed, _ := codexAppCrashed(since); !crashed || !at.Equal(since.Add(4*time.Second)) {
		t.Fatalf("the next day's folder: at %v, crashed %v", at, crashed)
	}

	// another app's logs (the beta's bundle id) are not the Codex app's
	home = restartSandbox(t)
	restartOnMac = true
	codexLog(t, filepath.Join(home, "Library", "Logs", "com.openai.codex.beta"), since, fatalLine(since.Add(time.Second)), since.Add(time.Second))
	if _, crashed, _ := codexAppCrashed(since); crashed {
		t.Fatal("another app's page read as the Codex app's")
	}

	// a log folder that can't be read is unknown
	if runtime.GOOS != "windows" {
		root := filepath.Join(home, "Library", "Logs", "com.openai.codex")
		os.MkdirAll(root, 0o755)
		os.Chmod(root, 0)
		t.Cleanup(func() { os.Chmod(root, 0o755) })
		if _, err := os.ReadDir(root); err == nil {
			t.Skip("running as a user who reads any folder")
		}
		if _, crashed, known := codexAppCrashed(since); crashed || known {
			t.Fatalf("an unreadable folder: crashed %v, known %v", crashed, known)
		}
	}
}

// The app keeps a day's log file open and writes on to it, and Windows
// stamps a file in its folder listing only as it is closed: the Codex
// app's file on the Windows box listed 10:27Z while the app wrote to it at
// 15:22Z. Read by the listing's time, the file the page was written to
// counted as untouched since the end, the page was never seen, and the
// app was left on it till the user quit and opened it (bitssly on X,
// Store Codex 26.1029). The file is held open here as the app holds it,
// its stamp set back and written to after, which on Windows leaves the
// listing on the old stamp (red there on a listing's time).
func TestCodexAppCrashedReadsAFileTheAppHoldsOpen(t *testing.T) {
	home := restartSandbox(t)
	restartOnMac = true
	root := filepath.Join(home, "Library", "Logs", "com.openai.codex")
	since := time.Now()
	dir := filepath.Join(root, since.UTC().Format("2006"), since.UTC().Format("01"), since.UTC().Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "codex-desktop-a57d47b4-b287-48b4-9ecb-f9671478ca87-33908-t0-i1-000219-0.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(exitedLine(since)); err != nil {
		t.Fatal(err)
	}
	opened := since.Add(-5 * time.Hour)
	if err := os.Chtimes(p, opened, opened); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(fatalLine(since.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	at, crashed, known := codexAppCrashed(since)
	if !crashed || !known || !at.Equal(since.Add(time.Second).UTC().Truncate(time.Millisecond)) {
		t.Fatalf("the page in the file the app holds open: at %v, crashed %v, known %v", at, crashed, known)
	}
}

// Windows and Linux keep the logs where the app's file logger puts them
// there: %LOCALAPPDATA%\Codex\Logs, which the Store app writes under its
// package's LocalCache, and $XDG_STATE_HOME/codex/logs (~/.local/state
// without it).
func TestCodexAppCrashedReadsEachSystemsFolder(t *testing.T) {
	since := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	page := exitedLine(since.Add(time.Second)) + fatalLine(since.Add(2*time.Second))
	for _, tc := range []struct {
		name string
		set  func(home string)
		root func(home string) string
	}{
		{"windows", func(home string) {
			restartOnWindows = true
			t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		}, func(home string) string { return filepath.Join(home, "AppData", "Local", "Codex", "Logs") }},
		{"windows store", func(home string) {
			restartOnWindows = true
			t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		}, func(home string) string {
			return filepath.Join(home, "AppData", "Local", "Packages", "OpenAI.Codex_2p2nqsd0c76g0", "LocalCache", "Local", "Codex", "Logs")
		}},
		{"linux", func(home string) {
			restartOnLinux = true
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
		}, func(home string) string { return filepath.Join(home, "state", "codex", "logs") }},
		{"linux default", func(home string) {
			restartOnLinux = true
			t.Setenv("XDG_STATE_HOME", "")
		}, func(home string) string { return filepath.Join(home, ".local", "state", "codex", "logs") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := restartSandbox(t)
			tc.set(home)
			if _, crashed, _ := codexAppCrashed(since); crashed {
				t.Fatal("a page with no logs")
			}
			codexLog(t, tc.root(home), since, page, since.Add(3*time.Second))
			if at, crashed, known := codexAppCrashed(since); !crashed || !known || !at.Equal(since.Add(2*time.Second)) {
				t.Fatalf("at %v, crashed %v, known %v", at, crashed, known)
			}
		})
	}
}

// The app is reopened as the Agents row's Restart reopens an app on a Mac:
// ChatGPT.app, known by its bundle id, quit as ⌘Q does and opened once it
// has ended, never another bundle (a Codex.app of another id), and never
// forced (one still open is said). An app not open: nothing. Linux: not done.
func TestCodexAppRelaunchOnlyTheCodexApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the bundle paths are built with filepath, so a Windows run spells them with \\ (#4115)")
	}
	home := restartSandbox(t)
	apps := filepath.Join(home, "Applications")
	chatgpt := macApp(t, apps, "ChatGPT.app", "com.openai.codex", "ChatGPT")
	other := macApp(t, apps, "Codex.app", "com.openai.codex.beta", "Codex")
	restartOnMac = true
	appFolders = func() []string { return []string{apps} }
	appQuitWait = 100 * time.Millisecond
	alive := map[string]bool{chatgpt: true, other: true}
	stays := false
	running = func(pat string) []runningProc {
		var out []runningProc
		exe := map[string]string{chatgpt: "ChatGPT", other: "Codex"}
		for b, on := range alive {
			for _, cmd := range []string{
				b + "/Contents/MacOS/" + exe[b],
				b + "/Contents/Resources/codex app-server --analytics-default-enabled",
			} {
				if on && regexp.MustCompile(pat).MatchString(cmd) {
					out = append(out, runningProc{time.Hour, cmd})
				}
			}
		}
		return out
	}
	var quit, opened []string
	appQuit = func(_ context.Context, b string) error {
		quit = append(quit, b)
		if !stays {
			alive[b] = false
		}
		return nil
	}
	appOpen = func(_ context.Context, b string) error {
		opened = append(opened, b)
		alive[b] = true
		return nil
	}
	if err := codexAppRelaunch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(quit, []string{chatgpt}) || !slices.Equal(opened, []string{chatgpt}) || !alive[other] {
		t.Fatalf("quit %q, opened %q, the other app running %v", quit, opened, alive[other])
	}

	// one that doesn't quit: said, not opened again
	quit, opened, stays = nil, nil, true
	if err := codexAppRelaunch(context.Background()); err == nil || len(opened) > 0 {
		t.Fatalf("an app that stayed: %v, opened %q", err, opened)
	}

	// not running: nothing
	quit, alive[chatgpt] = nil, false
	if err := codexAppRelaunch(context.Background()); err != nil || len(quit) > 0 {
		t.Fatalf("not running: %v, quit %q", err, quit)
	}

	restartOnMac, restartOnLinux = false, true
	alive[chatgpt] = true
	if err := codexAppRelaunch(context.Background()); err == nil || len(quit) > 0 {
		t.Fatalf("linux: %v, quit %q", err, quit)
	}
}
