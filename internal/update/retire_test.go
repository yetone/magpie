//go:build !windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// #4983, #2362: on macOS 13 every update quit magpie for good. Install
// deleted the app magpie was running from, and a process whose program
// file is gone is refused every program it starts: the open of the new
// version (the handover's, which waited 60s for nothing) and the shell
// that relaunches it, which never wrote a line to relaunch.log. The copy
// magpie runs from stays, on the app's volume, where neither the next
// download (Stage clears stageDir) nor anything else removes it, and it
// isn't named like an app (#3965).
func TestInstallKeepsTheAppItRunsFrom(t *testing.T) {
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	app(t, bundle, "old")
	// the file a running magpie is: the same file, moved, is still it
	exe := filepath.Join(bundle, "Contents", "MacOS", "magpie")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old magpie"), 0o755)
	running, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stageDir(apps), stagedName)
	app(t, staged, "new")

	if err := Install(staged, bundle); err != nil {
		t.Fatal(err)
	}
	if version(t, bundle) != "new" {
		t.Fatal("the new app isn't in place")
	}
	// the next update's download starts its folder over
	os.RemoveAll(stageDir(apps))
	kept, _ := filepath.Glob(filepath.Join(retiredDir(bundle), "*", "Contents", "MacOS", "magpie"))
	if len(kept) != 1 {
		t.Fatalf("install deleted the app magpie was running from: kept %q", kept)
	}
	if fi, err := os.Stat(kept[0]); err != nil || !os.SameFile(fi, running) {
		t.Errorf("the program kept isn't the one running (%v)", err)
	}
	if !strings.HasPrefix(kept[0], apps+string(filepath.Separator)) {
		t.Errorf("kept off the app's folder, where a move is a copy and a delete: %s", kept[0])
	}
	noApps(t, retiredDir(bundle))

	// a second update keeps each copy apart
	staged = filepath.Join(stageDir(apps), stagedName)
	app(t, staged, "newer")
	if err := Install(staged, bundle); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(retiredDir(bundle)); len(es) != 2 {
		t.Errorf("after two installs %d copies are kept, want 2", len(es))
	}
}

// A failed swap leaves the app where it was, and no folder of old copies.
func TestInstallFailingKeepsTheAppInPlace(t *testing.T) {
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	app(t, bundle, "old")
	if err := Install(filepath.Join(stageDir(apps), stagedName), bundle); err == nil {
		t.Fatal("installed with nothing staged")
	}
	if version(t, bundle) != "old" {
		t.Error("the app wasn't put back")
	}
	if _, err := os.Stat(retiredDir(bundle)); !os.IsNotExist(err) {
		t.Errorf("a folder of old copies is left: %v", err)
	}
}

// helperEnv has this test binary, copied into an app, run as the magpie
// still running the old version (a `magpie web` beside the app).
const helperEnv = "MAGPIE_TEST_RETIRED_RUNNING"

func TestRetiredHelper(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("the process TestRetiredKeptWhileRunFrom runs from the old app")
	}
	time.Sleep(time.Minute)
}

// The copy an update replaced is removed once nothing runs from it, and
// not before: a `magpie web` started from the app runs on as the old
// version after the app updated (#4983's reporter runs one under launchd),
// and it starts agents and plugin hosts as long as it lives.
func TestRetiredKeptWhileRunFrom(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("lsof, and an app bundle")
	}
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	app(t, bundle, "old")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bundle, "Contents", "MacOS", "magpie")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	if b, err := exec.Command("cp", self, exe).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, b)
	}
	web := exec.Command(exe, "-test.run=^TestRetiredHelper$")
	web.Env = append(os.Environ(), helperEnv+"=1")
	if err := web.Start(); err != nil {
		t.Fatal(err)
	}
	defer web.Process.Kill()
	staged := filepath.Join(stageDir(apps), stagedName)
	app(t, staged, "new")
	if err := Install(staged, bundle); err != nil {
		t.Fatal(err)
	}

	RemoveRetired(bundle)
	if kept, _ := os.ReadDir(retiredDir(bundle)); len(kept) != 1 {
		t.Fatalf("the app a running magpie runs from was removed (%d kept)", len(kept))
	}
	web.Process.Kill()
	web.Wait()
	RemoveRetired(bundle)
	if _, err := os.Stat(retiredDir(bundle)); !os.IsNotExist(err) {
		t.Errorf("the old copy outlived the last process running it: %v", err)
	}
	if version(t, bundle) != "new" {
		t.Error("cleaning up touched the installed app")
	}
}

// A copy whose use can't be told (lsof failed or timed out) is kept.
func TestRetiredKeptWhenUnknown(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("lsof")
	}
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	old := filepath.Join(retiredDir(bundle), "1-x")
	os.MkdirAll(filepath.Join(old, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(old, "Contents", "MacOS", "magpie"), []byte("x"), 0o755)
	was := runFrom
	runFrom = func(string) bool { return true }
	defer func() { runFrom = was }()
	RemoveRetired(bundle)
	if _, err := os.Stat(old); err != nil {
		t.Errorf("a copy whose use couldn't be told was removed: %v", err)
	}
}

// diesFirst is a shell that is ended before it runs a line of its script,
// as macOS 13 ends every process a magpie whose app was deleted starts.
func diesFirst(t *testing.T) {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	was := shellPath
	shellPath = sh
	t.Cleanup(func() { shellPath = was })
}

// #4983: the handover's open and the relaunch's shell were started, and
// ended before they ran; Start said nil, so magpie waited for a new
// version no one opened and then quit with nothing to bring it back. A
// shell that never ran is an error, which the restart turns into staying
// up and saying so.
func TestRelaunchShellThatNeverRanIsAnError(t *testing.T) {
	diesFirst(t)
	bundle := filepath.Join(t.TempDir(), "magpie.app")
	start := time.Now()
	if err := Relaunch(bundle, false, false); err == nil {
		t.Error("Relaunch said its shell started, though it never ran")
	}
	if err := OpenBeside(bundle, false, false); err == nil {
		t.Error("OpenBeside said its shell started, though it never ran")
	}
	// the end of the shell is seen as it happens, not after shellReady
	if d := time.Since(start); d > shellReady {
		t.Errorf("took %s to see the shells gone", d)
	}
}

// A shell that runs says so at once, and goes on with its script.
func TestStartShellRuns(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	if err := startShell("echo ran > '" + marker + "'"); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(marker); string(b) == "ran\n" {
			return
		}
	}
	t.Fatal("the script never ran")
}
