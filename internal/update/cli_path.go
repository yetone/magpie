package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// The `magpie` command in a terminal (PAMI on Discord: a way in the app to
// put it there, and to see whether it is). install.sh links it, Homebrew
// links it, but the app downloaded from the site — and every Windows one —
// leaves a terminal without it. Settings' Command line says, for each shell
// a terminal may open, which magpie its `magpie` runs, and puts this app's
// there when the user asks: a link in a folder already on PATH (the one
// whose magpie runs first, when that one is another copy, or ~/.local/bin
// and the like), on Windows the app's folder put first in the user's PATH.
// A shell profile is written only for the shell the user picks, and only
// with one line that puts ~/.local/bin on its PATH.
//
// Another magpie's version is read from its .app's Info.plist or its
// Homebrew Cellar folder, never by running it: `magpie version` migrates
// the settings first (see StaleCLI).

// CLIShell is one shell's `magpie`.
type CLIShell struct {
	Name    string `json:"name"`              // zsh, bash, fish, PowerShell, cmd
	Default bool   `json:"default,omitempty"` // the one a terminal opens
	Known   bool   `json:"known"`             // its PATH could be read
	Command string `json:"command,omitempty"` // the magpie it runs, "" none
	Ours    bool   `json:"ours"`              // and that is this app
	Profile string `json:"profile,omitempty"` // the file a pick of it writes, ~ for the home; "" none
	HasDir  bool   `json:"hasDir,omitempty"`  // its PATH has CLI.Dir, so Add reaches it without its profile
}

// CLI is what `magpie` runs in a terminal opened now.
type CLI struct {
	Exe     string     `json:"exe"`               // this app's program, which it should run
	Command string     `json:"command,omitempty"` // the default shell's
	Ours    bool       `json:"ours"`
	Version string     `json:"version,omitempty"` // the other one's, when it can be read without running it
	Dir     string     `json:"dir,omitempty"`     // where Add puts it; "" when no folder on PATH can take it
	Stuck   string     `json:"stuck,omitempty"`   // translocated, read-only: why it can't be linked from where it is
	Shells  []CLIShell `json:"shells"`
	Windows bool       `json:"windows,omitempty"`
	Shim    string     `json:"shim,omitempty"` // Windows: the magpie.cmd Add writes beside an app not named magpie.exe, which runs it, in a build with no magpie.com (com.go)
}

// cliShell is a shell installed here.
type cliShell struct {
	Name, Path string
	Default    bool
}

// What ReadCLI asks the system; tests set them.
var (
	cliShellPath = proc.ShellPath
	cliShells    = installedShells
	cliExe       = cliProgram
)

var cliMu sync.Mutex

// ReadCLI says what `magpie` runs in each shell. It asks each shell for its
// PATH, a few seconds at most.
func ReadCLI() *CLI {
	v := &CLI{Windows: runtime.GOOS == "windows", Shells: []CLIShell{}}
	exe, err := cliExe()
	if err != nil {
		return v
	}
	v.Exe, v.Stuck = exe, cliStuck()
	if v.Windows && comLauncher == nil && needsShim(exe) {
		v.Shim = filepath.Join(filepath.Dir(exe), shimName)
	}
	shells := cliShells()
	paths := make([][]string, len(shells))
	var wg sync.WaitGroup
	for i, sh := range shells {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i] = cliShellPath(sh.Path)
		}()
	}
	wg.Wait()
	var def []string
	for i, sh := range shells {
		s := CLIShell{Name: sh.Name, Default: sh.Default, Known: paths[i] != nil, Command: findCLI(paths[i])}
		s.Ours = s.Command != "" && (sameFile(s.Command, exe) || shimRuns(s.Command, exe) || comRuns(s.Command, exe))
		if p := cliProfile(sh.Name); p != "" {
			s.Profile = tildeHome(p)
		}
		if sh.Default {
			v.Command, v.Ours, def = s.Command, s.Ours, paths[i]
		}
		v.Shells = append(v.Shells, s)
	}
	if v.Command != "" && !v.Ours {
		v.Version = otherVersion(v.Command)
	}
	if !v.Ours {
		v.Dir = cliDir(exe, def, v.Command)
		for i := range v.Shells {
			v.Shells[i].HasDir = v.Dir != "" && slices.Contains(paths[i], v.Dir)
		}
	}
	return v
}

// AddCLI puts this app's magpie on the PATH: of every shell whose PATH has
// the folder ReadCLI names (shell ""), or of the one shell named, by
// linking it in ~/.local/bin and adding that folder to the shell's profile.
// The launchers magpie keeps (Launchers: claude-magpie) go beside it, so
// they are there with nothing more asked.
func AddCLI(shell string) (*CLI, error) {
	v, err := addCLI(shell)
	if err == nil && v != nil && v.Ours && v.Command != "" {
		for name, args := range Launchers {
			_ = addLauncherBeside(v, name, args...) // one of the user's own by that name stays
		}
	}
	return v, err
}

func addCLI(shell string) (*CLI, error) {
	cliMu.Lock()
	defer cliMu.Unlock()
	exe, err := cliExe()
	if err != nil {
		return nil, err
	}
	switch cliStuck() {
	case "translocated":
		return nil, errors.New("macOS runs magpie from a temporary copy until it is moved to Applications: move it there, open it again, then add the command")
	case "read-only":
		return nil, errors.New("magpie is running from its disk image: copy it to Applications, open it from there, then add the command")
	}
	if shell == "" {
		v := ReadCLI()
		if v.Ours {
			return v, nil
		}
		if v.Dir == "" {
			return nil, errors.New("no folder on your PATH can take the magpie command: add ~/.local/bin to a shell's profile below")
		}
		if err := cliPlace(exe, v.Dir); err != nil {
			return nil, err
		}
		v = ReadCLI()
		if !v.Ours && v.Command != "" {
			return v, fmt.Errorf("magpie is in %s now, but a terminal still runs %s first", v.Dir, v.Command)
		}
		return v, nil
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("on Windows the command is added to the user's PATH, for every shell at once")
	}
	var sh *cliShell
	for _, s := range cliShells() {
		if s.Name == shell {
			sh = &s
			break
		}
	}
	prof := cliProfile(shell)
	if sh == nil || prof == "" {
		return nil, fmt.Errorf("%s isn't a shell magpie can add the command to", shell)
	}
	dir := filepath.Join(os.Getenv("HOME"), ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := cliPlace(exe, dir); err != nil {
		return nil, err
	}
	if err := addToProfile(prof, profileLine(shell)); err != nil {
		return nil, err
	}
	return ReadCLI(), nil
}

// Program is this magpie by a path that stays put: what a link, or an
// agent's config that starts magpie, should name (cliProgram).
func Program() (string, error) { return cliProgram() }

// cliProgram is the program a link should point at: the running one, or
// what stays put when it is replaced — Homebrew's opt link for a Cellar
// build, the AppImage for one run from its mount.
func cliProgram() (string, error) {
	if a := os.Getenv("APPIMAGE"); a != "" && runtime.GOOS == "linux" {
		return a, nil
	}
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	if i := strings.Index(filepath.ToSlash(exe), "/Cellar/magpie/"); i > 0 {
		opt := filepath.Join(exe[:i], "opt", "magpie", "bin", "magpie")
		if sameFile(opt, exe) {
			return opt, nil
		}
	}
	return exe, nil
}

// cliStuck is why the app can't be linked to from where it runs (Stuck).
var cliStuck = func() string {
	if b := Bundle(); b != "" {
		return Stuck(b)
	}
	return ""
}

func cliNames() []string {
	if runtime.GOOS == "windows" {
		// PATHEXT's order: magpie.com, the console program magpie writes
		// beside the app (com.go), runs before magpie.exe
		return []string{"magpie.com", "magpie.exe", "magpie.cmd", "magpie.bat"}
	}
	return []string{"magpie"}
}

// findCLI is the magpie a shell with PATH dirs runs, "" none.
func findCLI(dirs []string) string {
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		for _, n := range cliNames() {
			p := filepath.Join(d, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0) {
				return p
			}
		}
	}
	return ""
}

func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

var (
	plistVersion = lazyre.New(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
	cellarVer    = lazyre.New(`/Cellar/magpie/([^/]+)/`)
)

// otherVersion is the version of the magpie at cmd, read from its .app's
// Info.plist or its Cellar folder; "" when neither says.
func otherVersion(cmd string) string {
	p, err := filepath.EvalSymlinks(cmd)
	if err != nil {
		return ""
	}
	if m := cellarVer.FindStringSubmatch(filepath.ToSlash(p)); m != nil {
		return m[1]
	}
	if i := strings.Index(p, ".app/Contents/MacOS/"); i > 0 {
		b, err := os.ReadFile(filepath.Join(p[:i+len(".app")], "Contents", "Info.plist"))
		if m := plistVersion.FindSubmatch(b); err == nil && m != nil {
			return strings.TrimSpace(string(m[1]))
		}
	}
	return ""
}

// profileLine is what a shell's profile is given: ~/.local/bin first on its
// PATH.
func profileLine(shell string) string {
	if shell == "fish" {
		return "set -gx PATH $HOME/.local/bin $PATH"
	}
	return `export PATH="$HOME/.local/bin:$PATH"`
}

const profileMark = "# added by magpie (Settings › Command line)"

// addToProfile adds line to the profile at path, unless it has it.
func addToProfile(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	add := "\n" + profileMark + "\n" + line + "\n"
	if len(b) > 0 && b[len(b)-1] != '\n' {
		add = "\n" + add
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// cliProfile is the file a shell reads as a terminal opens it, which a pick
// of it writes; "" for a shell magpie doesn't write one for (Windows').
func cliProfile(shell string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	switch shell {
	case "zsh":
		if z := os.Getenv("ZDOTDIR"); filepath.IsAbs(z) {
			return filepath.Join(z, ".zshrc")
		}
		return filepath.Join(home, ".zshrc")
	case "bash":
		// Terminal on the Mac opens a login bash; a Linux terminal an
		// interactive one
		if runtime.GOOS == "darwin" {
			return filepath.Join(home, ".bash_profile")
		}
		return filepath.Join(home, ".bashrc")
	case "fish":
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(cfg) {
			cfg = filepath.Join(home, ".config")
		}
		return filepath.Join(cfg, "fish", "config.fish")
	}
	return ""
}

func tildeHome(p string) string {
	if h := os.Getenv("HOME"); h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

// A Windows app not named magpie.exe — the site's download is
// magpie-windows-amd64.exe (#942) — is given a magpie.cmd beside it that
// runs it, since a terminal looks for `magpie` by that name only. It names
// the program by its folder (%~dp0) and file name, which an update keeps:
// it replaces the program where it is, under its name.

// shimName is the file a shim is written to.
const shimName = "magpie.cmd"

// shimText is the magpie.cmd that runs the program exe in its folder.
func shimText(exe string) string {
	name := filepath.Base(strings.ReplaceAll(exe, `\`, "/"))
	return "@echo off\r\nrem added by magpie (Settings > Command line): the magpie command runs " + name + " in this folder\r\n\"%~dp0" + name + "\" %*\r\n"
}

// needsShim says whether the program exe can't be run as `magpie` by its
// own name.
func needsShim(exe string) bool {
	return !strings.EqualFold(filepath.Base(strings.ReplaceAll(exe, `\`, "/")), "magpie.exe")
}

// writeShim writes the magpie.cmd for exe beside it, when its name needs
// one; it is the file's path, "" when none was needed.
func writeShim(exe string) (string, error) {
	if !needsShim(exe) {
		return "", nil
	}
	p := filepath.Join(filepath.Dir(exe), shimName)
	if b, err := os.ReadFile(p); err == nil && string(b) == shimText(exe) {
		return p, nil
	}
	if err := os.WriteFile(p, []byte(shimText(exe)), 0o755); err != nil {
		return "", fmt.Errorf("writing %s, which runs %s as magpie: %w", p, filepath.Base(exe), err)
	}
	return p, nil
}

// shimRuns says whether cmd is the magpie.cmd magpie wrote for exe.
func shimRuns(cmd, exe string) bool {
	if !strings.EqualFold(filepath.Base(cmd), shimName) || !strings.EqualFold(filepath.Dir(cmd), filepath.Dir(exe)) {
		return false
	}
	b, err := os.ReadFile(cmd)
	return err == nil && string(b) == shimText(exe)
}

// pathFirst is the PATH value v (Windows', ;-separated) with dir first and
// nowhere else.
func pathFirst(v, dir string) string {
	same := func(d string) bool {
		return strings.EqualFold(strings.TrimRight(strings.TrimSpace(d), `\`), strings.TrimRight(dir, `\`))
	}
	out := []string{dir}
	for _, d := range strings.Split(v, ";") {
		if strings.TrimSpace(d) != "" && !same(d) {
			out = append(out, d)
		}
	}
	return strings.Join(out, ";")
}
