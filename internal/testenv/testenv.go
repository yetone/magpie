// Package testenv gives a package's tests a home of their own, on every OS:
// HOME, USERPROFILE (Windows' home), the XDG folders and Windows' APPDATA
// and LOCALAPPDATA all point into one new temporary folder, the agents' own
// variables (agentenv.Vars) are cleared, and the keychain tools and agent
// CLIs on PATH are failing stand-ins, so that no test reads or writes the
// files of the user running it. Setting HOME alone isn't that: on Windows
// Go reads the home from USERPROFILE, and a test then meets the real
// ~/.claude. A package uses it from its TestMain:
//
//	func TestMain(m *testing.M) { testenv.Main(m) }
//
// and a test that wants a home of its own sets it with SetHome, never
// HOME alone. Only tests import it.
package testenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

const (
	// prefix starts the temporary home's name; Run removes only a folder
	// so named.
	prefix = "magpie-test-home-"
	// Marker holds the temporary home in the environment. A test binary
	// started again by one of its own tests inherits it, and keeps the
	// environment that test gave it (its fixtures) instead of isolating
	// again; a test that hands such a child an environment of its own
	// passes Marker along.
	Marker = "MAGPIE_TEST_SANDBOX"
)

// folderVars are the variables Isolate points into the temporary home.
var folderVars = []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"}

// Main runs the package's tests in a home of their own and exits with
// their code.
func Main(m *testing.M) {
	os.Exit(Run(m))
}

// Run is Main for a TestMain with more to set up: it isolates, runs the
// tests and removes the temporary home, and gives the code to exit with.
// It runs no test when the home can't be made safe.
func Run(m *testing.M) int {
	return RunIn(m, nil)
}

// RunIn is Run with setup called on the temporary home before the tests,
// for fixtures that live in it.
func RunIn(m *testing.M, setup func(home string)) int {
	if home := os.Getenv(Marker); home != "" {
		// started again by one of the tests: keep what it set up
		if err := checkChild(home); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
		if setup != nil {
			setup(home)
		}
		defer RemovePrograms()
		return m.Run()
	}
	home, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		return 1
	}
	defer remove(home)
	defer RemovePrograms()
	if setup != nil {
		setup(home)
	}
	return m.Run()
}

// Isolate points the home and the config, cache and data folders into a
// new temporary folder, clears the agents' variables, puts the stand-ins
// first on PATH and gives the folder. It fails when Go would still
// resolve any of them, or a stand-in's tool, outside that folder.
func Isolate() (string, error) {
	home, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
		Marker:            home,
	} {
		if err := os.Setenv(k, v); err != nil {
			return "", err
		}
	}
	for _, k := range agentenv.Vars {
		if err := os.Unsetenv(k); err != nil {
			return "", err
		}
	}
	if err := inertTools(home); err != nil {
		return "", err
	}
	for _, name := range tools {
		if p, err := exec.LookPath(name); err != nil || !within(p, home) {
			return "", fmt.Errorf("%s resolves to %q, not the stand-in in %s (%v)", name, p, home, err)
		}
	}
	for name, dir := range map[string]func() (string, error){
		"home": os.UserHomeDir, "config": os.UserConfigDir, "cache": os.UserCacheDir,
	} {
		p, err := dir()
		if err != nil || !within(p, home) {
			return "", fmt.Errorf("the %s folder %q is outside the test home %s (%v)", name, p, home, err)
		}
	}
	return home, nil
}

// checkChild accepts the environment a test gave the binary it started
// again: the sandbox, and every folder variable and agent folder set, lie
// in the temporary folder. On Windows that folder must come from TMP or
// TEMP, since without them Go would take it from USERPROFILE, the very
// thing to check.
func checkChild(home string) error {
	if runtime.GOOS == "windows" && os.Getenv("TMP") == "" && os.Getenv("TEMP") == "" {
		return errors.New("a test started this binary again without TMP or TEMP: pass them along")
	}
	tmp := os.TempDir()
	if !within(home, tmp) {
		return fmt.Errorf("%s is %q, outside the temporary folder %s", Marker, home, tmp)
	}
	vars := append([]string{}, folderVars...)
	for _, k := range agentenv.Vars {
		if !agentenv.NotPaths[k] {
			vars = append(vars, k)
		}
	}
	for _, k := range vars {
		if v := os.Getenv(k); v != "" && !within(v, tmp) {
			return fmt.Errorf("%s is %q, outside the temporary folder %s", k, v, tmp)
		}
	}
	return nil
}

// SetHome makes dir the home for one test, on every OS: HOME, and
// USERPROFILE, which Go reads on Windows.
func SetHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// tools are the programs a test must never reach for real: the system
// keychains (macOS' security, Linux's secret-tool), which hold the user's
// sign-ins outside any home, and the agents' CLIs.
var tools = []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli"}

// inertTools puts a failing stand-in for each of tools first on PATH, in
// home/bin. Tests can still put their own fakes before it.
func inertTools(home string) error {
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		return err
	}
	if err := StandIns(bin, tools); err != nil {
		return err
	}
	for _, name := range tools {
		// Windows finds a program by PATHEXT; a .bat comes before the
		// user's claude.cmd further down PATH
		if runtime.GOOS == "windows" {
			if err := os.WriteFile(filepath.Join(bin, name+".bat"), []byte("@exit /b 1\r\n"), 0o755); err != nil {
				return err
			}
		}
	}
	return os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// StandIns writes a failing program for each name in bin. On macOS each
// is run once now, in the background, so that the check macOS makes of a
// new program's first run (Program) is made while the tests start rather
// than in the first test to reach it, and without holding them up.
func StandIns(bin string, names []string) error {
	for _, name := range names {
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			return err
		}
		if runtime.GOOS == "darwin" {
			warming.Add(1)
			go func() {
				defer warming.Done()
				if proc, err := os.StartProcess(p, []string{p}, &os.ProcAttr{}); err == nil {
					proc.Wait()
					record(p)
				}
			}()
		}
	}
	return nil
}

// Zone sets time.Local for the test, and puts it back after. It first
// waits out the stand-ins' first runs: they os.Stat what ran, which reads
// time.Local, so a test that swaps it while they are under way races them.
func Zone(t testing.TB, loc *time.Location) {
	warming.Wait()
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

// warming is the stand-ins' first runs under way.
var warming sync.WaitGroup

// within says whether path lies in dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// remove deletes the temporary home Isolate made, and nothing else: a
// folder in the temporary folder, named as Isolate names them.
func remove(home string) {
	if filepath.IsAbs(home) && filepath.Dir(home) == filepath.Clean(os.TempDir()) && strings.HasPrefix(filepath.Base(home), prefix) {
		os.RemoveAll(home)
	}
}
