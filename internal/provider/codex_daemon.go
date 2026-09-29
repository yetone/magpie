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

// codexDaemon is the process id of Codex's managed app-server, 0 when none
// runs; known is false when the processes couldn't be listed.
func codexDaemon(ctx context.Context) (pid int, known bool) {
	ps, err := listProcesses(ctx)
	if err != nil {
		return 0, false
	}
	return findCodexDaemon(ps, codexHome()), true
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
)

// codexDaemonRecheck is how long CodexDaemonStale trusts what it last saw.
const codexDaemonRecheck = 5 * time.Second

// noteCodexSwitch remembers, when Codex's account changes from one to
// another, the app-server left running on the one before.
func noteCodexSwitch(from, to string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pid, known := codexDaemon(ctx)
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	codexDaemonChecked = time.Now()
	switch {
	case !known:
	case pid == 0:
		codexDaemonLeft = leftDaemon{}
	case pid == codexDaemonLeft.pid:
		// the daemon is on the account it started with, whatever came between
		if strings.EqualFold(to, codexDaemonLeft.user) {
			codexDaemonLeft = leftDaemon{}
		}
	default:
		codexDaemonLeft = leftDaemon{pid, from}
	}
}

// CodexDaemonStale answers the account Codex's app-server is still signed
// in to when Codex has been switched to another since it started, "" when
// it isn't running or is on the account Codex is.
func CodexDaemonStale() string {
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	if codexDaemonLeft.pid == 0 {
		return ""
	}
	if time.Since(codexDaemonChecked) > codexDaemonRecheck {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pid, known := codexDaemon(ctx)
		cancel()
		codexDaemonChecked = time.Now()
		if known && pid != codexDaemonLeft.pid {
			// restarted, or stopped, since: it reads the sign-in afresh
			codexDaemonLeft = leftDaemon{}
		}
	}
	return codexDaemonLeft.user
}

// DismissCodexDaemon stops saying the app-server is on another account.
func DismissCodexDaemon() {
	codexDaemonMu.Lock()
	defer codexDaemonMu.Unlock()
	codexDaemonLeft = leftDaemon{}
}

// RestartCodexDaemon has Codex restart its app-server, which reads the
// sign-in again: `codex app-server daemon restart`, run with the codex CLI
// magpie finds and the Codex home it signs Codex in through. The Codex
// sessions attached to it are ended.
func RestartCodexDaemon(ctx context.Context) error {
	cmd, err := codexDaemonCommand(ctx, "restart")
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := lastLine(out); msg != "" {
			return errors.New("codex app-server daemon restart: " + msg)
		}
		return errors.New("codex app-server daemon restart: " + err.Error())
	}
	DismissCodexDaemon()
	return nil
}

// codexDaemonCommand is `codex app-server daemon <verb>`, with CODEX_HOME
// set to the home magpie switches, and nothing to read on stdin (Codex
// waits on a stdin left open).
func codexDaemonCommand(ctx context.Context, verb string) (*exec.Cmd, error) {
	exe := codexExecutable()
	if exe == "" {
		return nil, errors.New("the codex CLI isn't installed, or magpie can't find it")
	}
	cmd := proc.CommandContext(ctx, exe, "app-server", "daemon", verb)
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "CODEX_HOME=") })
	cmd.Env = append(env, "CODEX_HOME="+codexHome())
	cmd.Stdin = nil // os/exec reads it from the null device
	return cmd, nil
}
