package provider

// Codex's background app-server. Codex CLI (0.156 on) can keep one running
// for its sessions to attach to — `codex app-server --managed-daemon`,
// started by the CLI and left running — and it reads the ChatGPT sign-in
// once, when it starts. So when magpie switches the account Codex is signed
// in to, a Codex session opened after that still runs on the account before
// until the daemon is restarted. magpie doesn't restart it on its own:
// that ends the sessions running on it. It says so instead, and restarts it
// when asked (RestartCodexDaemon).
//
// The daemon is told apart by --managed-daemon: the app-servers other apps
// run for themselves (the ChatGPT app's own codex, an editor's) are started
// without it, and they are none of magpie's business.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// CodexDaemonRestart is the command that restarts Codex's app-server, for
// the user to run themselves.
const CodexDaemonRestart = "codex app-server daemon restart"

// listProcesses lists the processes that may be Codex's; a var so tests
// can fake it.
var listProcesses = func(ctx context.Context) ([]proc.Process, error) {
	return proc.List(ctx, "codex")
}

// processCodexHome is the CODEX_HOME a process was started with, when it
// can be read (Linux, a process of the user's): "" is unset. A var so tests
// can fake it.
var processCodexHome = func(pid int) (home string, ok bool) {
	if runtime.GOOS != "linux" {
		return "", false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if v, found := bytes.CutPrefix(kv, []byte("CODEX_HOME=")); found {
			return string(v), true
		}
	}
	return "", true
}

// codexHome is the Codex home magpie signs Codex in through.
func codexHome() string { return filepath.Dir(codexAuthPath()) }

// isManagedDaemon reports whether a command line is Codex's managed
// app-server: `codex [-c k=v…] app-server --managed-daemon …`. Its helpers
// (`codex app-server daemon pid-update-loop`), other apps' app-servers and
// other programs given those words aren't.
func isManagedDaemon(args string) bool {
	f := strings.Fields(args)
	i := slices.Index(f, "app-server")
	if i < 1 || !slices.ContainsFunc(f[:i], func(a string) bool {
		base := strings.ToLower(a[strings.LastIndexAny(a, `/\`)+1:])
		return base == "codex" || base == "codex.exe"
	}) {
		return false
	}
	return slices.ContainsFunc(f[i+1:], func(a string) bool {
		return a == "--managed-daemon" || strings.HasPrefix(a, "--managed-daemon=")
	})
}

// daemonPIDs are the ids Codex wrote down for the app-server it runs out of
// home: app-server-daemon/app-server.pid (daemon.pid before), a PidRecord
// ({"pid": …, …}) or a bare number.
func daemonPIDs(home string) []int {
	var ids []int
	for _, name := range []string{"app-server.pid", "daemon.pid"} {
		b, err := os.ReadFile(filepath.Join(home, "app-server-daemon", name))
		if err != nil {
			continue
		}
		if id, ok := parsePIDFile(b); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func parsePIDFile(b []byte) (int, bool) {
	b = bytes.TrimSpace(b)
	if id, err := strconv.Atoi(string(b)); err == nil && id > 0 {
		return id, true
	}
	var rec struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &rec) == nil && rec.PID > 0 {
		return rec.PID, true
	}
	return 0, false
}

// findCodexDaemon picks, among the running processes, Codex's managed
// app-server for home: 0 when none runs. One started for another
// CODEX_HOME is left out where that can be read; where it can't, the one
// home's pid file names is taken, else the first.
func findCodexDaemon(ps []proc.Process, home string) int {
	var ids []int
	for _, p := range ps {
		if !isManagedDaemon(p.Args) {
			continue
		}
		if h, ok := processCodexHome(p.PID); ok && h != "" && filepath.Clean(h) != filepath.Clean(home) {
			continue
		}
		ids = append(ids, p.PID)
	}
	if len(ids) == 0 {
		return 0
	}
	for _, id := range daemonPIDs(home) {
		if slices.Contains(ids, id) {
			return id
		}
	}
	return ids[0]
}

// codexDaemonLeft is the app-server that was running when Codex's account
// last changed, and the account it was started on: it stays on that one
// until it restarts.
type leftDaemon struct {
	pid  int
	user string
}

var (
	codexDaemonMu      sync.Mutex
	codexDaemonLeft    leftDaemon
	codexDaemonChecked time.Time
	// codexAppLeft is the Codex desktop app's own app-server (ChatGPT.app's
	// CodexCLI.app) that was running when Codex's account changed: it
	// keeps the account it started on until the app is quit and opened
	// again (codex-rs AuthManager reloads auth.json only for the same
	// account), so the app goes on reading that account's limits, and
	// stops sending in every thread once those are spent.
	codexAppLeft leftDaemon
)

// codexDaemonRecheck is how long CodexDaemonStale trusts what it last saw.
const codexDaemonRecheck = 5 * time.Second

// noteCodexSwitch remembers, when Codex's account changes from one to
// another, the app-server left running on the one before.
func noteCodexSwitch(from, to string) { noteCodexSwitchBy(from, to, false) }

// noteCodexSwitchBy is noteCodexSwitch, byMagpie saying magpie made the
// switch by itself (switchLoginBy).
func noteCodexSwitchBy(from, to string, byMagpie bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// the Codex app's app-servers are found and restarted on
	// KeepCodexAppOnItsProvider's own look, whatever this listing finds.
	// A switch by hand off an account known held is one off a used-up
	// account, as SwitchWhenSpent's is: a turn left open on it when quota
	// ran out mid-turn is restarted past once quiet (xiaoq6285 on Discord)
	codexAppSwitched(codexHeldKnown(from), byMagpie)
	ps, err := listProcesses(ctx)
	known := err == nil
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	codexDaemonChecked = time.Now()
	if !known {
		return
	}
	noteLeft(&codexDaemonLeft, findCodexDaemon(ps, codexHome()), from, to)
	noteLeft(&codexAppLeft, findCodexApp(ps), from, to)
}

// noteLeft updates what is known of the app-server pid, running as Codex's
// account changes from one to another.
func noteLeft(left *leftDaemon, pid int, from, to string) {
	switch {
	case pid == 0:
		*left = leftDaemon{}
	case pid == left.pid:
		// it is on the account it started with, whatever came between
		if strings.EqualFold(to, left.user) {
			*left = leftDaemon{}
		}
	default:
		*left = leftDaemon{pid, from}
	}
}

// CodexDaemonStale answers the account Codex's app-server is still signed
// in to when Codex has been switched to another since it started, "" when
// it isn't running or is on the account Codex is.
func CodexDaemonStale() string {
	codexDaemonMu.Lock()
	recheckLeft()
	was := codexDaemonLeft.user
	codexDaemonMu.Unlock()
	return leftOnAccount(was)
}

// leftOnAccount is was, the account an app-server is left on, or "" when
// Codex's provider takes no ChatGPT account (CodexNoAccount): the
// app-server goes by none, so nothing is to be restarted for it.
func leftOnAccount(was string) string {
	if was == "" || codexNoAccount() {
		return ""
	}
	return was
}

// CodexAppStale answers the account the Codex desktop app is still signed
// in to when Codex has been switched to another since the app opened, ""
// when it isn't open or is on the account Codex is. The app shows that
// account's limits, and once they are spent won't send in any thread,
// until it is quit and opened again.
func CodexAppStale() string {
	codexDaemonMu.Lock()
	recheckLeft()
	was := codexAppLeft.user
	codexDaemonMu.Unlock()
	return leftOnAccount(was)
}

// recheckLeft forgets an app-server left on an account before that has
// restarted, or stopped, since: a new one reads the sign-in afresh. What
// was known stays when the processes can't be listed. codexDaemonMu is
// held.
func recheckLeft() {
	if codexDaemonLeft.pid == 0 && codexAppLeft.pid == 0 || time.Since(codexDaemonChecked) <= codexDaemonRecheck {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ps, err := listProcesses(ctx)
	cancel()
	codexDaemonChecked = time.Now()
	if err != nil {
		return
	}
	if findCodexDaemon(ps, codexHome()) != codexDaemonLeft.pid {
		codexDaemonLeft = leftDaemon{}
	}
	// one that went with none in its place left an app still on nothing
	// new while it runs (codexAppGotNone): the app read no sign-in
	// afresh, so it is still said
	if p := findCodexApp(ps); p != codexAppLeft.pid && (p != 0 || !codexAppStillOpen()) {
		codexAppLeft = leftDaemon{}
	}
}

// codexAppStillOpen is whether the Codex desktop app is known to run
// (CodexAppOpen), for an app-server gone with none in its place.
func codexAppStillOpen() bool {
	if CodexAppOpen == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	open, known := CodexAppOpen(ctx)
	return open && known
}

// DismissCodexDaemon stops saying the app-server is on another account.
func DismissCodexDaemon() {
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	codexDaemonLeft = leftDaemon{}
}

// DismissCodexApp stops saying the Codex app is on another account.
func DismissCodexApp() {
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	codexAppLeft = leftDaemon{}
}

// findCodexApp picks, among the running processes, the app-server the
// Codex desktop app runs for itself: 0 when it isn't open. On macOS that
// is ChatGPT.app's (or Codex.app's) bundled codex, `…/CodexCLI.app/
// Contents/MacOS/codex [-c k=v…] app-server --analytics-default-enabled …`;
// on Windows the Store app's (OpenAI.Codex, ChatGPT.exe), which copies its
// codex out of WindowsApps and runs it from %LOCALAPPDATA%:
// `C:\Users\…\AppData\Local\OpenAI\Codex\bin\<hash>\codex.exe -c … app-server
// --analytics-default-enabled …` (26.1021 on the Windows box); on Linux
// the official app's (the chatgpt deb, rpm or Arch package, 26.1027):
// `/usr/lib/chatgpt/resources/codex -c … app-server
// --analytics-default-enabled …`. Not the
// managed daemon, nor an app-server's helpers or proxy, nor an editor's
// codex.
func findCodexApp(ps []proc.Process) int {
	for _, p := range ps {
		// not another program's, run from the app's codex with --listen
		if isCodexAppOwnServer(p.Args) {
			return p.PID
		}
	}
	return 0
}

// isCodexAppServer is findCodexApp's test of one command line.
func isCodexAppServer(args string) bool {
	if isManagedDaemon(args) {
		return false
	}
	// the program's own path: ps leaves a path's spaces unquoted, so
	// "…/Codex Computer Use.app/Contents/MacOS/…" reads as one named Codex
	exe, rest := splitExe(args)
	if !strings.Contains(exe, ".app/Contents/") && !windowsCodexApp(exe) && !linuxCodexApp(exe) {
		return false
	}
	if base := strings.ToLower(exe[strings.LastIndexAny(exe, `/\`)+1:]); base != "codex" && base != "codex.exe" {
		return false
	}
	f := strings.Fields(rest)
	i := slices.Index(f, "app-server")
	return i >= 0 && (i+1 >= len(f) || strings.HasPrefix(f[i+1], "-"))
}

// windowsCodexApp is whether exe is the Windows Codex app's codex: the one
// it runs from %LOCALAPPDATA%\OpenAI\Codex\bin\<hash>\, or one in its
// package under WindowsApps.
func windowsCodexApp(exe string) bool {
	e := strings.ToLower(exe)
	return strings.Contains(e, `\appdata\local\openai\codex\bin\`) || strings.Contains(e, `\windowsapps\openai.`)
}

// linuxCodexApp is whether exe is the Linux Codex app's codex: the one in
// its resources, /usr/lib/chatgpt/resources/codex in OpenAI's packages
// (the deb's and the Arch package's, chatgpt 26.1027). Not a codex the
// user installed (npm's, a release's): those run from a folder of their
// own.
func linuxCodexApp(exe string) bool {
	return strings.HasPrefix(exe, "/") && strings.HasSuffix(exe, "/chatgpt/resources/codex")
}

// RestartCodexDaemon has Codex restart its app-server, which reads the
// sign-in again: `codex app-server daemon restart`, run with the codex CLI
// magpie finds and the Codex home it signs Codex in through. Codex 0.162's
// sessions attached to it reconnect to the new one, a turn running is let
// finish first (for up to a minute); an older Codex's are ended.
func RestartCodexDaemon(ctx context.Context) error {
	codexRestarting.Lock()
	defer codexRestarting.Unlock()
	if err := restartCodexDaemon(ctx, codexHome()); err != nil {
		return err
	}
	DismissCodexDaemon()
	return nil
}

func restartCodexDaemon(ctx context.Context, home string) error {
	cmd, err := codexDaemonCommand(ctx, home, "restart")
	if err != nil {
		return err
	}
	// run as its own responsible process: the daemon it leaves running
	// works in the folder of every codex attached to it, and macOS would
	// otherwise ask for ~/Desktop and the like in magpie's name
	// (reformed9448 on Discord)
	if out, err := proc.CombinedOutputOwn(ctx, cmd); err != nil {
		if msg := lastLine(out); msg != "" {
			return errors.New("codex app-server daemon restart: " + msg)
		}
		return errors.New("codex app-server daemon restart: " + err.Error())
	}
	return nil
}

// codexDaemonCommand is `codex app-server daemon <verb>`, with CODEX_HOME
// set to the home magpie switches, and nothing to read on stdin (Codex
// waits on a stdin left open).
func codexDaemonCommand(ctx context.Context, home, verb string) (*exec.Cmd, error) {
	exe := codexExecutable()
	if exe == "" {
		return nil, errors.New("the codex CLI isn't installed, or magpie can't find it")
	}
	cmd := proc.CommandContext(ctx, exe, "app-server", "daemon", verb)
	env := slices.DeleteFunc(codexDaemonEnv(os.Environ()), func(kv string) bool { return strings.HasPrefix(kv, "CODEX_HOME=") })
	cmd.Env = append(env, "CODEX_HOME="+home)
	cmd.Stdin = nil // os/exec reads it from the null device
	return cmd, nil
}

// daemonProxyVars are the proxy variables Codex reads, as proc.ProxyVars
// names them for the login shell.
var daemonProxyVars = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// daemonLoginEnv and daemonLoginPath are the login shell's variables and
// PATH ("" for none); vars so tests can say.
var (
	daemonLoginEnv  = proc.LoginEnv
	daemonLoginPath = func() string { return strings.Join(proc.LoginPath(), string(os.PathListSeparator)) }
)

// codexDaemonEnv is env with the PATH and proxy variables a terminal opened
// now has, from the login shell: the daemon outlives the restart and
// serves every codex started after it, so it runs as one started from a
// terminal would. magpie opened from the desktop has neither: on Linux,
// with the proxy set in .zshrc, the daemon magpie restarted after a
// ChatGPT sign-in reached chatgpt.com directly, and every codex after it
// stopped at "account/read failed: workspace routing discovery timed out"
// until the daemon was restarted from a terminal (guotianci6 on X, Codex
// 0.164). The proxy variables are taken as a set: when the login shell
// names any, magpie's own are dropped, so a NO_PROXY only magpie has
// doesn't ride along; when it names none, env's stay.
func codexDaemonEnv(env []string) []string {
	if runtime.GOOS == "windows" {
		return env // a terminal's environment is the registry's, as magpie's is
	}
	var login []string
	for _, k := range daemonProxyVars {
		if v := daemonLoginEnv(k); v != "" {
			login = append(login, k+"="+v)
		}
	}
	path := daemonLoginPath()
	out := make([]string, 0, len(env)+len(login))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if (len(login) > 0 && slices.Contains(daemonProxyVars, k)) || (path != "" && k == "PATH") {
			continue
		}
		out = append(out, kv)
	}
	if path != "" {
		out = append(out, "PATH="+path)
	}
	return append(out, login...)
}

// The daemon also builds Codex's model list once, as it starts, from
// config.toml and the catalog it names (codex-rs app-server's models
// manager): a codex session started after magpie wires Codex in, or takes
// it out, attaches to the daemon left running and shows the list from
// before — Codex's own models, or magpie's — until it restarts (luci on
// Discord, Codex 0.162). It runs per CODEX_HOME, not per terminal, and
// has no idle exit. Restarting it with no codex session attached ends
// nothing, so magpie does that itself (KeepCodexDaemonCurrent); with one
// attached, the Agents page offers it.

// A CodexDaemonCheck is what KeepCodexDaemonCurrent found and did.
type CodexDaemonCheck struct {
	// Behind: a daemon runs that started before what it reads changed
	Behind bool
	// Since is when that daemon started
	Since time.Time
	// Attached is how many codex sessions may be on it, which magpie
	// doesn't restart it under
	Attached int
	// Restarted: magpie restarted it, and new sessions get the new list
	Restarted bool
	Err       error
}

// codexRestarting keeps one restart of the daemon at a time.
var codexRestarting sync.Mutex

// KeepCodexDaemonCurrent restarts the managed app-server of the Codex home
// home when it started before changed (when magpie last changed what Codex
// reads at start) and no codex session is attached to it. One that can't
// be found or timed, or a restart already under way, is left as it is.
func KeepCodexDaemonCurrent(ctx context.Context, home string, changed time.Time) CodexDaemonCheck {
	var c CodexDaemonCheck
	if changed.IsZero() || !pidFileBehind(home, changed) {
		return c
	}
	ps, err := listProcesses(ctx)
	if err != nil {
		return c
	}
	pid := findCodexDaemon(ps, home)
	if pid == 0 {
		return c
	}
	since, ok := daemonStarted(home, pid)
	// a second's slack: the pid file and the config are written by
	// different programs, whose clocks round differently
	if !ok || !since.Before(changed.Add(-time.Second)) {
		return c
	}
	c.Behind, c.Since, c.Attached = true, since, codexClients(ps, home)
	if c.Attached > 0 || !codexRestarting.TryLock() {
		return c
	}
	defer codexRestarting.Unlock()
	if c.Err = restartCodexDaemon(ctx, home); c.Err == nil {
		c.Restarted = true
	}
	return c
}

// pidFileBehind reports whether a pid file of home's was written before
// changed: only a daemon one names can be found behind (daemonStarted), so
// with none the processes aren't listed at all. Listing them is a
// PowerShell run on Windows, which every look did every codexDaemonEvery
// for as long as magpie ran (#2341).
func pidFileBehind(home string, changed time.Time) bool {
	for _, pid := range daemonPIDs(home) {
		if since, ok := daemonStarted(home, pid); ok && since.Before(changed.Add(-time.Second)) {
			return true
		}
	}
	return false
}

// daemonStarted is when the daemon pid started: the time Codex wrote its
// pid file, which it does once, as the daemon comes up, on every OS. ok is
// false when no pid file of home's names pid.
func daemonStarted(home string, pid int) (time.Time, bool) {
	for _, name := range []string{"app-server.pid", "daemon.pid"} {
		p := filepath.Join(home, "app-server-daemon", name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if id, ok := parsePIDFile(b); !ok || id != pid {
			continue
		}
		if st, err := os.Stat(p); err == nil {
			return st.ModTime(), true
		}
	}
	return time.Time{}, false
}

// codexClients counts the codex sessions that may be attached to home's
// daemon: codex run in a terminal (its TUI, exec, resume, an app-server
// proxy); not the daemon, its helpers and the commands that drive it, nor
// an app's or an editor's codex, which serve themselves. One started for
// another CODEX_HOME is left out where that can be read.
func codexClients(ps []proc.Process, home string) int {
	n := 0
	for _, p := range ps {
		if !isCodexClient(p.Args) {
			continue
		}
		if h, ok := processCodexHome(p.PID); ok && h != "" && filepath.Clean(h) != filepath.Clean(home) {
			continue
		}
		n++
	}
	return n
}

// isCodexClient reports whether a command line is a codex session that
// may attach to the daemon.
func isCodexClient(args string) bool {
	exe, rest := splitExe(args)
	base := strings.ToLower(exe[strings.LastIndexAny(exe, `/\`)+1:])
	if base != "codex" && base != "codex.exe" {
		return false
	}
	// an app's (ChatGPT.app's CodexCLI.app) or an editor extension's; ps
	// leaves a path's spaces unquoted, so "…/Codex Computer Use.app/
	// Contents/MacOS/…" reads as a program named Codex: the whole line
	// is looked at
	if strings.Contains(args, ".app/Contents/") || strings.Contains(exe, "/extensions/") || strings.Contains(exe, `\extensions\`) {
		return false
	}
	f := strings.Fields(rest)
	if slices.Contains(f, "exec-server") || slices.Contains(f, "mcp-server") {
		return false
	}
	if i := slices.Index(f, "app-server"); i >= 0 {
		// the daemon, its helpers, `daemon restart` and other apps'
		// app-servers serve; a proxy is a client of the daemon
		return i+1 < len(f) && f[i+1] == "proxy"
	}
	return true
}

// splitExe parts a command line into the program and its arguments, the
// program quoted as Windows lists it (`"C:\Program Files\…\codex.exe" …`).
func splitExe(args string) (exe, rest string) {
	args = strings.TrimSpace(args)
	if q, ok := strings.CutPrefix(args, `"`); ok {
		if exe, rest, ok = strings.Cut(q, `"`); ok {
			return exe, rest
		}
	}
	exe, rest, _ = strings.Cut(args, " ")
	return exe, rest
}

// CodexHome is the Codex home magpie wires and signs Codex in through.
func CodexHome() string { return codexHome() }

// CodexPIDs lists the Codex processes running now that may hold a thread
// sending to magpie's gateway: the Codex app's app-server, the managed
// daemon, the CLI's sessions, an editor's codex. A relay magpie leaves at
// quit for them ends once they all have (codexrelay).
func CodexPIDs(ctx context.Context) []int {
	ps, err := listProcesses(ctx)
	if err != nil {
		return nil
	}
	var pids []int
	for _, p := range ps {
		exe, _ := splitExe(p.Args)
		base := strings.ToLower(exe[strings.LastIndexAny(exe, `/\`)+1:])
		if p.PID != os.Getpid() && (base == "codex" || base == "codex.exe" || isCodexAppServer(p.Args) || isManagedDaemon(p.Args)) {
			pids = append(pids, p.PID)
		}
	}
	return pids
}
