// Package daemon runs `magpie serve` as a service of the user's own: a
// systemd user unit on Linux, a launch agent on the Mac. The system starts
// it at boot or login and starts it again when it stops, with no window
// and no desktop session, for a server or a machine reached over SSH.
// Whether it is installed and running is the system's own record, read
// each time, never a setting of magpie's.
package daemon

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

// Name is the service's name: magpie.service, com.yetone.magpie.serve.
const Name = "magpie"

// Status is what the system says of the service.
type Status struct {
	Installed bool   // the unit or launch agent is on disk
	Enabled   bool   // started at boot or login
	Running   bool   // its process is up
	PID       int    // that process, when the system says
	State     string // the system's own word for it: active, failed, running…
	LastExit  string // how its last run ended, when the system says
	File      string // the unit or launch agent's file
	Logs      string // how to read what it printed
	Advice    []string
}

// manager is one system's way of running a service.
type manager interface {
	file() string
	logs() string
	install(exe string, args, env []string) error
	uninstall() error
	start() error
	stop() error
	restart() error
	status() (Status, error)
}

// goos picks the manager; a var so that the tests run both on any unix.
var goos = runtime.GOOS

// ErrUnsupported is for a system with no service manager magpie writes for.
var ErrUnsupported = errors.New("unsupported")

func current() (manager, error) {
	switch goos {
	case "linux":
		return systemd{}, nil
	case "darwin":
		return launchd{}, nil
	case "windows":
		return nil, fmt.Errorf("%w on Windows: `magpie autostart on` opens magpie, with its gateway, when you log in; or run `magpie daemon install` inside WSL", ErrUnsupported)
	case "android":
		return nil, fmt.Errorf("%w on Android: `magpie autostart on` starts the gateway at boot through Termux:Boot", ErrUnsupported)
	}
	return nil, fmt.Errorf("%w on %s: run `magpie serve` under the system's own service manager", ErrUnsupported, goos)
}

// Options are what the service serves besides the gateway.
type Options struct {
	// Web is the address the app's page is served on (`magpie serve
	// --web=…`), "" for none.
	Web string
	// Key is the page's MAGPIE_WEB_KEY, written into the service so its
	// link stays the same across restarts; needed with Web.
	Key string
}

// Install writes the service for this copy of magpie, with the
// environment it was installed from, and starts it; again, it writes the
// service anew (a moved magpie, a new PATH or proxy) and restarts it.
func Install(o Options) error {
	m, err := current()
	if err != nil {
		return err
	}
	// the app opened at login serves the gateway too: the two would take
	// turns failing on the port
	if autostartEnabled() {
		return errors.New("magpie already opens at login and serves the gateway: run `magpie autostart off` first, or keep that instead")
	}
	if o.Web != "" && o.Key == "" {
		return errors.New("the page needs a key")
	}
	exe, err := self()
	if err != nil {
		return err
	}
	widenPath()
	if err := m.install(exe, serveArgs(o), withKey(environ(), o)); err != nil {
		return err
	}
	os.Remove(removedMark())
	return nil
}

// removedMark is there once the user removed the service (Uninstall): what
// magpie puts in place by itself (the terminal's keepGateway, as it quits) is put there
// no more.
func removedMark() string { return filepath.Join(appdir.Config(), "daemon-removed") }

// Removed says the user removed the service, and hasn't installed it since.
func Removed() bool {
	_, err := os.Stat(removedMark())
	return err == nil
}

// serveArgs is what the service runs magpie with.
func serveArgs(o Options) []string {
	if o.Web == "" {
		return []string{"serve"}
	}
	return []string{"serve", webFlag + o.Web}
}

// webFlag is `magpie serve`'s, naming the page's address.
const webFlag = "--web="

// webKeyVar is the page's key in the service's environment.
const webKeyVar = "MAGPIE_WEB_KEY"

// withKey is env with the page's key, when it has a page: a key in the
// shell's environment is never carried otherwise.
func withKey(env []string, o Options) []string {
	if o.Web == "" {
		return env
	}
	return append(env, webKeyVar+"="+o.Key)
}

// Web is the page the installed service serves: its address and key, ""
// when it serves none or isn't installed. Read from the service's own
// file, which is the user's alone.
func Web() (addr, key string) {
	m, err := current()
	if err != nil {
		return "", ""
	}
	b, err := os.ReadFile(m.file())
	if err != nil {
		return "", ""
	}
	return webOf(string(b))
}

var (
	webArgRe = regexp.MustCompile(`(?:"|<string>)` + regexp.QuoteMeta(webFlag) + `([^"<\s]+)(?:"|</string>)`)
	// a unit's Environment="MAGPIE_WEB_KEY=…", a plist's
	// <key>MAGPIE_WEB_KEY</key><string>…</string>
	webKeyRe = regexp.MustCompile(webKeyVar + `(?:=|</key><string>)([A-Za-z0-9._~-]+)`)
)

// TLS is the certificate and key files the installed service serves
// HTTPS with (`magpie daemon install --tls-cert … --tls-key …`), "" when
// it has none or isn't installed.
func TLS() (cert, key string) {
	m, err := current()
	if err != nil {
		return "", ""
	}
	b, err := os.ReadFile(m.file())
	if err != nil {
		return "", ""
	}
	return tlsOf(string(b))
}

// tlsOf is TLS from a unit's or a launch agent's text: both files or
// neither.
func tlsOf(text string) (cert, key string) {
	val := func(name string) string {
		// a unit's Environment="NAME=…", quoted by unitQuote
		if m := regexp.MustCompile(`Environment="` + name + `=((?:[^"\\]|\\.)*)"`).FindStringSubmatch(text); m != nil {
			return strings.NewReplacer(`\\`, `\`, `\"`, `"`, "%%", "%").Replace(m[1])
		}
		// a plist's <key>NAME</key><string>…</string>
		if m := regexp.MustCompile(`<key>` + name + `</key><string>([^<]*)</string>`).FindStringSubmatch(text); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	cert, key = val("MAGPIE_TLS_CERT"), val("MAGPIE_TLS_KEY")
	if cert == "" || key == "" {
		return "", ""
	}
	return cert, key
}

// webOf is Web from a unit's or a launch agent's text.
func webOf(text string) (addr, key string) {
	m := webArgRe.FindStringSubmatch(text)
	if m == nil {
		return "", ""
	}
	k := webKeyRe.FindStringSubmatch(text)
	if k == nil {
		return "", ""
	}
	return html.UnescapeString(m[1]), k[1]
}

// widenPath gives PATH, before it is written into the service, the
// folders a terminal of the user's has: nvm's default Node and the other
// managers' folders, then the login shell's own PATH — what the app does
// when it is opened from the Dock. The shell install is run from can lack
// them (nvm loaded lazily, a script, ssh without a login shell), and the
// service has only what is written: plugins and the MCP servers and agent
// CLIs they start need node, which wasn't found (bookyue on Discord). A
// var for the tests.
var widenPath = func() {
	proc.UserPath()
	cur := filepath.SplitList(os.Getenv("PATH"))
	for _, d := range proc.LoginPath() {
		if d != "" && filepath.IsAbs(d) && !slices.Contains(cur, d) {
			cur = append(cur, d)
		}
	}
	os.Setenv("PATH", strings.Join(cur, string(os.PathListSeparator)))
}

// Node is the node the service finds on its PATH, "" for none: what the
// CLI warns of after an install.
func Node() string {
	p, err := exec.LookPath("node")
	if err != nil {
		return ""
	}
	return p
}

// Uninstall stops the service and removes it. One not installed is left
// as it is.
func Uninstall() error {
	err := with(manager.uninstall)
	if err == nil {
		os.MkdirAll(filepath.Dir(removedMark()), 0o700)
		os.WriteFile(removedMark(), nil, 0o600)
	}
	return err
}

// Start starts the installed service.
func Start() error { return installed(manager.start) }

// Stop stops it, until the next boot or login, or Start.
func Stop() error { return installed(manager.stop) }

// Restart stops it and starts it again: a magpie updated in place serves
// as the new version.
func Restart() error { return installed(manager.restart) }

// Get is what the system says of the service.
func Get() (Status, error) {
	m, err := current()
	if err != nil {
		return Status{}, err
	}
	st := Status{File: m.file(), Logs: m.logs()}
	if _, err := os.Stat(st.File); err != nil {
		return st, nil
	}
	got, err := m.status()
	got.Installed, got.File, got.Logs = true, st.File, st.Logs
	return got, err
}

// ErrNotInstalled is Start, Stop or Restart before Install.
var ErrNotInstalled = errors.New("the magpie service isn't installed: `magpie daemon install`")

func with(f func(manager) error) error {
	m, err := current()
	if err != nil {
		return err
	}
	return f(m)
}

func installed(f func(manager) error) error {
	return with(func(m manager) error {
		if _, err := os.Stat(m.file()); err != nil {
			return ErrNotInstalled
		}
		return f(m)
	})
}

// autostartEnabled: magpie opens at login, by its own record or, on
// Linux, by a package's entry (#3372). On the Mac a record the user has
// switched off in System Settings for now counts all the same, since
// switching it back on there opens the app with the service still
// installed. On Linux an entry the desktop's own settings switched off
// (Hidden, X-GNOME-Autostart-enabled=false) reads off, as Open at login
// reads it, so the install goes in; and a package's entry counts on a box
// no desktop logs into too (#3756). A var for the tests.
var autostartEnabled = func() bool { return autostart.Enabled() != autostart.Off }

// self is the program the service runs: this one, by the name on PATH
// when that is this same file, so a package manager's link (Homebrew's
// /opt/homebrew/bin/magpie) still leads to the version it updates to. A
// var for the tests, whose binary is a build Go deletes.
var self = func() (string, error) {
	if p := os.Getenv("APPIMAGE"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real := exe
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		real = p
	}
	return pick(real, exe)
}

// pick is self past os.Executable: real is the running file, exe the
// name it was started by.
func pick(real, exe string) (string, error) {
	if strings.Contains(real, "/AppTranslocation/") {
		return "", errors.New("move magpie to Applications first, then install the service")
	}
	if strings.Contains(real, string(filepath.Separator)+"go-build") {
		return "", errors.New("this magpie is a build `go run` deletes when it ends: install magpie, then run `magpie daemon install` from it")
	}
	if p, err := exec.LookPath(Name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			if r, err := filepath.EvalSymlinks(abs); err == nil && sameFile(r, real) {
				return abs, nil
			}
		}
	}
	if filepath.IsAbs(exe) && sameFile(exe, real) {
		return exe, nil
	}
	return real, nil
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// carried are the variables the service gets from the shell it was
// installed from: the PATH that finds the agents' CLIs and bun, the proxy,
// magpie's own settings, and where each agent keeps its files. A service
// starts with the system's bare environment otherwise.
var carried = []string{
	"PATH",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"MAGPIE_ADDR", "MAGPIE_TRUST_PROXY", "MAGPIE_BUN", "MAGPIE_WEB_URL",
	// `magpie daemon install --tls-cert … --tls-key …` sets them
	"MAGPIE_TLS_CERT", "MAGPIE_TLS_KEY",
}

// environ is carried's variables that are set, as KEY=value, in order.
func environ() []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range append(append([]string{}, carried...), agentenv.Vars...) {
		if seen[k] {
			continue
		}
		seen[k] = true
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// run runs a service manager's tool, from PATH, and gives what it printed.
// A failure carries that text, which is what the user needs to read.
func run(name string, args ...string) (string, error) {
	out, err := proc.Command(name, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return text, err
		}
		if text != "" {
			return text, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), text)
		}
		return text, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return text, nil
}

// private writes a service's file, readable by the user alone from its
// first byte: it holds the environment, and a proxy's can carry a
// password.
func private(path string, data []byte) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
	}
	if err := edit.WriteAtomic(path, data); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
