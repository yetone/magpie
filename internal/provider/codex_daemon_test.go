package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

func TestIsManagedDaemon(t *testing.T) {
	for args, want := range map[string]bool{
		"/Users/me/.codex/packages/standalone/current/bin/codex app-server --managed-daemon --listen unix://": true,
		`C:\Users\me\.codex\bin\codex.exe app-server --managed-daemon=true`:                                   true,
		"codex -c features.x=true app-server --managed-daemon":                                                true,
		// the ChatGPT app's own codex, and other apps' app-servers
		"/Applications/ChatGPT.app/Contents/Resources/codex -c features.code_mode_host=true app-server --analytics-default-enabled": false,
		"/Users/me/Library/Application Support/Cindy/codex-package/0.156.0/bin/codex app-server --disable plugins":                  false,
		// the daemon's helpers, and the commands that drive it
		"/Users/me/.codex/packages/standalone/current/bin/codex app-server daemon pid-update-loop": false,
		"codex app-server daemon restart":               false,
		"codex --managed-daemon":                        false,
		"codex --managed-daemon app-server":             false,
		"app-server --managed-daemon":                   false,
		"node /x/server.js app-server --managed-daemon": false,
		"/x/codex-helper app-server --managed-daemon":   false,
		"": false,
	} {
		if got := isManagedDaemon(args); got != want {
			t.Errorf("isManagedDaemon(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestParsePIDFile(t *testing.T) {
	for in, want := range map[string]int{
		"4242\n": 4242,
		`{"pid":812,"processStartTime":123,"processIdentity":"x","executableIdentity":{"digest":"d"}}`: 812,
		"":           0,
		"{}":         0,
		"not a pid":  0,
		`{"pid":-3}`: 0,
		"-1":         0,
	} {
		if got, _ := parsePIDFile([]byte(in)); got != want {
			t.Errorf("parsePIDFile(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFindCodexDaemon(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	old := processCodexHome
	t.Cleanup(func() { processCodexHome = old })
	homes := map[int]string{}
	processCodexHome = func(pid int) (string, bool) {
		h, ok := homes[pid]
		return h, ok
	}
	chatgpt := proc.Process{PID: 10, Args: "/Applications/ChatGPT.app/Contents/Resources/codex app-server --analytics-default-enabled"}
	helper := proc.Process{PID: 11, Args: "/x/codex app-server daemon pid-update-loop"}
	ps := []proc.Process{chatgpt, helper}
	if got := findCodexDaemon(ps, home); got != 0 {
		t.Fatalf("no managed daemon: got %d", got)
	}
	ps = append(ps, proc.Process{PID: 20, Args: "/x/codex app-server --managed-daemon"},
		proc.Process{PID: 30, Args: "/y/codex app-server --managed-daemon"})
	if got := findCodexDaemon(ps, home); got != 20 {
		t.Fatalf("no pid file: got %d, want the first, 20", got)
	}
	// the pid file of this home names the one that is its
	os.MkdirAll(filepath.Join(home, "app-server-daemon"), 0o700)
	os.WriteFile(filepath.Join(home, "app-server-daemon", "app-server.pid"), []byte(`{"pid":30}`), 0o600)
	if got := findCodexDaemon(ps, home); got != 30 {
		t.Fatalf("pid file: got %d, want 30", got)
	}
	// one known to run for another CODEX_HOME isn't this home's
	homes[30] = "/elsewhere/.codex"
	homes[20] = home + "/"
	if got := findCodexDaemon(ps, home); got != 20 {
		t.Fatalf("other home: got %d, want 20", got)
	}
	homes[20] = "/elsewhere/too"
	if got := findCodexDaemon(ps, home); got != 0 {
		t.Fatalf("all for other homes: got %d", got)
	}
}

// fakeDaemon has the process list show the managed daemon as pid (none
// when 0), or fail when pid is -1.
func fakeDaemon(t *testing.T) *int {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pid := new(int)
	old, oldHome := listProcesses, processCodexHome
	t.Cleanup(func() {
		listProcesses, processCodexHome = old, oldHome
		DismissCodexDaemon()
	})
	processCodexHome = func(int) (string, bool) { return "", false }
	listProcesses = func(context.Context) ([]proc.Process, error) {
		switch *pid {
		case -1:
			return nil, errors.New("no ps")
		case 0:
			return []proc.Process{{PID: 7, Args: "codex app-server"}}, nil
		}
		return []proc.Process{{PID: 7, Args: "codex app-server"}, {PID: *pid, Args: "codex app-server --managed-daemon"}}, nil
	}
	DismissCodexDaemon()
	return pid
}

func TestCodexDaemonStale(t *testing.T) {
	pid := fakeDaemon(t)
	recheck := func() { codexDaemonMu.Lock(); codexDaemonChecked = time.Time{}; codexDaemonMu.Unlock() }

	// no daemon running: nothing to say
	noteCodexSwitch("a@x", "b@x")
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("no daemon: %q", got)
	}
	// one running when the account changes stays on the one before
	*pid = 50
	noteCodexSwitch("a@x", "b@x")
	if got := CodexDaemonStale(); got != "a@x" {
		t.Fatalf("got %q, want a@x", got)
	}
	// and still after another switch, to a third
	noteCodexSwitch("b@x", "c@x")
	if got := CodexDaemonStale(); got != "a@x" {
		t.Fatalf("after a third: got %q, want a@x", got)
	}
	// switched back to the one it started on, it is right again
	noteCodexSwitch("c@x", "A@x")
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("back on its own: %q", got)
	}
	// restarted since: a new process, which read the new sign-in
	noteCodexSwitch("a@x", "b@x")
	*pid = 51
	recheck()
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("restarted: %q", got)
	}
	// the list failing says nothing about it: what was known stays
	noteCodexSwitch("b@x", "a@x")
	*pid = -1
	recheck()
	if got := CodexDaemonStale(); got != "b@x" {
		t.Fatalf("list failing: got %q, want b@x", got)
	}
	DismissCodexDaemon()
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("dismissed: %q", got)
	}
}

// fakeCodex puts a codex that writes down how it was run in place of the
// real one, which is never run: it would restart the user's daemon.
func fakeCodex(t *testing.T, exit int) (out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for codex")
	}
	dir := t.TempDir()
	out = filepath.Join(dir, "out")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > '" + out + ".args'\n" +
		"echo \"$CODEX_HOME\" > '" + out + ".home'\n" +
		"if [ -p /dev/stdin ]; then echo pipe; else echo other; fi > '" + out + ".stdin'\n" +
		"echo 'starting…'\n"
	if exit != 0 {
		script += "echo 'Error: background server socket is stale or unreachable' >&2\nexit " + string(rune('0'+exit)) + "\n"
	}
	exe := filepath.Join(dir, "codex")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := codexExecutable
	t.Cleanup(func() { codexExecutable = old })
	codexExecutable = func() string { return exe }
	return out
}

func TestRestartCodexDaemon(t *testing.T) {
	pid := fakeDaemon(t)
	t.Setenv("CODEX_HOME", "/somewhere/else")
	out := fakeCodex(t, 0)
	*pid = 50
	noteCodexSwitch("a@x", "b@x")
	if err := RestartCodexDaemon(context.Background()); err != nil {
		t.Fatal(err)
	}
	read := func(ext string) string {
		b, _ := os.ReadFile(out + ext)
		return strings.TrimSpace(string(b))
	}
	if got := read(".args"); got != "app-server daemon restart" {
		t.Errorf("args %q", got)
	}
	home, _ := os.UserHomeDir()
	if got, want := read(".home"), filepath.Join(home, ".codex"); got != want {
		t.Errorf("CODEX_HOME %q, want %q (where magpie writes auth.json)", got, want)
	}
	if got := read(".stdin"); got != "other" {
		t.Errorf("stdin is a %s, want the null device", got)
	}
	codexDaemonMu.Lock()
	left := codexDaemonLeft
	codexDaemonMu.Unlock()
	if left != (leftDaemon{}) {
		t.Errorf("still noted after a restart: %+v", left)
	}
}

func TestRestartCodexDaemonFails(t *testing.T) {
	pid := fakeDaemon(t)
	fakeCodex(t, 2)
	*pid = 50
	noteCodexSwitch("a@x", "b@x")
	err := RestartCodexDaemon(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale or unreachable") {
		t.Fatalf("err %v", err)
	}
	if got := CodexDaemonStale(); got != "a@x" {
		t.Fatalf("a failed restart forgot the daemon: %q", got)
	}
	codexExecutable = func() string { return "" }
	if err := RestartCodexDaemon(context.Background()); err == nil {
		t.Fatal("no codex: no error")
	}
}

// Switching Codex's account notes the daemon left on the one before; the
// switch to the account Codex is on already changes nothing.
func TestSwitchLoginNotesCodexDaemon(t *testing.T) {
	pid := fakeDaemon(t)
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	*pid = 50
	if err := SwitchLogin("codex", "work@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("no switch, yet %q", got)
	}
	if err := SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := CodexDaemonStale(); got != "work@example.com" {
		t.Fatalf("got %q, want work@example.com", got)
	}
}
