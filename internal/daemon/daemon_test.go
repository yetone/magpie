package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, with a fake systemctl, launchctl
// and loginctl first on PATH: nothing is ever installed in the system.
func TestMain(m *testing.M) { testenv.Main(m) }

// fakes puts a systemctl, launchctl and loginctl on PATH, alone, that log
// each call to the file it gives and answer from the files the test
// writes: <name>.out is printed, and <name>.fail, when there, makes the
// call fail with its text. A file named after the first argument, such as
// launchctl.print.fail, is for that call alone.
func fakes(t *testing.T, os_ string) (log, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	prev, prevSelf, prevAuto, prevWiden := goos, self, autostartEnabled, widenPath
	t.Cleanup(func() { goos, self, autostartEnabled, widenPath = prev, prevSelf, prevAuto, prevWiden })
	// the login shell of whoever runs the tests is no test's PATH
	widenPath = func() {}
	goos = os_
	self = func() (string, error) { return "/opt/my magpie/magpie%1", nil }
	autostartEnabled = func() bool { return false }
	dir = t.TempDir()
	// a home of the test's own: nothing one test installs is the next's
	testenv.SetHome(t, filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "home", ".config"))
	bin := filepath.Join(dir, "bin")
	log = filepath.Join(dir, "calls")
	for _, name := range []string{"systemctl", "launchctl", "loginctl"} {
		testenv.Program(t, filepath.Join(bin, name), `#!/bin/sh
d='`+dir+`'
echo "`+name+` $*" >> "$d/calls"
for f in "$d/`+name+`.$1" "$d/`+name+`.$2"; do
	if [ -f "$f.fail" ]; then /bin/cat "$f.fail"; exit 1; fi
	if [ -f "$f.out" ]; then /bin/cat "$f.out"; exit 0; fi
done
if [ -f "$d/`+name+`.fail" ]; then /bin/cat "$d/`+name+`.fail"; exit 1; fi
[ -f "$d/`+name+`.out" ] && /bin/cat "$d/`+name+`.out"
exit 0
`)
	}
	t.Setenv("PATH", bin)
	return log, dir
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// On Linux, install writes a user unit that runs `magpie serve` with the
// shell's PATH, proxy and agents' folders, readable by the user alone,
// then enables and starts it through systemctl --user; uninstall stops,
// disables and removes it (HuanCheng on Discord).
func TestSystemdInstallAndUninstall(t *testing.T) {
	log, _ := fakes(t, "linux")
	t.Setenv("HTTPS_PROXY", "http://me:p%ss@proxy:8080")
	t.Setenv("CODEX_HOME", "/srv/codex")
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", "magpie.service")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	for _, want := range []string{
		"ExecStart=\"/opt/my magpie/magpie%%1\" serve\n",
		"Restart=on-failure\n",
		"Environment=\"PATH=" + os.Getenv("PATH") + "\"\n",
		"Environment=\"HTTPS_PROXY=http://me:p%%ss@proxy:8080\"\n",
		"Environment=\"CODEX_HOME=/srv/codex\"\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit has no %q:\n%s", want, unit)
		}
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("the unit, which holds the proxy's password, is %v", st.Mode().Perm())
	}
	// one left readable by others, written over, is the user's alone too
	os.Chmod(file, 0o644)
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("the unit written again is %v", st.Mode().Perm())
	}
	os.Remove(log)
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(calls(t, log), "\n")
	want := "systemctl --user is-system-running\nsystemctl --user daemon-reload\nsystemctl --user enable magpie.service\nsystemctl --user restart magpie.service"
	if got != want {
		t.Errorf("install ran\n%s\nwant\n%s", got, want)
	}

	os.Remove(log)
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the unit is still there: %v", err)
	}
	got = strings.Join(calls(t, log), "\n")
	want = "systemctl --user is-system-running\nsystemctl --user disable --now magpie.service\nsystemctl --user daemon-reload"
	if got != want {
		t.Errorf("uninstall ran\n%s\nwant\n%s", got, want)
	}
	// none left: nothing to do, and no failure
	os.Remove(log)
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if c := calls(t, log); c[0] != "" {
		t.Errorf("uninstall with none installed ran %v", c)
	}
}

// Start, stop and restart before an install say to install, and run
// nothing.
func TestNotInstalled(t *testing.T) {
	for _, os_ := range []string{"linux", "darwin"} {
		t.Run(os_, func(t *testing.T) {
			log, _ := fakes(t, os_)
			for _, f := range []func() error{Start, Stop, Restart} {
				if err := f(); !errors.Is(err, ErrNotInstalled) {
					t.Errorf("got %v, want ErrNotInstalled", err)
				}
			}
			if c := calls(t, log); c[0] != "" {
				t.Errorf("ran %v", c)
			}
			st, err := Get()
			if err != nil || st.Installed {
				t.Errorf("status %+v, %v", st, err)
			}
		})
	}
}

// Status reads systemctl show; a user who doesn't linger is told the
// service stops at logout and how to keep it from boot.
func TestSystemdStatus(t *testing.T) {
	_, dir := fakes(t, "linux")
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "systemctl.show.out"), "ActiveState=active\nSubState=running\nMainPID=4242\nUnitFileState=enabled\nExecMainStatus=0\n")
	write(t, filepath.Join(dir, "loginctl.out"), "no\n")
	st, err := Get()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Enabled || !st.Running || st.PID != 4242 || st.State != "active (running)" || st.LastExit != "" {
		t.Errorf("status %+v", st)
	}
	if len(st.Advice) != 1 || !strings.Contains(st.Advice[0], "sudo loginctl enable-linger ") {
		t.Errorf("advice %q, want enable-linger", st.Advice)
	}
	if !strings.HasPrefix(st.Logs, "journalctl --user -u magpie") {
		t.Errorf("logs %q", st.Logs)
	}
	// stopped by hand: SIGTERM's 15 is no failure to show
	write(t, filepath.Join(dir, "systemctl.show.out"), "ActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=enabled\nExecMainStatus=15\n")
	if st, _ = Get(); st.Running || st.LastExit != "" || st.State != "inactive (dead)" {
		t.Errorf("a stopped service's status %+v", st)
	}
	write(t, filepath.Join(dir, "systemctl.show.out"), "ActiveState=activating\nSubState=auto-restart\nMainPID=0\nUnitFileState=enabled\nExecMainStatus=1\n")
	write(t, filepath.Join(dir, "loginctl.out"), "yes\n")
	st, _ = Get()
	if st.Running || st.PID != 0 || st.LastExit != "1" || len(st.Advice) != 0 {
		t.Errorf("a failing service's status %+v", st)
	}
}

// A shell without the user's bus (su, sudo) or with no systemd says
// what to do instead, and writes nothing.
func TestSystemdUnreachable(t *testing.T) {
	_, dir := fakes(t, "linux")
	write(t, filepath.Join(dir, "systemctl.fail"), "Failed to connect to bus: No medium found")
	err := Install(Options{})
	if err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Errorf("got %v, want the bus advice", err)
	}
	if _, err := os.Stat(systemd{}.file()); !os.IsNotExist(err) {
		t.Errorf("a unit was written: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := Install(Options{}); err == nil || !strings.Contains(err.Error(), "no systemd") {
		t.Errorf("got %v, want no systemd", err)
	}
}

// The app opened at login serves the gateway too: an install beside it is
// refused before anything is written.
func TestRefusedBesideAutostart(t *testing.T) {
	for _, os_ := range []string{"linux", "darwin"} {
		t.Run(os_, func(t *testing.T) {
			log, _ := fakes(t, os_)
			autostartEnabled = func() bool { return true }
			if err := Install(Options{}); err == nil || !strings.Contains(err.Error(), "magpie autostart off") {
				t.Errorf("got %v", err)
			}
			if c := calls(t, log); c[0] != "" {
				t.Errorf("ran %v", c)
			}
		})
	}
}

// On the Mac, install writes a launch agent that runs `magpie serve` at
// login and again when it fails, and loads it; an install over one loaded
// takes the old one out first.
func TestLaunchdInstall(t *testing.T) {
	log, dir := fakes(t, "darwin")
	t.Setenv("HTTP_PROXY", "http://a&b<c>@proxy")
	// not loaded yet
	write(t, filepath.Join(dir, "launchctl.print.fail"), "Could not find service")
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.yetone.magpie.serve.plist")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	p := string(b)
	for _, want := range []string{
		"<key>Label</key><string>com.yetone.magpie.serve</string>",
		"<array><string>/opt/my magpie/magpie%1</string><string>serve</string></array>",
		"<key>SuccessfulExit</key><false/>",
		"<key>ProcessType</key><string>Standard</string>",
		"<key>HTTP_PROXY</key><string>http://a&amp;b&lt;c&gt;@proxy</string>",
		"<key>PATH</key><string>" + os.Getenv("PATH") + "</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the plist has no %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Background") {
		t.Errorf("the plist still says Background:\n%s", p)
	}
	if lint := "/usr/bin/plutil"; runtime.GOOS == "darwin" {
		if out, err := exec.Command(lint, "-lint", file).CombinedOutput(); err != nil {
			t.Errorf("plutil: %s", out)
		}
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("the plist is %v", st.Mode().Perm())
	}
	uid := strconv.Itoa(os.Getuid())
	got := strings.Join(calls(t, log), "\n")
	want := "launchctl print gui/" + uid + "/com.yetone.magpie.serve\nlaunchctl bootstrap gui/" + uid + " " + file
	if got != want {
		t.Errorf("install ran\n%s\nwant\n%s", got, want)
	}

	// loaded: install again takes it out first; restart takes it out and
	// loads it again (the fake still says loaded, so start kickstarts)
	os.Remove(filepath.Join(dir, "launchctl.print.fail"))
	write(t, filepath.Join(dir, "launchctl.print.out"), "gui/501/com.yetone.magpie.serve = {\n\tactive count = 1\n\tstate = running\n\tprogram = /opt/magpie\n\tpid = 777\n\tlast exit code = (never exited)\n\tendpoints = {\n\t\tstate = active\n\t}\n}\n")
	os.Remove(log)
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Restart(); err != nil {
		t.Fatal(err)
	}
	got = strings.Join(calls(t, log), "\n")
	svc := "gui/" + uid + "/com.yetone.magpie.serve"
	want = "launchctl print " + svc + "\nlaunchctl bootout " + svc + "\nlaunchctl bootstrap gui/" + uid + " " + file + "\nlaunchctl print " + svc + "\nlaunchctl bootout " + svc + "\nlaunchctl print " + svc + "\nlaunchctl kickstart " + svc
	if got != want {
		t.Errorf("reinstall and restart ran\n%s\nwant\n%s", got, want)
	}
	st, err := Get()
	if err != nil || !st.Running || st.PID != 777 || st.State != "running" || st.LastExit != "" {
		t.Errorf("status %+v, %v", st, err)
	}
	if !strings.Contains(st.Logs, filepath.Join("Library", "Logs", "magpie", "serve.log")) {
		t.Errorf("logs %q", st.Logs)
	}

	os.Remove(log)
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the plist is still there: %v", err)
	}
	if got := strings.Join(calls(t, log), "\n"); got != "launchctl print "+svc+"\nlaunchctl bootout "+svc {
		t.Errorf("uninstall ran\n%s", got)
	}
}

// The daemon's launch agent is Standard, not Background, on every platform:
// plist only builds text, so this runs without the shell fakes and without
// a Mac.
func TestLaunchdPlistProcessType(t *testing.T) {
	p := plist("/opt/magpie", []string{"serve"}, nil)
	if want := "<key>ProcessType</key><string>Standard</string>"; !strings.Contains(p, want) {
		t.Errorf("the plist has no %q:\n%s", want, p)
	}
	if bad := "<key>ProcessType</key><string>Background</string>"; strings.Contains(p, bad) {
		t.Errorf("the plist says %q:\n%s", bad, p)
	}
}

// Windows and Android have no service of this kind: the error points to
// what they have.
func TestUnsupported(t *testing.T) {
	prev := goos
	t.Cleanup(func() { goos = prev })
	for os_, want := range map[string]string{"windows": "magpie autostart on", "android": "Termux:Boot"} {
		goos = os_
		if err := Install(Options{}); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", os_, err)
		}
	}
}

// The service runs magpie by the name on PATH when that is this same file
// (a package manager's link survives an update), never a build `go run`
// deletes, nor a copy macOS runs from a random place.
func TestPick(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar", "magpie", "1.0", "magpie")
	os.MkdirAll(filepath.Dir(real), 0o755)
	write(t, real, "x")
	os.Chmod(real, 0o755)
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	if err := os.Symlink(real, filepath.Join(bin, "magpie")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got, err := pick(real, real); err != nil || got != filepath.Join(bin, "magpie") {
		t.Errorf("got %q, %v; want the link on PATH", got, err)
	}
	// another magpie on PATH: this one
	other := filepath.Join(dir, "other")
	write(t, other, "y")
	t.Setenv("PATH", t.TempDir())
	if got, _ := pick(other, other); got != other {
		t.Errorf("got %q", got)
	}
	for _, p := range []string{
		filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "magpie"),
		"/private/var/folders/x/T/AppTranslocation/ABC/d/magpie.app/Contents/MacOS/magpie",
	} {
		if _, err := pick(p, p); err == nil {
			t.Errorf("%s was taken", p)
		}
	}
}

// The environment carried is the set variables of the list, once each.
func TestEnviron(t *testing.T) {
	t.Setenv("PATH", "/a:/b")
	t.Setenv("no_proxy", "localhost")
	t.Setenv("MAGPIE_ADDR", "")
	got := strings.Join(environ(), "\n")
	if !strings.Contains(got, "PATH=/a:/b") || !strings.Contains(got, "no_proxy=localhost") || strings.Contains(got, "MAGPIE_ADDR") {
		t.Errorf("environ:\n%s", got)
	}
	if strings.Count(got, "PATH=/a:/b") != 1 {
		t.Errorf("PATH twice:\n%s", got)
	}
}

// The service's PATH has the Node a terminal gets though the shell
// install runs from hasn't: nvm loaded lazily, or not at all over ssh, put
// no node on PATH, and plugins started under systemd found none (bookyue
// on Discord). nvm's default is the one written, not its newest.
func TestInstallFindsNvmNode(t *testing.T) {
	log, dir := fakes(t, "linux")
	widenPath = realWiden
	home := filepath.Join(dir, "home")
	t.Setenv("NVM_DIR", "")
	t.Setenv("SHELL", filepath.Join(dir, "no-shell")) // no login shell to ask
	for _, v := range []string{"v20.11.0", "v22.1.0"} {
		testenv.Program(t, filepath.Join(home, ".nvm", "versions", "node", v, "bin", "node"), "#!/bin/sh\n")
	}
	if err := os.MkdirAll(filepath.Join(home, ".nvm", "alias"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".nvm", "alias", "default"), "20\n")
	if Node() != "" {
		t.Fatal("the test's PATH has a node already")
	}
	if err := Install(Options{}); err != nil {
		t.Fatal(err, calls(t, log))
	}
	b, err := os.ReadFile(systemd{}.file())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".nvm", "versions", "node", "v20.11.0", "bin")
	var path string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, `Environment="PATH=`) {
			path = line
		}
	}
	if !strings.Contains(path, want) || strings.Contains(path, "v22.1.0") {
		t.Errorf("the unit's PATH has no nvm default Node (%s):\n%s", want, path)
	}
	if !strings.HasPrefix(path, `Environment="PATH=`+filepath.Join(dir, "bin")+":") {
		t.Errorf("the shell's own PATH should come first:\n%s", path)
	}
	if Node() != filepath.Join(want, "node") {
		t.Errorf("Node() = %q, want nvm's default", Node())
	}
}

var realWiden = widenPath

// Installed with the page, the service runs `magpie serve --web=<addr>`
// with the page's key in its environment, on both systems, and Web reads
// both back from the service's own file: the key stays the same across
// restarts and is what `magpie daemon status` prints the link with
// (HuanCheng on Discord). Installed again without, both are gone, and a
// MAGPIE_WEB_KEY in the shell is never carried into a service with no page.
func TestInstallWithWeb(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		goos       string
		file       func() string
		arg, keyIn string
	}{
		{"linux", func() string {
			return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", "magpie.service")
		}, "ExecStart=\"/opt/my magpie/magpie%%1\" serve \"--web=0.0.0.0:3430\"\n", "Environment=\"MAGPIE_WEB_KEY=" + key + "\"\n"},
		{"darwin", func() string {
			return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.yetone.magpie.serve.plist")
		}, "<array><string>/opt/my magpie/magpie%1</string><string>serve</string><string>--web=0.0.0.0:3430</string></array>", "<key>MAGPIE_WEB_KEY</key><string>" + key + "</string>"},
	} {
		t.Run(c.goos, func(t *testing.T) {
			fakes(t, c.goos)
			t.Setenv("MAGPIE_WEB_KEY", "from-the-shell-0123456789")
			if err := Install(Options{Web: "0.0.0.0:3430", Key: key}); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(c.file())
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{c.arg, c.keyIn} {
				if !strings.Contains(string(b), want) {
					t.Errorf("the service has no %q:\n%s", want, b)
				}
			}
			if strings.Contains(string(b), "from-the-shell") {
				t.Errorf("the shell's key was carried beside the page's:\n%s", b)
			}
			if c.goos == "darwin" && runtime.GOOS == "darwin" {
				if out, err := exec.Command("/usr/bin/plutil", "-lint", c.file()).CombinedOutput(); err != nil {
					t.Errorf("plutil: %s", out)
				}
			}
			if a, k := Web(); a != "0.0.0.0:3430" || k != key {
				t.Errorf("Web: %q %q", a, k)
			}
			if err := Install(Options{}); err != nil {
				t.Fatal(err)
			}
			b, _ = os.ReadFile(c.file())
			if strings.Contains(string(b), "--web") || strings.Contains(string(b), "MAGPIE_WEB_KEY") {
				t.Errorf("installed without the page, the service still has it:\n%s", b)
			}
			if a, k := Web(); a != "" || k != "" {
				t.Errorf("Web without the page: %q %q", a, k)
			}
			if err := Install(Options{Web: "127.0.0.1:3430"}); err == nil {
				t.Error("a page with no key was installed")
			}
		})
	}
}

// Web is "" with no service installed.
func TestWebNotInstalled(t *testing.T) {
	fakes(t, "linux")
	if a, k := Web(); a != "" || k != "" {
		t.Errorf("%q %q", a, k)
	}
}

// install --tls-cert … --tls-key … writes the pair into the service, and
// TLS reads it back through the unit's and the plist's quoting, so the
// service installed again keeps it (paths with a space, % and & here).
func TestInstallCarriesTLS(t *testing.T) {
	const cert, key = `/etc/my certs/full%chain&"1".pem`, "/etc/my certs/priv%key.pem"
	for _, c := range []struct {
		goos string
		in   string
	}{
		{"linux", `Environment="MAGPIE_TLS_CERT=/etc/my certs/full%%chain&\"1\".pem"`},
		{"darwin", "<key>MAGPIE_TLS_CERT</key><string>/etc/my certs/full%chain&amp;&#34;1&#34;.pem</string>"},
	} {
		t.Run(c.goos, func(t *testing.T) {
			fakes(t, c.goos)
			if a, b := TLS(); a != "" || b != "" {
				t.Errorf("TLS with no service: %q %q", a, b)
			}
			t.Setenv("MAGPIE_TLS_CERT", cert)
			t.Setenv("MAGPIE_TLS_KEY", key)
			if err := Install(Options{}); err != nil {
				t.Fatal(err)
			}
			st, _ := Get()
			b, _ := os.ReadFile(st.File)
			if !strings.Contains(string(b), c.in) {
				t.Errorf("the service has no %q:\n%s", c.in, b)
			}
			if a, k := TLS(); a != cert || k != key {
				t.Errorf("TLS: %q %q", a, k)
			}
			t.Setenv("MAGPIE_TLS_KEY", "")
			if err := Install(Options{}); err != nil {
				t.Fatal(err)
			}
			if a, k := TLS(); a != "" || k != "" {
				t.Errorf("half a pair read back: %q %q", a, k)
			}
		})
	}
}

// A service the user removed is recorded, so the terminal's hand-over of
// the gateway (tui.keepGateway) never installs it again by itself; an
// install by the user clears that.
func TestUninstallRecordsTheUsersNo(t *testing.T) {
	fakes(t, "linux")
	if Removed() {
		t.Fatal("removed before anything")
	}
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if !Removed() {
		t.Fatal("the user's uninstall not recorded")
	}
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	if Removed() {
		t.Fatal("still recorded after the user installed it")
	}
}

// Program is what the installed service runs, read from its own file.
func TestProgramOf(t *testing.T) {
	exe := `/Users/a b/.local/bin/magpie`
	if got := programOf(plist(exe, []string{"serve"}, nil)); got != exe {
		t.Errorf("plist: %q", got)
	}
	if got := programOf(unitText(exe, []string{"serve"}, nil)); got != exe {
		t.Errorf("unit: %q", got)
	}
	if got := programOf("nothing"); got != "" {
		t.Errorf("none: %q", got)
	}
}

// A launch agent an older magpie wrote, running the gateway as Background,
// is mended to Standard in place: by the service as it starts (Mend), with
// nothing else in it changed and launchd left alone, and by a restart,
// which loads it anew for launchd to read it (#2961).
func TestLaunchdOldAgentMended(t *testing.T) {
	log, dir := fakes(t, "darwin")
	write(t, filepath.Join(dir, "launchctl.print.fail"), "Could not find service")
	if err := Install(Options{}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.yetone.magpie.serve.plist")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	now := string(b)
	old := strings.Replace(now, processType, oldProcessType, 1)
	if old == now {
		t.Fatalf("the plist has no %q:\n%s", processType, now)
	}
	// loaded, as the running service's is
	os.Remove(filepath.Join(dir, "launchctl.print.fail"))
	write(t, filepath.Join(dir, "launchctl.print.out"), "state = running\n")
	mended := func(what string) {
		t.Helper()
		b, _ := os.ReadFile(file)
		if string(b) != now {
			t.Errorf("%s: the plist is\n%s\nwant\n%s", what, b, now)
		}
		if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
			t.Errorf("%s: the plist is %v", what, st.Mode().Perm())
		}
	}

	write(t, file, old)
	os.Remove(log)
	Mend()
	mended("Mend")
	if got := strings.Join(calls(t, log), "\n"); got != "" {
		t.Errorf("Mend ran %s", got)
	}
	Mend() // a second time finds nothing to do
	mended("Mend again")

	write(t, file, old)
	os.Remove(log)
	if err := Restart(); err != nil {
		t.Fatal(err)
	}
	mended("Restart")
	uid := strconv.Itoa(os.Getuid())
	svc := "gui/" + uid + "/com.yetone.magpie.serve"
	// the fake still says loaded after the bootout, so start kickstarts
	// what bootstrap would load: what matters is the bootout first
	got := strings.Join(calls(t, log), "\n")
	if !strings.HasPrefix(got, "launchctl print "+svc+"\nlaunchctl bootout "+svc+"\n") {
		t.Errorf("restart of an old agent ran\n%s\nwant a bootout first", got)
	}

	// on Linux there is no launch agent to mend
	goos = "linux"
	write(t, file, old)
	Mend()
	if b, _ := os.ReadFile(file); string(b) != old {
		t.Errorf("Mend on Linux changed the plist")
	}
}
