package update

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// relaunchFiles is an update just put in: exe the new version, old the
// running one moved aside.
func relaunchFiles(t *testing.T) (exe, old string) {
	t.Helper()
	dir := t.TempDir()
	exe, old = filepath.Join(dir, "magpie.exe"), filepath.Join(dir, "magpie.exe.old")
	os.WriteFile(exe, []byte("new version"), 0o755)
	os.WriteFile(old, []byte("running version"), 0o755)
	return exe, old
}

func stubLaunch(t *testing.T, f func(exe string, args, env []string) (<-chan struct{}, func(), error)) {
	t.Helper()
	was := launch
	launch = f
	t.Cleanup(func() { launch = was })
}

func wantPutBack(t *testing.T, exe, old string) {
	t.Helper()
	if b, err := os.ReadFile(exe); err != nil || string(b) != "running version" {
		t.Errorf("%s = %q (%v), want the running version back", filepath.Base(exe), b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("%s still there: %v", filepath.Base(old), err)
	}
}

// #894: Smart App Control refuses the new, unsigned exe when it is
// started (ERROR_SYSTEM_INTEGRITY_POLICY_VIOLATION): the running version
// goes back in its place, and the error says it was Windows' policy.
func TestRelaunchRefusedPutsBack(t *testing.T) {
	exe, old := relaunchFiles(t)
	stubLaunch(t, func(string, []string, []string) (<-chan struct{}, func(), error) {
		return nil, nil, &os.PathError{Op: "fork/exec", Path: exe, Err: errIntegrityPolicy}
	})
	err := RelaunchBinary(exe, old, true, "settings")
	var be *BlockedError
	if !errors.As(err, &be) || !be.Policy() {
		t.Fatalf("err = %v, want a policy *BlockedError", err)
	}
	if !strings.Contains(err.Error(), "Smart App Control") {
		t.Errorf("err doesn't say why: %v", err)
	}
	wantPutBack(t, exe, old)
}

// A new version that starts and says nothing in time is ended and the
// running one put back; one that quits at once likewise.
func TestRelaunchSilentPutsBack(t *testing.T) {
	was := startWait
	startWait = 200 * time.Millisecond
	t.Cleanup(func() { startWait = was })
	for _, quits := range []bool{false, true} {
		exe, old := relaunchFiles(t)
		var killed atomic.Bool
		stubLaunch(t, func(string, []string, []string) (<-chan struct{}, func(), error) {
			exited := make(chan struct{})
			if quits {
				close(exited)
				return exited, func() {}, nil
			}
			return exited, func() { killed.Store(true); close(exited) }, nil
		})
		err := RelaunchBinary(exe, old, false, "")
		var be *BlockedError
		if !errors.As(err, &be) || be.Policy() {
			t.Fatalf("quits=%v: err = %v, want a *BlockedError not of policy", quits, err)
		}
		if !quits && !killed.Load() {
			t.Error("the silent new version was left running")
		}
		wantPutBack(t, exe, old)
	}
}

// A new version that says it is up stays; what was moved aside is left for
// it to remove.
func TestRelaunchReportedStays(t *testing.T) {
	exe, old := relaunchFiles(t)
	var args []string
	stubLaunch(t, func(_ string, a, env []string) (<-chan struct{}, func(), error) {
		args = a
		for _, kv := range env {
			if p, ok := strings.CutPrefix(kv, startedEnv+"="); ok {
				go func() { time.Sleep(50 * time.Millisecond); os.WriteFile(p, []byte("1"), 0o644) }()
			}
		}
		return make(chan struct{}), func() { t.Error("killed") }, nil
	})
	if err := RelaunchBinary(exe, old, false, ""); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "tray" {
		t.Errorf("args = %q", args)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new version" {
		t.Errorf("exe = %q", b)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("old: %v", err)
	}
}

// 柒拾 on Discord: with TEMP naming a folder that isn't there, the new
// version couldn't write its word in the temp folder, was ended after
// startWait and the update noted as refused, on every try. Its word goes
// beside the exe then, where the update has just written. The stand-in
// writes it as reportStarted does.
func TestRelaunchWithoutATempFolder(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "unmounted", "Temp")
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, gone)
	}
	was := startWait
	startWait = 2 * time.Second
	t.Cleanup(func() { startWait = was })
	for _, forward := range []bool{false, true} {
		exe, old := relaunchFiles(t)
		var at string
		stubLaunch(t, func(_ string, _, env []string) (<-chan struct{}, func(), error) {
			for _, kv := range env {
				if p, ok := strings.CutPrefix(kv, startedEnv+"="); ok {
					at = p
					t.Setenv(startedEnv, p)
					reportStarted()
				}
			}
			return make(chan struct{}), func() { t.Error("killed") }, nil
		})
		var err error
		if forward {
			err = Forward(old, exe)
		} else {
			err = RelaunchBinary(exe, old, false, "")
		}
		if err != nil {
			t.Fatalf("forward=%v: %v", forward, err)
		}
		if filepath.Dir(at) != filepath.Dir(exe) {
			t.Errorf("forward=%v: word written at %s, want beside %s", forward, at, exe)
		}
		if _, err := os.Stat(at); !os.IsNotExist(err) {
			t.Errorf("forward=%v: the word was left behind: %v", forward, err)
		}
	}
}

// What the started magpie writes is what the one starting it waits for.
func TestReportStarted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "started")
	t.Setenv(startedEnv, p)
	reportStarted()
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(startedEnv) != "" {
		t.Error("left in the environment, for what it starts")
	}
}

// The note of a refused version holds while it is newer than the one
// running, and goes once that has caught up.
func TestBlockedNote(t *testing.T) {
	t.Cleanup(ClearBlocked)
	NoteBlocked("0.1.20", &BlockedError{Err: errIntegrityPolicy})
	b := ReadBlocked("0.1.19")
	if b == nil || b.Version != "0.1.20" || !b.Policy || b.Error == "" {
		t.Fatalf("ReadBlocked = %+v", b)
	}
	if b := ReadBlocked("0.1.20"); b != nil {
		t.Fatalf("caught up: %+v", b)
	}
	if b := ReadBlocked("0.1.19"); b != nil {
		t.Fatalf("not forgotten: %+v", b)
	}
	if policyRefused(syscall.Errno(2)) || !policyRefused(&os.PathError{Err: syscall.Errno(1260)}) {
		t.Error("policyRefused")
	}
}

// The started magpie's word goes in a file no one else could have made
// first: the temp folder can be shared (/tmp on Linux), and a link left at
// the old fixed name "magpie-started-<pid>" was followed, emptying and
// writing over the file of the user's it named. A file someone else left
// there already holding a word doesn't say a new version is up that never
// ran either.
func TestRelaunchMarkerNotAGuessableName(t *testing.T) {
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	was := startWait
	startWait = time.Second
	t.Cleanup(func() { startWait = was })
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("the user's own"), 0o600)
	for _, forward := range []bool{false, true} {
		name := "magpie-started-" + strconv.Itoa(os.Getpid())
		if forward {
			name = "magpie-installed-" + strconv.Itoa(os.Getpid())
		}
		planted := filepath.Join(tmp, name)
		os.Remove(planted)
		if err := os.Symlink(victim, planted); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		exe, old := relaunchFiles(t)
		var at string
		stubLaunch(t, func(_ string, _, env []string) (<-chan struct{}, func(), error) {
			for _, kv := range env {
				if p, ok := strings.CutPrefix(kv, startedEnv+"="); ok {
					at = p
					if runtime.GOOS != "windows" {
						if fi, err := os.Lstat(p); err != nil {
							t.Errorf("forward=%v: the word's file %s: %v", forward, p, err)
						} else if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
							t.Errorf("forward=%v: the word's file %s is %v, want a regular 0600 file", forward, p, fi.Mode())
						}
					}
					t.Setenv(startedEnv, p)
					reportStarted()
				}
			}
			return make(chan struct{}), func() { t.Error("killed") }, nil
		})
		var err error
		if forward {
			err = Forward(old, exe)
		} else {
			err = RelaunchBinary(exe, old, false, "")
		}
		if err != nil {
			t.Fatalf("forward=%v: %v", forward, err)
		}
		if at == planted {
			t.Errorf("forward=%v: the word went to the guessable %s", forward, at)
		}
		if b, _ := os.ReadFile(victim); string(b) != "the user's own" {
			t.Fatalf("forward=%v: the linked file was written over: %q", forward, b)
		}
		os.Remove(planted)
	}

	// a file at the old name already holding a pid is not the started
	// magpie's word: one that never writes is still ended and refused
	planted := filepath.Join(tmp, "magpie-started-"+strconv.Itoa(os.Getpid()))
	os.WriteFile(planted, []byte("1"), 0o644)
	exe, old := relaunchFiles(t)
	killed := make(chan struct{})
	stubLaunch(t, func(string, []string, []string) (<-chan struct{}, func(), error) {
		return killed, func() { close(killed) }, nil
	})
	if err := RelaunchBinary(exe, old, false, ""); err == nil {
		t.Fatal("a version that never said it was up was taken as started")
	}
	select {
	case <-killed:
	default:
		t.Error("the silent version wasn't ended")
	}
	wantPutBack(t, exe, old)
}
