package agent

// The version and the update of an agent's CLI inside a WSL distro
// (cliupdate.go's, asked in the distro): where its binary is comes from the
// distro's own probe, how it was installed from that Linux path, and the
// update runs there, through wsl.exe, as the distro's user. A distro that
// isn't running is never started to be asked: its agents show nothing.

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// wslProbes keeps what a distro said of a CLI (wslCLIScript's output); a
// rescan asks again.
var wslProbes memo

// wslCLIScript says, for the first of the paths $@ that is a program: where
// it is, where its links lead, what it said to --version and its exit code.
const wslCLIScript = `for c in "$@"; do case "$c" in /*) ;; *) c=$(command -v "$c" 2>/dev/null);; esac
  if [ -n "$c" ] && [ -x "$c" ] && [ ! -d "$c" ]; then b="$c"; break; fi; done
[ -n "$b" ] || exit 0
echo "bin:$b"
r=$(readlink -f "$b" 2>/dev/null || echo "$b")
echo "real:$r"
# wsl.exe -e has no login PATH: a node CLI finds its node beside it, in its
# npm prefix, or where a user's tools are
PATH="$(dirname "$b"):${r%%/lib/node_modules/*}/bin:$HOME/.local/bin:$HOME/.bun/bin:$PATH"
o=$("$b" --version 2>&1 </dev/null); c=$?
echo "code:$c"
echo "ver:$(printf %s "$o" | head -n 3 | tr '\n' ' ')"`

// wslCLI is what a distro said of a CLI.
type wslCLI struct {
	bin, real, out string
	code           int
}

func (w wslCLI) version() string { return parseVersion(w.out) }

// wslCLIOf is the agent's distro, when it is running, and the kind of
// program it is (codex for codex@wsl:Ubuntu, omp for omp#work@wsl:Ubuntu).
func (a *Agent) wslCLIOf() (d distro, kind string, ok bool) {
	kind, _, _ = strings.Cut(a.ID, "@")
	kind, _, _ = strings.Cut(kind, "#")
	if _, has := cliSpecs[kind]; !has || a.WSL == "" {
		return d, "", false
	}
	for _, x := range wslDistros() {
		if x.Name == a.WSL {
			return x, kind, x.live()
		}
	}
	return d, "", false
}

// wslCLIPaths are where the distro's CLI of kind may be: where the probe
// found its command, else the home folder's place of a vendor's installer.
func wslCLIPaths(d distro, kind string, spec cliSpec) []string {
	var out []string
	for _, k := range wslKinds {
		if k.id == kind && k.bin != "" {
			if p := d.Paths[k.bin]; p != "" {
				out = append(out, p)
			}
			out = append(out, k.bin) // asked the distro's PATH, before a probe has said where
		}
	}
	if spec.at != "" && d.Home != "" {
		out = append(out, path.Join(d.Home, spec.at))
	}
	return out
}

// askWSLCLI is the distro's CLI of kind, asked again after a few minutes
// or an update; ok is false when it has none (or couldn't be asked).
func askWSLCLI(d distro, kind string, spec cliSpec) (wslCLI, bool) {
	paths := wslCLIPaths(d, kind, spec)
	if len(paths) == 0 {
		return wslCLI{}, false
	}
	raw := wslProbes.get(d.Name+"\x00"+kind, "", 10*time.Minute, time.Minute, func() (string, error) {
		args := append([]string{"-d", d.Name, "-e", "sh", "-c", wslCLIScript, "sh"}, paths...)
		out, err := wslRun(30*time.Second, args...)
		if err != nil || !strings.Contains(string(out), "bin:") {
			return "", errors.New("not found")
		}
		return string(out), nil
	})
	var w wslCLI
	for _, l := range strings.Split(raw, "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(l, "\r"), ":")
		switch k {
		case "bin":
			w.bin = v
		case "real":
			w.real = v
		case "code":
			fmt.Sscan(v, &w.code)
		case "ver":
			w.out = v
		}
	}
	return w, w.bin != ""
}

// wslTest is whether `test flag p` holds in the distro.
var wslTest = func(d distro, flag, p string) bool {
	_, err := wslRun(15*time.Second, "-d", d.Name, "-e", "test", flag, p)
	return err == nil
}

// wslHowInstalled says how the CLI at bin (real: where its links lead) was
// installed in the distro, from where it is; nil when that says nothing
// sure. cliupdate.go's howInstalled, on a Linux path asked of the distro.
func wslHowInstalled(d distro, spec cliSpec, bin, real string) *updater {
	if spec.self != nil {
		if c := spec.self(bin, real); c != nil {
			pkg := ""
			if len(spec.npm) > 0 {
				pkg = spec.npm[0]
			}
			u := &updater{via: "self", pkg: pkg, cmd: c, latest: spec.latest, wsl: d.Name}
			for _, p := range spec.npm {
				if n := wslFromNodeModules(d, p, false, bin, real); n != nil && n.via == "npm" {
					u.path = n.path
				}
			}
			return u
		}
	}
	if m := brewPath.FindStringSubmatch(real); m != nil && contains(spec.brew, m[2]) {
		prefix := real[:strings.Index(real, m[0])]
		brew := prefix + "/bin/brew"
		if !wslTest(d, "-x", brew) {
			brew = "brew"
		}
		u := &updater{via: "brew", pkg: m[2], cask: m[1] == "Caskroom", brew: brew, wsl: d.Name}
		u.cmd = []string{brew, "upgrade", m[2]}
		if u.cask {
			u.cmd = []string{brew, "upgrade", "--cask", m[2]}
		}
		return u
	}
	for _, pkg := range spec.npm {
		if u := wslFromNodeModules(d, pkg, spec.scripts, bin, real); u != nil {
			return u
		}
	}
	return nil
}

// wslFromNodeModules is fromNodeModules for a Linux path.
func wslFromNodeModules(d distro, pkg string, scripts bool, bin, real string) *updater {
	i := strings.Index(real, "/node_modules/"+pkg+"/")
	if i < 0 {
		return nil
	}
	modules := real[:i] + "/node_modules"
	lower := strings.ToLower(real + "|" + bin)
	switch {
	case strings.Contains(lower, "/.bun/"):
		bun := path.Join(path.Dir(bin), "bun")
		if !wslTest(d, "-x", bun) {
			bun = "bun"
		}
		return &updater{via: "bun", pkg: pkg, wsl: d.Name, path: path.Dir(bun), cmd: withScripts([]string{bun, "add", "-g"}, "bun", pkg, scripts)}
	case strings.Contains(lower, "/pnpm/") || strings.Contains(lower, "/.pnpm/"):
		return &updater{via: "pnpm", pkg: pkg, wsl: d.Name, cmd: withScripts([]string{"pnpm", "add", "-g"}, "pnpm", pkg, scripts)}
	case strings.Contains(lower, "/.volta/") || strings.Contains(lower, "/yarn/") || strings.Contains(lower, "/.yarn/"):
		return nil // a manager of its own, which magpie leaves to it
	}
	lib := path.Dir(modules)
	if path.Base(lib) != "lib" {
		return nil // not a global install
	}
	prefix := path.Dir(lib)
	if !wslTest(d, "-w", modules) {
		return nil // the system's (/usr/lib): npm would want sudo
	}
	npm := prefix + "/bin/npm"
	u := &updater{via: "npm", pkg: pkg, prefix: prefix, wsl: d.Name}
	if wslTest(d, "-x", npm) {
		u.path = prefix + "/bin" // its own node, not another on PATH
	} else {
		npm = "npm"
	}
	u.cmd = withScripts([]string{npm, "install", "-g", "--prefix", prefix}, "npm", pkg, scripts)
	return u
}

// wslBrewLatest is brewLatest's question asked in the distro.
func wslBrewLatest(u *updater) (string, error) {
	args := []string{"info", "--json=v2", u.pkg}
	if u.cask {
		args = []string{"info", "--json=v2", "--cask", u.pkg}
	}
	out, err := wslRun(30*time.Second, append([]string{"-d", u.wsl, "-e", "env", "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", u.brew}, args...)...)
	if err != nil {
		return "", err
	}
	return brewVersion(out)
}

// wslRunUpdate runs an update in the distro, as its user, with u's folder
// first on PATH, and says what it printed.
var wslRunUpdate = func(timeout time.Duration, u *updater) ([]byte, error) {
	args := []string{"-d", u.wsl, "-e", "sh", "-c", `d="$1"; shift; PATH="$d:$HOME/.local/bin:$HOME/.bun/bin:$PATH"; export PATH; exec env NONINTERACTIVE=1 HOMEBREW_NO_ENV_HINTS=1 NO_COLOR=1 CLINE_NO_AUTO_UPDATE=1 "$@" 2>&1`, "sh", u.path}
	if u.path == "" {
		args[len(args)-1] = "/usr/bin" // wsl.exe drops an empty argument
	}
	return wslRun(timeout, append(args, u.cmd...)...)
}

// wslCLI is the CLI of an agent in a running distro: what CliInfo says of
// a host one.
func (a *Agent) wslCLIInfo() (c CLI, ok bool) {
	d, kind, ok := a.wslCLIOf()
	if !ok {
		return c, false
	}
	spec := cliSpecs[kind]
	w, ok := askWSLCLI(d, kind, spec)
	if !ok {
		return c, false
	}
	c.Version, c.Path = w.version(), w.bin
	u := wslHowInstalled(d, spec, w.bin, w.real)
	if u == nil {
		return c, true
	}
	c.Via, c.Command = u.via, u.shown()
	c.Latest = latestVersion(u)
	c.Update = c.Version != "" && c.Latest != "" && Newer(c.Latest, c.Version)
	return c, true
}

// updateWSL is UpdateCLI for an agent in a distro.
func (a *Agent) updateWSL() (CLI, error) {
	d, kind, ok := a.wslCLIOf()
	if !ok {
		return CLI{}, fmt.Errorf("%s's distro %s isn't running: start it, then update", a.Name, a.WSL)
	}
	spec := cliSpecs[kind]
	w, ok := askWSLCLI(d, kind, spec)
	if !ok {
		return CLI{}, fmt.Errorf("magpie can't find %s's CLI in %s", a.Name, a.WSL)
	}
	u := wslHowInstalled(d, spec, w.bin, w.real)
	if u == nil {
		return CLI{}, fmt.Errorf("magpie can't tell how %s was installed at %s in %s — update it the way you installed it", a.Name, w.bin, a.WSL)
	}
	if _, busy := updating.LoadOrStore(a.ID, true); busy {
		return CLI{}, fmt.Errorf("%s is already being updated", a.Name)
	}
	defer updating.Delete(a.ID)
	before := w.version()
	if spec.target && u.via == "self" {
		if v := latestVersion(u); Newer(v, before) {
			u.cmd = append(u.cmd, v)
		}
	}
	out, err := wslRunUpdate(updateTimeout, u)
	wslProbes.forget(d.Name + "\x00" + kind)
	if u.via == "brew" {
		latests.forget("brew:" + u.pkg)
	}
	c, _ := a.wslCLIInfo()
	if err != nil {
		msg := lastLines(string(out), 3)
		if msg == "" {
			msg = err.Error()
		}
		return c, fmt.Errorf("%s: %s", u.shown(), msg)
	}
	if c.Update && c.Version == before {
		return c, fmt.Errorf("%s is still %s after %s; %s is out", a.Name, c.Version, u.shown(), c.Latest)
	}
	// the package says its new version, but that isn't the CLI running
	// (cliupdate.go's runsBroken)
	if n, _ := askWSLCLI(d, kind, spec); n.code > 0 && n.code != 127 && n.version() == "" {
		return c, fmt.Errorf("%s was updated, but %s --version fails: %s — %s", a.Name, path.Base(w.bin), lastLines(n.out, 2), mendHint(u, c.Version))
	}
	return c, nil
}
