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
	"slices"
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

// proxyVars are the machine's proxy variables. net/http reads them once per
// process, at its first routed request, so a test binary that lets them
// stand freezes the machine's proxy into that read: a test that sets its
// own afterwards reads the machine's instead (search's Auto, which follows
// ALL_PROXY alone for that reason), and Offline's refused hosts are carried
// for real by the machine's proxy standing on loopback.
var proxyVars = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// ClearProxy unsets the machine's proxy variables. Isolate calls it before
// the tests run; a package that isolates itself in its own TestMain calls
// it too, before the first request any test makes.
func ClearProxy() {
	for _, k := range proxyVars {
		os.Unsetenv(k)
	}
}

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
// resolve any of them, or a stand-in's tool, outside that folder. The
// system's config folders ($XDG_CONFIG_DIRS, /etc/xdg when unset), where
// a package keeps its autostart entry (#3372), are one in it too.
func Isolate() (string, error) {
	home, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CONFIG_DIRS": filepath.Join(home, "etc", "xdg"),
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
	for _, k := range append(slices.Clone(agentenv.Vars), agentenv.ClaudeProviders...) {
		if err := os.Unsetenv(k); err != nil {
			return "", err
		}
	}
	ClearProxy()
	if err := dropWindowsPath(); err != nil {
		return "", err
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
// sign-ins outside any home, the agents' CLIs, and wsl.exe, which opens
// the WSL distros' homes: a home of its own doesn't cover those, and a
// test on Windows listed the real distro's Codex sessions beside its
// fixtures (#4177). A test of the real WSL puts System32 before the
// stand-in itself.
var tools = []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli", "wsl.exe"}

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
	return os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// dropWindowsPath takes Windows' folders off PATH in WSL. WSL's interop
// appends them (/mnt/c/Windows/System32, …\VS Code\bin and the rest)
// over 9P, where a stat takes milliseconds, and a test binary takes the
// PATH folders' stamp again at every look (proc's lookFreshInTests): a
// `magpie <agent> <field>` run looked for each agent's command through
// them, and internal/cli's TestEveryAgentFieldNameWritesNothing ran past
// 40 minutes in the magpie-desktop distro (0.3s without them). They
// also hold the machine's own Windows programs, such as VS Code's `code`
// script, which no Linux test is to find.
func dropWindowsPath() error {
	if runtime.GOOS != "linux" || !inWSL() {
		return nil
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	kept := slices.DeleteFunc(slices.Clone(dirs), windowsDrive)
	if len(kept) == len(dirs) {
		return nil
	}
	return os.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
}

// inWSL is whether this Linux runs in WSL, with Windows' interop on.
func inWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}

// windowsDrive is whether dir is on a Windows drive WSL mounts:
// /mnt/<letter> or below it.
func windowsDrive(dir string) bool {
	rest, ok := strings.CutPrefix(dir, "/mnt/")
	if !ok || rest == "" {
		return false
	}
	c := rest[0] | 0x20
	return c >= 'a' && c <= 'z' && (len(rest) == 1 || rest[1] == '/')
}

// StandIns writes a failing program for each name in bin. On macOS each
// is run once now, in the background, so that the check macOS makes of a
// new program's first run (Program) is made while the tests start rather
// than in the first test to reach it, and without holding them up.
// Windows finds a program by PATHEXT and passes over a file with no
// extension, so there each also gets a .bat, which comes before the user's
// own codex.cmd further down PATH: without it the gateway's tests, which
// make their stand-ins here, read the installed Codex's version (#1827,
// EdwinDigital: TestCodexPoolSaysTheClientsVersion saw 0.162.0).
func StandIns(bin string, names []string) error {
	for _, name := range names {
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			return err
		}
		if runtime.GOOS == "windows" {
			if err := os.WriteFile(p+".bat", []byte("@exit /b 1\r\n"), 0o755); err != nil {
				return err
			}
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
