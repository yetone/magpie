package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
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
	t.Setenv("USERPROFILE", t.TempDir())
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
		"echo \"$HTTPS_PROXY\" > '" + out + ".proxy'\n" +
		"if [ -p /dev/stdin ]; then echo pipe; else echo other; fi > '" + out + ".stdin'\n" +
		"echo 'starting…'\n"
	if exit != 0 {
		script += "echo 'Error: background server socket is stale or unreachable' >&2\nexit " + string(rune('0'+exit)) + "\n"
	}
	exe := filepath.Join(dir, "codex")
	testenv.Program(t, exe, script)
	old, oldEnv, oldPath := codexExecutable, daemonLoginEnv, daemonLoginPath
	t.Cleanup(func() { codexExecutable, daemonLoginEnv, daemonLoginPath = old, oldEnv, oldPath })
	codexExecutable = func() string { return exe }
	// no login shell is asked
	daemonLoginEnv = func(string) string { return "" }
	daemonLoginPath = func() string { return "" }
	return out
}

func TestRestartCodexDaemon(t *testing.T) {
	pid := fakeDaemon(t)
	t.Setenv("CODEX_HOME", "/somewhere/else")
	out := fakeCodex(t, 0)
	t.Setenv("HTTPS_PROXY", "")
	daemonLoginEnv = func(k string) string {
		if k == "HTTPS_PROXY" {
			return "http://127.0.0.1:7890"
		}
		return ""
	}
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
	// the daemon of the CODEX_HOME magpie writes auth.json in, which is the
	// user's own CODEX_HOME when that is set
	if got, want := read(".home"), "/somewhere/else"; got != want || filepath.Clean(got) != filepath.Dir(codexAuthPath()) {
		t.Errorf("CODEX_HOME %q, want %q (where magpie writes auth.json)", got, want)
	}
	// the proxy a terminal has, which magpie opened from the desktop hasn't
	if got := read(".proxy"); got != "http://127.0.0.1:7890" {
		t.Errorf("HTTPS_PROXY %q, want the login shell's", got)
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

// The daemon a restart leaves running answers for itself to macOS's
// privacy prompts, not to magpie: every codex attached to it works in its
// own folder, and the first one in ~/Desktop had macOS say magpie wants
// the Desktop folder (reformed9448 on Discord).
func TestRestartedCodexDaemonIsNotMagpies(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS's responsibility for a process")
	}
	self := proc.Responsible(os.Getpid())
	if self == 0 {
		t.Skip("macOS doesn't say who is responsible for this process")
	}
	fakeCodex(t, 0)
	// a codex whose restart leaves a daemon (a sleep) running, and says
	// its pid
	dir := t.TempDir()
	left := filepath.Join(dir, "daemon.pid")
	exe := filepath.Join(dir, "codex")
	testenv.Program(t, exe, "#!/bin/sh\nsleep 30 >/dev/null 2>&1 </dev/null &\necho $! > '"+left+"'\n")
	codexExecutable = func() string { return exe }
	if err := RestartCodexDaemon(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(left)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid %q", b)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	})
	if got := proc.Responsible(pid); got == 0 || got == self {
		t.Fatalf("the daemon answers to %d, magpie to %d: want one of its own", got, self)
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

func TestIsCodexClient(t *testing.T) {
	for args, want := range map[string]bool{
		// the TUI as ps lists it (argv[0] alone), and run by path
		"codex":                              true,
		"/opt/homebrew/bin/codex -m gpt-6":   true,
		"codex exec hello":                   true,
		"codex resume --last":                true,
		"codex app-server proxy":             true,
		`"C:\Program Files\Codex\codex.exe"`: true,
		`C:\Users\me\AppData\Roaming\npm\node_modules\@openai\codex\vendor\x86_64-pc-windows-msvc\codex\codex.exe`: true,
		// the daemon, its helpers, the commands that drive it
		"/Users/me/.codex/packages/app-server-daemon/releases/0.162.0-aarch64-apple-darwin/bin/codex app-server --listen unix:///x --managed-daemon": false,
		"codex app-server daemon pid-update-loop": false,
		"codex app-server daemon restart":         false,
		// apps' and editors' own, which don't use the daemon
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex -c features.code_mode_host=true app-server": false,
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex exec-server --remote x":                     false,
		"/Users/me/.vscode/extensions/openai.chatgpt-1.0/bin/darwin-aarch64/codex app-server":                                                 false,
		"codex mcp-server": false,
		`"C:\Program Files\WindowsApps\OpenAI.Codex_1.0\app\resources\codex.exe" app-server`: false,
		codexWindowsAppServer: false,
		// other programs
		"/Users/me/.codex/plugins/cache/chrome/extension-host ChatGPT for Chrome": false,
		// a path with a space, as the user's own ps showed it
		"/Users/me/.codex/computer-use/Codex Computer Use.app/Contents/MacOS/SkyComputerUseService": false,
		"codex-code-mode-host": false,
		"node /x/codex.js":     false,
		"":                     false,
	} {
		if got := isCodexClient(args); got != want {
			t.Errorf("isCodexClient(%q) = %v, want %v", args, got, want)
		}
	}
}

// writeDaemonPID writes the pid file Codex keeps for its daemon, dated at.
func writeDaemonPID(t *testing.T, home, body string, at time.Time) string {
	t.Helper()
	p := filepath.Join(home, "app-server-daemon", "daemon.pid")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// luci (Discord, Codex 0.162): the daemon codex sessions attach to was
// started on Codex's own models; wired to magpie after, every new codex
// showed the official list. magpie restarts a daemon started before the
// change while no codex session is on it, and only then.
func TestKeepCodexDaemonCurrent(t *testing.T) {
	pid := fakeDaemon(t)
	out := fakeCodex(t, 0)
	home, _ := os.UserHomeDir()
	home = filepath.Join(home, ".codex")
	clients := 0
	listProcesses = func(context.Context) ([]proc.Process, error) {
		if *pid == 0 {
			return nil, nil
		}
		ps := []proc.Process{{PID: *pid, Args: home + "/packages/app-server-daemon/releases/0.162.0/bin/codex app-server --listen unix:///x --managed-daemon"}}
		for i := range clients {
			ps = append(ps, proc.Process{PID: 100 + i, Args: "codex"})
		}
		return ps, nil
	}
	ran := func() bool {
		b, err := os.ReadFile(out + ".args")
		os.Remove(out + ".args")
		return err == nil && strings.TrimSpace(string(b)) == "app-server daemon restart"
	}
	ctx := context.Background()
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	*pid = 50
	pidFile := writeDaemonPID(t, home, `{"pid":50,"processStartTime":"x"}`, started)

	// started before the change, no session on it: restarted, in home
	c := KeepCodexDaemonCurrent(ctx, home, time.Now())
	if !c.Behind || !c.Restarted || c.Err != nil || !ran() {
		t.Fatalf("idle daemon behind: %+v", c)
	}
	if b, _ := os.ReadFile(out + ".home"); strings.TrimSpace(string(b)) != home {
		t.Errorf("CODEX_HOME %q, want %q", b, home)
	}
	// a codex session on it: said, left to the user
	clients = 1
	c = KeepCodexDaemonCurrent(ctx, home, time.Now())
	if !c.Behind || c.Restarted || c.Attached != 1 || ran() {
		t.Fatalf("session attached: %+v", c)
	}
	if !c.Since.Equal(started) {
		t.Errorf("since %v, want %v", c.Since, started)
	}
	clients = 0
	// started after the change: it has the list
	if c := KeepCodexDaemonCurrent(ctx, home, started.Add(-time.Minute)); c.Behind || ran() {
		t.Fatalf("daemon newer than the change: %+v", c)
	}
	// nothing changed yet
	if c := KeepCodexDaemonCurrent(ctx, home, time.Time{}); c.Behind || ran() {
		t.Fatalf("no change: %+v", c)
	}
	// a pid file naming another process: when it started isn't known
	writeDaemonPID(t, home, `{"pid":51}`, started)
	if c := KeepCodexDaemonCurrent(ctx, home, time.Now()); c.Behind || ran() {
		t.Fatalf("pid file of another: %+v", c)
	}
	// no pid file: the same
	os.Remove(pidFile)
	if c := KeepCodexDaemonCurrent(ctx, home, time.Now()); c.Behind || ran() {
		t.Fatalf("no pid file: %+v", c)
	}
	// no daemon
	writeDaemonPID(t, home, "50", started)
	*pid = 0
	if c := KeepCodexDaemonCurrent(ctx, home, time.Now()); c.Behind || ran() {
		t.Fatalf("no daemon: %+v", c)
	}
}

// The processes are listed only when a pid file says a daemon started
// before the change: listing them is a PowerShell run on Windows, which
// the look every 15s made for as long as magpie ran (#2341).
func TestKeepCodexDaemonCurrentListsOnlyWhenBehind(t *testing.T) {
	pid := fakeDaemon(t)
	fakeCodex(t, 0)
	home, _ := os.UserHomeDir()
	home = filepath.Join(home, ".codex")
	lists := 0
	listProcesses = func(context.Context) ([]proc.Process, error) {
		lists++
		return nil, nil
	}
	ctx := context.Background()
	*pid = 50
	// no pid file: no daemon to be behind
	KeepCodexDaemonCurrent(ctx, home, time.Now())
	// one written after the change: it has the list
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeDaemonPID(t, home, "50", started)
	KeepCodexDaemonCurrent(ctx, home, started.Add(-time.Minute))
	if lists != 0 {
		t.Fatalf("processes listed %d times with no daemon behind", lists)
	}
	// one written before the change: listed, to see it runs
	KeepCodexDaemonCurrent(ctx, home, time.Now())
	if lists != 1 {
		t.Fatalf("processes listed %d times for a daemon behind, want 1", lists)
	}
}

// A failed restart is said, not taken for one done.
func TestKeepCodexDaemonCurrentFails(t *testing.T) {
	pid := fakeDaemon(t)
	fakeCodex(t, 2)
	home, _ := os.UserHomeDir()
	home = filepath.Join(home, ".codex")
	*pid = 50
	writeDaemonPID(t, home, "50", time.Now().Add(-time.Hour))
	c := KeepCodexDaemonCurrent(context.Background(), home, time.Now())
	if !c.Behind || c.Restarted || c.Err == nil {
		t.Fatalf("%+v", c)
	}
}

func TestFindCodexApp(t *testing.T) {
	// the ChatGPT app's own app-server, as ps listed it on the owner's Mac
	// (ChatGPT 26.1007), beside a terminal codex and the managed daemon
	app := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex -c features.code_mode_host=true app-server --analytics-default-enabled -c plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=true"
	for want, ps := range map[int][]proc.Process{
		9:     {{PID: 3, Args: "codex"}, {PID: 4, Args: "/Users/me/.codex/packages/app-server-daemon/releases/0.162.0-aarch64-apple-darwin/bin/codex app-server --listen unix:///x --managed-daemon"}, {PID: 9, Args: app}},
		8:     {{PID: 8, Args: `"C:\Program Files\WindowsApps\OpenAI.Codex_1.0\app\resources\codex.exe" app-server`}},
		61552: {{PID: 3, Args: `C:\Users\yeton\AppData\Local\Microsoft\WinGet\Links\codex.exe`}, {PID: 61552, Args: codexWindowsAppServer}},
		// the Linux app's, beside npm's codex running an app-server of
		// its own (pota on Discord: no account switch for it on Linux)
		1957170: {{PID: 3, Args: "/usr/lib/node_modules/@openai/codex/vendor/x86_64-unknown-linux-musl/codex/codex app-server"}, {PID: 1957170, Args: codexLinuxAppServer}},
		0: {
			{PID: 3, Args: "codex app-server proxy"},
			{PID: 5, Args: "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex exec-server --remote x"},
			{PID: 6, Args: "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex app-server daemon restart"},
			{PID: 7, Args: "/Users/me/.vscode/extensions/openai.chatgpt-1.0/bin/darwin-aarch64/codex app-server"},
			{PID: 10, Args: "/Users/me/.codex/computer-use/Codex Computer Use.app/Contents/MacOS/SkyComputerUseService app-server"},
			{PID: 11, Args: "/usr/lib/chatgpt/ChatGPT --type=renderer"},
			{PID: 12, Args: "/home/me/.local/bin/codex app-server"},
			{PID: 13, Args: "chatgpt/resources/codex app-server"},
		},
	} {
		if got := findCodexApp(ps); got != want {
			t.Errorf("findCodexApp = %d, want %d (%v)", got, want, ps)
		}
	}
}

// The Codex app reads the sign-in once, as it opens: switched while it
// runs, it stays on the account before, shows that account's limits and,
// once they are spent, sends in no thread (CavillZhang on X). magpie says
// so until the app is opened again.
func TestCodexAppStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	app := new(int)
	old := listProcesses
	t.Cleanup(func() {
		listProcesses = old
		DismissCodexDaemon()
		DismissCodexApp()
	})
	listProcesses = func(context.Context) ([]proc.Process, error) {
		if *app < 0 {
			return nil, errors.New("no ps")
		}
		ps := []proc.Process{{PID: 7, Args: "codex"}}
		if *app > 0 {
			ps = append(ps, proc.Process{PID: *app, Args: "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex -c features.code_mode_host=true app-server --analytics-default-enabled"})
		}
		return ps, nil
	}
	recheck := func() { codexDaemonMu.Lock(); codexDaemonChecked = time.Time{}; codexDaemonMu.Unlock() }
	DismissCodexApp()

	noteCodexSwitch("a@x", "b@x")
	if got := CodexAppStale(); got != "" {
		t.Fatalf("app not open: %q", got)
	}
	*app = 60
	noteCodexSwitch("a@x", "b@x")
	if got := CodexAppStale(); got != "a@x" {
		t.Fatalf("got %q, want a@x", got)
	}
	if got := CodexDaemonStale(); got != "" {
		t.Fatalf("the app's app-server taken for the daemon: %q", got)
	}
	noteCodexSwitch("b@x", "c@x")
	if got := CodexAppStale(); got != "a@x" {
		t.Fatalf("after a third: got %q, want a@x", got)
	}
	noteCodexSwitch("c@x", "a@x")
	if got := CodexAppStale(); got != "" {
		t.Fatalf("back on its own: %q", got)
	}
	// quit and opened again: a new app-server, which read the new sign-in
	noteCodexSwitch("a@x", "b@x")
	*app = 61
	recheck()
	if got := CodexAppStale(); got != "" {
		t.Fatalf("reopened: %q", got)
	}
	noteCodexSwitch("b@x", "a@x")
	*app = -1
	recheck()
	if got := CodexAppStale(); got != "b@x" {
		t.Fatalf("list failing: got %q, want b@x", got)
	}
	DismissCodexApp()
	if got := CodexAppStale(); got != "" {
		t.Fatalf("dismissed: %q", got)
	}
}

// An app-server that went with none started in its place left the app
// with nothing that read the new sign-in (蛋仔 on Discord, Windows: "Codex
// app-server process is not available" till Codex was reopened): while
// the app runs, that is still said; once it quits, or it starts another,
// it isn't.
func TestCodexAppStaleWhileNoServerCameBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	server, open := 60, true
	oldList, oldOpen := listProcesses, CodexAppOpen
	t.Cleanup(func() {
		listProcesses, CodexAppOpen = oldList, oldOpen
		DismissCodexDaemon()
		DismissCodexApp()
		codexApp.Lock()
		codexApp.switched = false
		codexApp.Unlock()
	})
	listProcesses = func(context.Context) ([]proc.Process, error) {
		if server == 0 {
			return nil, nil
		}
		return []proc.Process{{PID: server, Args: codexWindowsAppServer}}, nil
	}
	CodexAppOpen = func(context.Context) (bool, bool) { return open, true }
	recheck := func() { codexDaemonMu.Lock(); codexDaemonChecked = time.Time{}; codexDaemonMu.Unlock() }
	DismissCodexApp()

	noteCodexSwitch("a@x", "b@x")
	server = 0 // ended, and the app started none
	recheck()
	if got := CodexAppStale(); got != "a@x" {
		t.Fatalf("app open with no app-server: got %q, want a@x", got)
	}
	open = false
	recheck()
	if got := CodexAppStale(); got != "" {
		t.Fatalf("app quit: %q", got)
	}

	open, server = true, 60
	noteCodexSwitch("a@x", "b@x")
	server = 61
	recheck()
	if got := CodexAppStale(); got != "" {
		t.Fatalf("a new app-server: %q", got)
	}
}

// The daemon magpie restarts runs with the login shell's proxy and PATH,
// as one a terminal started would (guotianci6 on X): magpie opened from
// the desktop has neither.
func TestCodexDaemonEnvIsTheTerminals(t *testing.T) {
	oldEnv, oldPath := daemonLoginEnv, daemonLoginPath
	t.Cleanup(func() { daemonLoginEnv, daemonLoginPath = oldEnv, oldPath })
	login := map[string]string{"https_proxy": "http://127.0.0.1:7890", "NO_PROXY": "localhost"}
	daemonLoginEnv = func(k string) string { return login[k] }
	daemonLoginPath = func() string { return "/home/u/.local/bin:/usr/bin" }
	got := codexDaemonEnv([]string{"HOME=/home/u", "PATH=/usr/bin", "no_proxy=*", "LANG=C"})
	want := []string{"HOME=/home/u", "LANG=C", "PATH=/home/u/.local/bin:/usr/bin", "https_proxy=http://127.0.0.1:7890", "NO_PROXY=localhost"}
	if !slices.Equal(got, want) {
		t.Errorf("env %q, want %q", got, want)
	}
	// a login shell with no proxy and no PATH to say leaves magpie's own
	login = nil
	daemonLoginPath = func() string { return "" }
	in := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://p:1"}
	if got := codexDaemonEnv(in); !slices.Equal(got, in) {
		t.Errorf("env %q, want %q", got, in)
	}
}

// CodexPIDs finds what can hold a thread open on the gateway's address
// as magpie quits: the Codex app, a CLI session, the app's app-server and
// a managed daemon; never another program, nor magpie itself.
func TestCodexPIDsFindsOpenCodex(t *testing.T) {
	old := listProcesses
	t.Cleanup(func() { listProcesses = old })
	listProcesses = func(context.Context) ([]proc.Process, error) {
		return []proc.Process{
			{PID: 11, Args: "/Applications/Codex.app/Contents/MacOS/Codex"},
			{PID: 12, Args: "/opt/homebrew/bin/codex resume"},
			{PID: 13, Args: codexWindowsAppServer},
			{PID: 14, Args: "/Users/u/.codex/bin/codex app-server --managed-daemon"},
			{PID: 15, Args: "/usr/bin/vim codex.md"},
			{PID: 16, Args: "/Applications/Codexify.app/Contents/MacOS/Codexify"},
			{PID: os.Getpid(), Args: "/usr/local/bin/codex"},
		}, nil
	}
	if got := CodexPIDs(context.Background()); !slices.Equal(got, []int{11, 12, 13, 14}) {
		t.Fatalf("got %v", got)
	}
	listProcesses = func(context.Context) ([]proc.Process, error) { return nil, errors.New("ps failed") }
	if got := CodexPIDs(context.Background()); got != nil {
		t.Fatalf("a failed list gave %v", got)
	}
}

// CLJY (Discord): before npm updates Codex on Windows, magpie stops the
// app-server daemon when no codex session is on it, since it holds the
// package's codex.exe, which npm can't remove while it runs. One with a
// session on it, or none running, is left.
func TestStopIdleCodexDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for codex")
	}
	fakeDaemon(t)
	out := fakeCodex(t, 0)
	home := codexHome()
	clients, daemon := 0, true
	listProcesses = func(context.Context) ([]proc.Process, error) {
		var ps []proc.Process
		if daemon {
			ps = append(ps, proc.Process{PID: 50, Args: home + "/packages/app-server-daemon/releases/0.162.0/bin/codex app-server --listen unix:///x --managed-daemon"})
		}
		for i := range clients {
			ps = append(ps, proc.Process{PID: 100 + i, Args: "codex"})
		}
		return ps, nil
	}
	ran := func() string {
		b, _ := os.ReadFile(out + ".args")
		os.Remove(out + ".args")
		return strings.TrimSpace(string(b))
	}
	ctx := context.Background()
	if stopped, err := StopIdleCodexDaemon(ctx); !stopped || err != nil || ran() != "app-server daemon stop" {
		t.Fatalf("idle daemon: stopped %t, %v", stopped, err)
	}
	if b, _ := os.ReadFile(out + ".home"); strings.TrimSpace(string(b)) != home {
		t.Errorf("CODEX_HOME %q, want %q", b, home)
	}
	clients = 1
	if stopped, err := StopIdleCodexDaemon(ctx); stopped || err != nil || ran() != "" {
		t.Fatalf("a session on it: stopped %t, %v", stopped, err)
	}
	clients, daemon = 0, false
	if stopped, err := StopIdleCodexDaemon(ctx); stopped || err != nil || ran() != "" {
		t.Fatalf("no daemon: stopped %t, %v", stopped, err)
	}
}
