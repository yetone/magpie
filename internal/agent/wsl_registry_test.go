package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/wslrun"
)

// wslRealRuns has WSL on and agent's and wslrun's real wsl.exe runners
// (testenv's failing stand-in on PATH) wrapped to note each run's arguments and
// error; what magpie remembers of WSL starts empty.
func wslRealRuns(t *testing.T) (runs func() map[string][]error) {
	t.Helper()
	syncHome(t)
	on, run, ron, rrun := wslOn, wslRun, wslrun.On, wslrun.Run
	var mu sync.Mutex
	got := map[string][]error{}
	wslOn, wslrun.On = true, true
	wslrun.Run = func(d time.Duration, args ...string) ([]byte, error) {
		out, err := rrun(d, args...)
		mu.Lock()
		k := "wslrun " + strings.Join(args, " ")
		got[k] = append(got[k], err)
		mu.Unlock()
		return out, err
	}
	wslRun = func(d time.Duration, args ...string) ([]byte, error) {
		out, err := run(d, args...)
		mu.Lock()
		k := strings.Join(args, " ")
		got[k] = append(got[k], err)
		mu.Unlock()
		return out, err
	}
	reset := func() {
		wsl.Lock()
		wsl.seen, wsl.at, wsl.runAt, wsl.names, wsl.running, wsl.probed, wsl.failed, wsl.dirty, wsl.listed, wsl.runOK = nil, time.Time{}, time.Time{}, nil, nil, nil, nil, false, false, false
		wsl.Unlock()
	}
	reset()
	t.Cleanup(func() { wslOn, wslRun, wslrun.On, wslrun.Run = on, run, ron, rrun; wslrun.Forget(); reset() })
	return func() map[string][]error {
		mu.Lock()
		defer mu.Unlock()
		out := map[string][]error{}
		for k, v := range got {
			out[k] = append([]error(nil), v...)
		}
		clear(got)
		return out
	}
}

// TraderX219 on X: magpie on a Windows with no WSL distro put up Windows'
// "WSL must be updated… press any key to install" console every 20 to 30
// seconds, from wsl.exe -l --running. With none registered no listing,
// running list, link or run in a distro starts wsl.exe, and the distros
// read as none, an answer rather than a failure (no hang is noted). A
// distro registered later is listed at the next listing.
func TestNoWSLDistroRunsNoWSL(t *testing.T) {
	runs := wslRealRuns(t)
	restore := proc.FakeWSLRegistry(nil, true, "")
	defer func() { restore() }()

	if ds := wslDistros(); len(ds) != 0 {
		t.Fatalf("no distro registered, read %+v", ds)
	}
	wsl.Lock()
	listed := wsl.listed
	wsl.runAt = time.Time{}
	wsl.Unlock()
	if !listed {
		t.Fatal("no distro registered read as wsl.exe failing")
	}
	if wslHungLately() {
		t.Fatal("no distro registered noted as a hung wsl.exe")
	}
	if wslUp("Ubuntu") {
		t.Fatal("a distro runs with none registered")
	}
	wslExec("Ubuntu", "true")
	if _, ok := wslReach("Ubuntu", "http://127.0.0.1:3425"); ok {
		t.Fatal("reached into a distro with none registered")
	}
	wslrun.Forget()
	if wslrun.Up("Ubuntu") {
		t.Fatal("wslrun: a distro runs with none registered")
	}
	got := runs()
	if len(got) == 0 {
		t.Fatal("nothing asked: the test tests nothing")
	}
	for args, errs := range got {
		for _, err := range errs {
			if !errors.Is(err, proc.ErrNoWSLDistro) {
				t.Errorf("no distro registered, wsl.exe %s was started: %v", args, err)
			}
		}
	}

	// installed later: the next listing asks wsl.exe
	restore()
	restore = proc.FakeWSLRegistry([]string{"Ubuntu"}, true, "")
	wsl.Lock()
	wsl.at = time.Time{}
	wsl.Unlock()
	wslDistros()
	if errs := runs()["-l -q"]; len(errs) == 0 || errors.Is(errs[0], proc.ErrNoWSLDistro) {
		t.Fatalf("a distro registered later wasn't listed: %v", errs)
	}
}

// A wsl.exe that says WSL must be updated isn't asked again, and what
// magpie remembers of a distro is kept while it isn't: its refusal is
// unknown, not "no distro".
func TestWSLUpdateKeepsDistros(t *testing.T) {
	runs := wslRealRuns(t)
	defer proc.FakeWSLRegistry([]string{"Ubuntu"}, true, "2.6.1.0")()
	wsl.Lock()
	wsl.seen = map[string]*distro{"Ubuntu": {Name: "Ubuntu", Home: "/home/me", Has: map[string]bool{"dir:.codex": true}, Probe: wslProbeVersion, Values: map[string]string{"k": "v"}}}
	wsl.probed, wsl.failed = map[string]bool{}, map[string]time.Time{}
	wsl.Unlock()

	say := "Windows Subsystem for Linux must be updated to the latest version to proceed. You can update by running 'wsl.exe --update'."
	if runtime.GOOS == "windows" {
		// a script can't stand in for wsl.exe here (testenv's own stands
		// first, and isn't a program): proc is told as the runner tells it
		proc.WSLSaid([]byte(say), errors.New("exit status 1"))
	} else {
		// a wsl.exe that says so, as the inbox one does, before testenv's
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "wsl.exe"), []byte("#!/bin/sh\necho \""+say+"\"\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	wslDistros()
	first := runs()
	if runtime.GOOS != "windows" && len(first["-l -q"]) != 1 {
		t.Fatalf("the first listing wasn't wsl.exe's: %v", first)
	}
	wsl.Lock()
	kept := wsl.seen["Ubuntu"] != nil
	wsl.at, wsl.runAt = time.Time{}, time.Time{}
	wsl.Unlock()
	if !kept {
		t.Fatal("a distro was forgotten while WSL asked to be updated")
	}
	if WSLKept("Ubuntu", "k") != "v" {
		t.Fatal("a distro's kept values were lost")
	}
	wslDistros()
	got := runs()
	if len(got) == 0 {
		t.Fatal("nothing asked: the test tests nothing")
	}
	for args, errs := range got {
		for _, err := range errs {
			if !errors.Is(err, proc.ErrWSLNeedsUpdate) {
				t.Errorf("WSL asked to be updated, wsl.exe %s was started: %v", args, err)
			}
		}
	}
}
