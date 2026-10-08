// Package wslrun runs, from magpie on Windows, a command that is installed
// only in a WSL distro: Claude Code signed in to inside Ubuntu, say. The
// command is found in a distro that is running (a stopped one is never
// started to look), and run there through wsl.exe, in the distro's own
// home and login, with what magpie hands it (its config directory, the
// files it writes, its own executable for a helper) told by their paths
// inside the distro.
package wslrun

import (
	"bufio"
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// A Tool is a command of a WSL distro's own.
type Tool struct {
	Distro string // as wsl.exe -l names it
	Path   string // the command, inside the distro
	PATH   string // the distro's PATH it was found on, which it may need (node)
	Mount  string // where Windows' drives are mounted: "/mnt/"
	Host   string // the Windows host's address inside the distro, "" when it is 127.0.0.1 (HostLoopback)
}

// On is whether there is WSL to look in: on Windows, or in tests.
var On = runtime.GOOS == "windows"

// Run runs wsl.exe and returns what it printed; a var for tests.
var Run = func(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, "wsl.exe", args...)
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	return cmd.Output()
}

var found struct {
	sync.Mutex
	at    map[string]time.Time
	tools map[string]*Tool
}

// findAge is how long an answer of Find's is kept: a distro started or
// stopped meanwhile is seen within it.
const findAge = time.Minute

// Find is the command name of a running WSL distro's own, the default
// distro first. ok is false off Windows, without WSL, with Settings'
// Detect agents in WSL off, or when no running distro has it.
func Find(name string) (Tool, bool) {
	// Settings' Detect agents in WSL, off, looks in no distro (#1264)
	if !On || settings.Load().NoWSLAgents {
		return Tool{}, false
	}
	found.Lock()
	defer found.Unlock()
	if t, ok := found.at[name]; ok && time.Since(t) < findAge {
		p := found.tools[name]
		if p == nil {
			return Tool{}, false
		}
		// a distro stopped since (wsl --shutdown, to repair WSL) isn't
		// run again, which would start it: it is looked for anew
		if Up(p.Distro) {
			return *p, true
		}
	}
	if found.at == nil {
		found.at, found.tools = map[string]time.Time{}, map[string]*Tool{}
	}
	found.at[name], found.tools[name] = time.Now(), nil
	names, ok := running()
	if !ok {
		return Tool{}, false
	}
	def := ""
	if v, err := Run(10*time.Second, "-l", "-q"); err == nil {
		if all := Distros(v); len(all) > 0 {
			def = all[0] // wsl -l lists the default first
		}
	}
	for i, n := range names {
		if n == def && i > 0 {
			names[0], names[i] = names[i], names[0]
		}
	}
	for _, d := range names {
		out, err := Run(30*time.Second, "-d", d, "-e", "sh", "-lc", probeScript(name))
		if err != nil {
			continue
		}
		if t, ok := parseProbe(d, string(out)); ok {
			found.tools[name] = &t
			return t, true
		}
	}
	return Tool{}, false
}

var up struct {
	sync.Mutex
	at    time.Time
	names []string
	ok    bool
}

// upAge is how long running's answer is kept: a distro stopped is seen
// within it.
const upAge = 3 * time.Second

// running are the distros running, as wsl.exe -l --running says (which
// starts none) at most upAge ago; ok is false when it couldn't say.
func running() (names []string, ok bool) {
	up.Lock()
	defer up.Unlock()
	if up.at.IsZero() || time.Since(up.at) > upAge {
		b, err := Run(10*time.Second, "-l", "--running", "-q")
		up.names, up.ok, up.at = Distros(b), err == nil, time.Now()
		if err != nil {
			up.names = nil
		}
	}
	return slices.Clone(up.names), up.ok
}

// Up is whether distro runs now. Anything magpie does in a distro on its
// own (not the user's asking) asks first: wsl.exe -d, or opening its files
// through \\wsl.localhost, starts a stopped one (TJHHHH on Discord: WSL
// kept starting while they repaired it). False off Windows, without WSL,
// or when wsl.exe can't say.
func Up(distro string) bool {
	if !On {
		return false
	}
	names, _ := running()
	return slices.Contains(names, distro)
}

// Known is the tool Find found last for name, however long ago, without
// looking again: for a page to say where it runs without waiting on WSL.
func Known(name string) (Tool, bool) {
	found.Lock()
	defer found.Unlock()
	if p := found.tools[name]; p != nil {
		return *p, true
	}
	return Tool{}, false
}

// Forget drops what Find knows, so the next Find looks again.
func Forget() {
	found.Lock()
	found.at, found.tools = nil, nil
	found.Unlock()
	up.Lock()
	up.at = time.Time{}
	up.Unlock()
}

// probeScript prints where name is in the distro, with the PATH it is on,
// from a login shell, then an interactive one (nvm's node is put on PATH
// by .bashrc), then where Claude Code's installer puts it; and where
// Windows' drives are mounted, the default route (the Windows host under
// NAT) and WSL's networking mode.
func probeScript(name string) string {
	return `for p in "$(command -v ` + name + ` 2>/dev/null)" ` +
		`"$(bash -ic 'command -v ` + name + `' 2>/dev/null </dev/null | tail -n1)" ` +
		`"$HOME/.local/bin/` + name + `" "/usr/local/bin/` + name + `"; do ` +
		`case "$p" in /mnt/*|"") continue;; esac; ` +
		`[ -x "$p" ] && { echo "bin:$p"; ` +
		`echo "path:$(bash -ic 'echo $PATH' 2>/dev/null </dev/null | tail -n1)"; echo "lpath:$PATH"; break; }; done; ` +
		`echo "mount:$(wslpath -u 'C:\' 2>/dev/null)"; ` +
		`ip route show default 2>/dev/null | head -n1 | sed 's/^/route:/'; ` +
		`command -v wslinfo >/dev/null 2>&1 && echo "net:$(wslinfo --networking-mode 2>/dev/null)"; true`
}

func parseProbe(distro, out string) (Tool, bool) {
	t := Tool{Distro: distro, Mount: "/mnt/"}
	var ipath, lpath, net_ string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		v = strings.TrimSpace(v)
		switch k {
		case "bin":
			t.Path = v
		case "path":
			ipath = v
		case "lpath":
			lpath = v
		case "mount":
			// /mnt/c/ → /mnt/
			if m := strings.TrimSuffix(strings.TrimSuffix(v, "/"), "c"); strings.HasPrefix(m, "/") && m != v {
				t.Mount = m
			}
		case "route":
			// default via 172.20.0.1 dev eth0 …
			if f := strings.Fields(v); len(f) >= 3 && f[1] == "via" && net.ParseIP(f[2]) != nil {
				t.Host = f[2]
			}
		case "net":
			net_ = strings.ToLower(v)
		}
	}
	if !strings.HasPrefix(t.Path, "/") {
		return Tool{}, false
	}
	t.PATH = lpath
	if strings.HasPrefix(ipath, "/") {
		t.PATH = ipath
	}
	if t.PATH == "" {
		t.PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	if dir := t.Path[:strings.LastIndex(t.Path, "/")]; dir != "" && !onPath(t.PATH, dir) {
		t.PATH = dir + ":" + t.PATH
	}
	if HostLoopback(net_) {
		t.Host = ""
	}
	return t, true
}

// HostLoopback reports whether 127.0.0.1 inside a WSL 2 distro reaches
// Windows' own 127.0.0.1 in networking mode, as wslinfo --networking-mode
// prints it: mirrored, and consomme (WSL 2.9's name for what was
// virtioproxy), whose user-mode stack relays the distro's 127.0.0.1 to
// Windows' while localhostForwarding is on, as it is unless .wslconfig
// turns it off (microsoft/WSL's ConsommeTests::LoopbackGuestToHost). Its
// default route there is Windows' own next hop (198.18.0.2 under a TUN
// proxy, #1230), never Windows. Under nat, bridged or none, and for a mode
// not known (""), 127.0.0.1 is the distro's own.
func HostLoopback(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "mirrored", "consomme", "virtioproxy":
		return true
	}
	return false
}

func onPath(path, dir string) bool {
	for _, p := range strings.Split(path, ":") {
		if p == dir {
			return true
		}
	}
	return false
}

// Command is the wsl.exe that runs t with args, in the distro, on the PATH
// it was found on. Nothing of the distro's shell runs: what the run is
// given of Windows' environment is what Env passes. ctx ending ends the
// run inside the distro too: killing wsl.exe alone leaves it there (a
// `claude auth login` would wait on for a browser that never comes).
func (t Tool) Command(ctx context.Context, args ...string) *exec.Cmd {
	return t.cancelable(proc.CommandContext(ctx, "wsl.exe", t.argv(args)...))
}

// Probe is Command for a short answer, as proc.ProbeContext runs one.
func (t Tool) Probe(ctx context.Context, args ...string) *exec.Cmd {
	return t.cancelable(proc.ProbeContext(ctx, "wsl.exe", t.argv(args)...))
}

// runVar marks the processes of a run in the distro, which they and what
// they start have in their environment.
const runVar = "MAGPIE_WSL_RUN"

func (t Tool) cancelable(cmd *exec.Cmd) *exec.Cmd {
	mark := cmd.Args[len(cmd.Args)-1]
	for _, a := range cmd.Args {
		if v, ok := strings.CutPrefix(a, runVar+"="); ok {
			mark = v
		}
	}
	cmd.Cancel = func() error {
		t.Stop(mark)
		return cmd.Process.Kill()
	}
	return cmd
}

// Stop ends the processes of the run marked mark in the distro.
func (t Tool) Stop(mark string) {
	_, _ = Run(10*time.Second, "-d", t.Distro, "--exec", "sh", "-c", stopScript, "sh", runVar+"="+mark)
}

const stopScript = `for e in /proc/[0-9]*/environ; do ` +
	`tr '\0' '\n' < "$e" 2>/dev/null | grep -qxF "$1" || continue; ` +
	`p=${e#/proc/}; kill -TERM "${p%/environ}" 2>/dev/null; done; true`

var runSeq struct {
	sync.Mutex
	n int
}

func (t Tool) argv(args []string) []string {
	runSeq.Lock()
	runSeq.n++
	mark := strconv.Itoa(os.Getpid()) + "-" + strconv.Itoa(runSeq.n)
	runSeq.Unlock()
	return append([]string{"-d", t.Distro, "--exec", "/usr/bin/env", "PATH=" + t.PATH, runVar + "=" + mark, t.Path}, args...)
}

// Linux is where Windows path win is inside the distro: C:\Users\me is
// /mnt/c/Users/me. A path on no drive letter is given back as it is.
func (t Tool) Linux(win string) string {
	if len(win) < 2 || win[1] != ':' || !isLetter(win[0]) {
		return win
	}
	rest := strings.ReplaceAll(win[2:], `\`, "/")
	mount := t.Mount
	if mount == "" {
		mount = "/mnt/"
	}
	return mount + strings.ToLower(win[:1]) + "/" + strings.TrimPrefix(rest, "/")
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// proxyVars are the proxy variables Env hands on.
var proxyVars = []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy"}

// Env is env for a run of t: WSLENV names the variables of env that go
// into the distro — those in names, their values Windows paths when they
// end in /p (WSL then gives the distro's path for them), and the proxy
// variables, whose proxy on this machine's loopback is the Windows host's
// address under NAT.
func (t Tool) Env(env []string, names ...string) []string {
	have := map[string]bool{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		have[k] = true
	}
	var pass []string
	for _, n := range names {
		if k, _, _ := strings.Cut(n, "/"); have[k] {
			pass = append(pass, n)
		}
	}
	out := make([]string, 0, len(env)+1)
	wslenv := ""
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if strings.EqualFold(k, "WSLENV") {
			wslenv = v
			continue
		}
		for _, p := range proxyVars {
			if k == p {
				if !strings.EqualFold(k, "NO_PROXY") {
					e = k + "=" + t.proxy(v)
				}
				pass = append(pass, k)
				break
			}
		}
		out = append(out, e)
	}
	if wslenv != "" {
		pass = append([]string{wslenv}, pass...)
	}
	return append(out, "WSLENV="+strings.Join(pass, ":"))
}

// proxy is proxy address v as the distro reaches it.
func (t Tool) proxy(v string) string {
	if t.Host == "" {
		return v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return v
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
		if p := u.Port(); p != "" {
			u.Host = net.JoinHostPort(t.Host, p)
		} else {
			u.Host = t.Host
		}
		return u.String()
	}
	return v
}

// Distros reads wsl.exe -l -q: UTF-16LE (with or without a BOM), or UTF-8
// under WSL_UTF8. Docker Desktop's own distros are left out.
func Distros(b []byte) []string {
	s := decode(b)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.Trim(l, "\x00\ufeff\r"))
		if l == "" || strings.HasPrefix(strings.ToLower(l), "docker-desktop") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func decode(b []byte) string {
	utf16le := len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[1] == 0 && b[0] != 0)
	if !utf16le {
		return string(b)
	}
	if b[0] == 0xff && b[1] == 0xfe {
		b = b[2:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// Exe is magpie's own executable as a run in the distro starts it (a
// Windows program, which WSL runs on Windows).
func (t Tool) Exe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return t.Linux(filepath.Clean(exe)), nil
}
