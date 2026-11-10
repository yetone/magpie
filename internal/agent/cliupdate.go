package agent

// An agent's command-line program is kept up to date from the Agents page
// (#202): its version, the newest there is, and a click that updates it the
// way it was installed — with the CLI's own updater where it came from the
// vendor's installer (claude update, codex update, opencode upgrade, grok update), or by
// the package manager that owns it (npm, bun, pnpm, Homebrew). How it was
// installed is read from where its binary is; when that says nothing sure,
// the version is shown and nothing offered.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/source"
	"github.com/yetone/magpie/internal/update"
)

// CLI is what magpie knows of an agent's command-line program.
type CLI struct {
	Version string `json:"version,omitempty"`
	// Latest is the newest there is, from where it would be updated:
	// Homebrew's for one Homebrew installed, npm's otherwise
	Latest string `json:"latest,omitempty"`
	// Via is what updates it: npm, bun, pnpm, brew, or self (its own
	// updater); "" when magpie can't tell how it was installed
	Via string `json:"via,omitempty"`
	// Command is the update as a person would type it
	Command string `json:"command,omitempty"`
	// Update: Latest is after Version, and magpie knows how to get it
	Update bool `json:"update,omitempty"`
	// App is the version of the agent's desktop app installed beside it
	// (the Codex app, AppVersion); shown even with no CLI on PATH
	App string `json:"app,omitempty"`
	// Admin: the update needs an administrator, which Windows asks for
	// first (npm's global folder under Program Files)
	Admin bool `json:"admin,omitempty"`
	// Path is where the CLI was found: the file magpie runs, as PATH
	// reached it
	Path string `json:"path,omitempty"`
	// OffPath says the CLI was found in a folder magpie adds to its own
	// PATH (proc.UserPath: ~/.local/bin, npm's prefixes, nvm's default
	// Node, bun, Homebrew, ...) that a terminal opened now doesn't have,
	// so that `which` there doesn't find it. A row of an agent the user
	// believed uninstalled, with a version and nothing to say where it
	// came from, read as magpie making it up (悠悠哥 on Discord: Codex
	// 0.162.1 listed with no codex on PATH and no ~/.codex). False when
	// the terminal's PATH can't be told.
	OffPath bool `json:"offPath,omitempty"`
	// InDesktop: no Claude Code of its own is found here, and Claude
	// Desktop is, which includes one for its Code tab that reads the
	// same ~/.claude (cybercaius: the card stayed after Claude Code was
	// uninstalled and Desktop installed again, with nothing saying why).
	// DesktopVersion is that copy's newest version, "" until Desktop has
	// fetched it.
	InDesktop      bool   `json:"inDesktop,omitempty"`
	DesktopVersion string `json:"desktopVersion,omitempty"`
}

// cliSpec is where an agent's CLI is published.
type cliSpec struct {
	npm  []string // its packages on npm, the current one first
	brew []string // its Homebrew formulae and casks
	// self is the CLI's own updater, when the binary (real is where its
	// links lead, with forward slashes) is the one its vendor's installer
	// put there
	self func(bin, real string) []string
	// at is where its vendor's installer puts the CLI under the home folder
	// (.exe added on Windows), for an agent with no Bin: its command's name
	// is other tools' too, so PATH can't say it is this one
	at string
	// latest is the newest version its own updater would install, for one
	// not published on npm
	latest func() string
	// target: its own updater takes the version to install (opencode
	// upgrade 1.19.3), so magpie gives it the one it already knows, as the
	// row promised, when that updater is what runs (via "self"): npm's,
	// bun's, pnpm's and brew's commands take no version after them. An opencode 1.19.0 from its own installer (method curl)
	// asks GitHub's API for the newest release, which answers an address 60
	// times an hour without a token and 403s once that is spent; opencode's
	// current source asks opencode.ai instead, but only an update gets
	// there (#2837, ggbdpq:
	// "opencode upgrade: ... 403 GET https://api.github.com/repos/
	// anomalyco/opencode/releases/latest")
	target bool
	// only are the systems (GOOS) its vendor builds it for, when not all:
	// on any other its package installs nothing that runs
	only []string
	// scripts: its npm package's install script is what makes it run
	// (OpenCode's postinstall puts the platform's binary in place of a stub
	// that only says "@opencode/cli's postinstall script was not run"),
	// and npm 12, bun and pnpm 10 run a package's install script only for
	// one they are told to: npm install -g --allow-scripts=<pkg>, bun add
	// -g --trust, pnpm add -g --allow-build=<pkg>. npm 10 and 11 run it
	// anyway, and take --allow-scripts as a config they don't know.
	scripts bool
	// line is the npm package of the line version is in, for a CLI whose
	// own updater (self) stays on its line: OpenCode 1's opencode upgrade
	// runs opencode.ai/install, which installs OpenCode 1 only and refuses
	// a 2.x version, so an OpenCode 1 from that installer is told
	// opencode-ai's newest, an OpenCode 2 @opencode/cli's. "" when the
	// version isn't known: no version is promised.
	line func(version string) string
}

var cliSpecs = map[string]cliSpec{
	"claude": {npm: []string{"@anthropic-ai/claude-code"}, brew: []string{"claude-code"}, scripts: true,
		self: func(bin, real string) []string {
			// ~/.local/bin/claude → ~/.local/share/claude/versions/2.1.3;
			// on Windows the binary itself, in ~/.local/bin
			if strings.Contains(real, "/.local/share/claude/versions/") || strings.HasSuffix(strings.ToLower(real), "/.local/bin/claude.exe") {
				return []string{bin, "update"}
			}
			return nil
		}},
	"codex": {npm: []string{"@openai/codex"}, brew: []string{"codex"},
		self: func(bin, real string) []string {
			// its standalone installer's ~/.codex/packages/standalone/releases/…,
			// reached on Windows through install.ps1's junctions
			// (%LOCALAPPDATA%\Programs\OpenAI\Codex\bin → …\current\bin)
			if strings.Contains(real, "/.codex/packages/standalone/") {
				return []string{bin, "update"}
			}
			return nil
		}},
	"gemini": {npm: []string{"@google/gemini-cli"}, brew: []string{"gemini-cli"}},
	// DevEco Code: npm's @deveco/deveco-code, or its own installer's
	// ~/.local/bin/deveco → ~/.local/share/deveco/versions/<v>/deveco
	// (install), deveco.exe in ~\.local\bin on Windows (install.ps1),
	// both of which deveco upgrade replaces
	// Its npm package carries macOS and Windows binaries only (and
	// OpenHarmony's), none for Linux.
	"deveco": {npm: []string{"@deveco/deveco-code"}, only: []string{"darwin", "windows"}, scripts: true,
		self: func(bin, real string) []string {
			if strings.Contains(real, "/.local/share/deveco/versions/") || strings.HasSuffix(strings.ToLower(real), "/.local/bin/deveco.exe") {
				return []string{bin, "upgrade"}
			}
			return nil
		}},
	// OpenCode 2 is @opencode/cli on npm (opencode-ai has OpenCode 1
	// only, its latest 1.19.5 while @opencode/cli's is 2.2.5, 2026-11-05;
	// math_milian on Discord), its installer opencode.ai/v2/install, which
	// puts it in ~/.opencode/bin as OpenCode 1's did, and Homebrew's
	// opencode formula (2.2.0) or anomalyco/tap's opencode-v2. An install
	// of either package is updated on its own: npm, bun and pnpm keep it
	// to its package, and line keeps opencode upgrade to its version's.
	"opencode": {npm: []string{"@opencode/cli", "opencode-ai"}, brew: []string{"opencode", "opencode-v2"}, target: true, scripts: true,
		line: func(v string) string {
			major, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
			switch n, err := strconv.Atoi(major); {
			case err != nil:
				return ""
			case n < 2:
				return "opencode-ai"
			}
			return "@opencode/cli"
		},
		self: func(bin, real string) []string {
			if strings.Contains(real, "/.opencode/bin/") {
				return []string{bin, "upgrade"}
			}
			return nil
		}},
	// Grok Build: x.ai/cli/install.sh links ~/.grok/bin/grok to
	// ../downloads/grok-<platform>, and grok update to a new
	// ../downloads/grok-<version>-<platform>; install.ps1 copies grok.exe
	// into ~\.grok\bin itself, as grok update does again. grok is also
	// Homebrew's regular-expression tool, hence at; its own cask, grok-build,
	// links $HOMEBREW_PREFIX/bin/grok to Caskroom/grok-build/<v>/grok-<v>-<platform>
	// (Will on Discord), which brew upgrade replaces (see cliBin).
	"grok": {at: ".grok/bin/grok", latest: grokLatest, brew: []string{"grok-build"},
		self: func(bin, real string) []string {
			if l := strings.ToLower(real); strings.Contains(l, "/.grok/bin/") || strings.Contains(l, "/.grok/downloads/") {
				return []string{bin, "update"}
			}
			return nil
		}},
	"pi": {npm: []string{"@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"}, brew: []string{"pi-coding-agent"}},
	// omp: bun's global, or omp.sh's -Binary install, a binary of its own
	// in ~/.local/bin (install, PI_INSTALL_DIR aside) or
	// %LOCALAPPDATA%\omp (install.ps1), which omp update replaces with
	// the release's, as the reporter of #2192 saw it do
	"omp": {npm: []string{"@oh-my-pi/pi-coding-agent"},
		self: func(bin, real string) []string {
			if l := strings.ToLower(real); strings.HasSuffix(l, "/.local/bin/omp") || strings.HasSuffix(l, "/appdata/local/omp/omp.exe") {
				return []string{bin, "update"}
			}
			return nil
		}},
	"copilot":   {npm: []string{"@github/copilot"}, brew: []string{"copilot-cli"}},
	"crush":     {npm: []string{"@charmland/crush"}, brew: []string{"crush"}, scripts: true},
	"cline":     {npm: []string{"cline"}, brew: []string{"cline"}, scripts: true},
	"dsh":       {npm: []string{"@deepseek-ai/dsh"}},
	"codebuddy": {npm: []string{"@tencent-ai/codebuddy-code"}},
	"goose":     {brew: []string{"block-goose-cli"}},
	// Cursor CLI: its own installer (cursor.com/install) links
	// ~/.local/bin/cursor-agent to ~/.local/share/cursor-agent/versions/<v>/
	// cursor-agent, and `cursor-agent --version` says the version
	// ("2026.10.01-e373342"). Not on npm nor Homebrew, and it updates
	// itself, so magpie shows its version only (@G_Z_Y on X saw none).
	"cursor": {},
	// AtomCode's installer chooses /usr/local/bin or ~/.local/bin by
	// writability (`.local\bin\atomcode.exe` on Windows); `atomcode upgrade`
	// exists, but its installed-binary update path is not exercised here, so
	// only its npm package and brew cask are recognized
	"atomcode": {npm: []string{"@atomgit.com/atomcode"}, brew: []string{"atomcode"}},
	// Snow CLI is published on npm alone (snow-ai)
	"snow": {npm: []string{"snow-ai"}, scripts: true},
	// MothX: its npm installer package, which brings the native binary
	// for the platform as an optional dependency
	"mothx": {npm: []string{"mothx-installer"}, scripts: true},
	// PenguinHarness: its npm package, or install.sh's tarball in
	// ~/.penguin (bin, lib, node; ~/.local/bin/penguin links there), which
	// penguin update re-runs the installer for. On Windows its update
	// refuses a tarball (update.ts planUpdate, "windows-installer"), so
	// none is offered there.
	"penguin": {npm: []string{"@prismshadow/penguin-cli"},
		self: func(bin, real string) []string {
			if runtime.GOOS == "windows" {
				return nil
			}
			if l := strings.ToLower(real); strings.Contains(l, "/.penguin/bin/") || strings.Contains(l, "/.penguin/lib/") {
				return []string{bin, "update", "--yes"}
			}
			return nil
		}},
	// omo update, however it was installed: OmO's own updater, which moves
	// the engine it pins (senpi) with it
	"omo": {npm: []string{"omo-ai"}, scripts: true,
		self: func(bin, real string) []string { return []string{bin, "update"} }},
	// mu-agent from npm: its own `mu update` updates Pi packages, not mu
	"mu": {npm: []string{"mu-agent"}},
}

// updater is how an installed CLI is brought up to date.
type updater struct {
	via  string   // npm, bun, pnpm, brew, self
	pkg  string   // the npm package, or the Homebrew formula or cask
	cask bool     // pkg is a Homebrew cask
	brew string   // the brew that has it
	cmd  []string // the update
	path string   // a folder to put first on PATH for it (its npm's node)
	// admin: npm's global folder (prefix) is one only an administrator can
	// change, so the update runs after Windows' administrator prompt
	admin  bool
	prefix string
	// latest is the newest its own updater would install (cliSpec.latest)
	latest func() string
	// reshim is run after the update: the version manager whose shim the
	// CLI was found as making shims for what it installed
	reshim []string
}

// shown is the update as a person would type it.
func (u *updater) shown() string {
	parts := append([]string{filepath.Base(u.cmd[0])}, u.cmd[1:]...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t") {
			parts[i] = strconv.Quote(p)
		}
	}
	return strings.Join(parts, " ")
}

var brewPath = lazyre.New(`/(Cellar|Caskroom)/([^/]+)/`)

// howInstalled says how the CLI at bin was installed, from where it is;
// nil when that says nothing sure.
func howInstalled(spec cliSpec, bin string) *updater {
	real, err := proc.RealPath(bin)
	if err != nil {
		real = bin
	}
	slash := filepath.ToSlash(real)
	if spec.self != nil {
		if c := spec.self(bin, slash); c != nil {
			pkg := ""
			if len(spec.npm) > 0 {
				pkg = spec.npm[0]
			}
			u := &updater{via: "self", pkg: pkg, cmd: c, latest: spec.latest}
			u.path = selfPath(spec, bin, slash)
			return u
		}
	}
	// a formula of the agent's own is Homebrew's, though a Node one keeps
	// the npm package in its keg (Cellar/gemini-cli/<v>/libexec/lib/
	// node_modules/@google/gemini-cli/…), where npm install -g --prefix
	// would change the keg behind brew's back
	m := brewPath.FindStringSubmatch(slash)
	if m != nil && contains(spec.brew, m[2]) {
		return fromBrew(slash, m)
	}
	// otherwise a package manager's before Homebrew's: npm under Homebrew's
	// node (a keg-only node@20's Cellar/node@20/<v>/lib/node_modules) is npm's
	for _, pkg := range spec.npm {
		if u := fromNodeModules(pkg, spec.scripts, bin, slash); u != nil {
			return u
		}
	}
	return nil
}

// selfPath is the folder to put first on PATH for a CLI's own updater, when
// a package manager installed it in a global node_modules: the updater runs
// that manager by its bare name (omo update runs npm i -g), and magpie's
// PATH can find another one first, a system node's in /usr/local/bin
// before a version manager's, whose npm then installs into a folder only
// root may change (#2021, ggbdpq). npm's global folder follows the node
// that runs it, so the folder holds that node too.
func selfPath(spec cliSpec, bin, real string) string {
	for _, pkg := range spec.npm {
		if u := fromNodeModules(pkg, false, bin, real); u != nil {
			if u.via == "npm" {
				return u.path
			}
			if u.via == "bun" || u.via == "pnpm" {
				return filepath.Dir(u.cmd[0])
			}
			return ""
		}
	}
	return ""
}

// fromBrew is the updater of the CLI at real (forward slashes), which m,
// brewPath's match in it, says Homebrew's formula or cask m[2] installed.
func fromBrew(real string, m []string) *updater {
	prefix := real[:strings.Index(real, m[0])]
	brew := filepath.FromSlash(prefix + "/bin/brew")
	if !isFile(brew) {
		var err error
		if brew, err = proc.Look("brew"); err != nil {
			return nil
		}
	}
	u := &updater{via: "brew", pkg: m[2], cask: m[1] == "Caskroom", brew: brew}
	u.cmd = []string{brew, "upgrade", m[2]}
	if u.cask {
		u.cmd = []string{brew, "upgrade", "--cask", m[2]}
	}
	return u
}

// brewBin is the CLI of an agent with no Bin (spec.at's name) where PATH
// has it, when Homebrew's formula or cask of the agent's put it there: a
// command of that name that isn't one of the agent's is another tool's
// (Homebrew's grok formula). "" otherwise.
func brewBin(spec cliSpec) string {
	if spec.at == "" || len(spec.brew) == 0 {
		return ""
	}
	bin := binAt(path.Base(spec.at))
	if bin == "" {
		return ""
	}
	real, err := proc.RealPath(bin)
	if err != nil {
		return ""
	}
	if m := brewPath.FindStringSubmatch(filepath.ToSlash(real)); m == nil || !contains(spec.brew, m[2]) {
		return ""
	}
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	return bin
}

// fromNodeModules is the updater of a CLI the package pkg installed in a
// global node_modules: the binary leads there (npm's and bun's links), or
// is a shim that names it (npm's .cmd on Windows, pnpm's scripts).
// scripts: the package's install script is let run (cliSpec.scripts).
func fromNodeModules(pkg string, scripts bool, bin, real string) *updater {
	mark := "/node_modules/" + pkg + "/"
	modules := ""
	if i := strings.Index(real, mark); i >= 0 {
		modules = real[:i] + "/node_modules"
	} else if shimNames(bin, pkg) {
		modules = filepath.ToSlash(filepath.Join(filepath.Dir(bin), "node_modules"))
		if !isDir(filepath.Join(filepath.FromSlash(modules), filepath.FromSlash(pkg))) {
			modules = "" // pnpm's shims name a store elsewhere
		}
	} else if !bunxNames(bin, pkg) {
		return nil
	}
	lower := strings.ToLower(real + "|" + filepath.ToSlash(bin))
	switch {
	case strings.Contains(lower, "/.bun/") || modules == "" && bunxNames(bin, pkg):
		bun := filepath.Join(filepath.Dir(bin), exe("bun"))
		if !isFile(bun) {
			var err error
			if bun, err = proc.Look("bun"); err != nil {
				return nil
			}
		}
		return &updater{via: "bun", pkg: pkg, cmd: withScripts([]string{bun, "add", "-g"}, "bun", pkg, scripts)}
	case strings.Contains(lower, "/pnpm/") || strings.Contains(lower, "/.pnpm/"):
		pnpm, err := proc.Look("pnpm")
		if err != nil {
			return nil
		}
		return &updater{via: "pnpm", pkg: pkg, cmd: withScripts([]string{pnpm, "add", "-g"}, "pnpm", pkg, scripts)}
	case strings.Contains(lower, "/.volta/") || strings.Contains(lower, "/yarn/") || strings.Contains(lower, "/.yarn/"):
		return nil // installed by a manager of its own, which magpie leaves to it
	}
	if modules == "" {
		return nil
	}
	// the prefix npm installed it under: …/lib/node_modules on a Mac or
	// Linux, …\node_modules itself on Windows
	m := filepath.FromSlash(modules)
	prefix := filepath.Dir(m)
	npm := filepath.Join(prefix, "npm.cmd")
	if runtime.GOOS != "windows" {
		if filepath.Base(prefix) != "lib" {
			return nil // not a global install
		}
		prefix = filepath.Dir(prefix)
		npm = filepath.Join(prefix, "bin", "npm")
	}
	u := &updater{via: "npm", pkg: pkg, prefix: prefix}
	if !canWrite(m) {
		if !adminNPM {
			return nil // the system's (/usr/lib): npm would want sudo
		}
		// Node.js's own folder, C:\Program Files\nodejs, where npm's
		// prefix was left there (新v学员 on Discord)
		u.admin = true
	}
	if isFile(npm) {
		u.path = filepath.Dir(npm) // its own node, not another on PATH
	} else {
		var err error
		if npm, err = proc.Look("npm"); err != nil {
			return nil
		}
	}
	u.cmd = withScripts([]string{npm, "install", "-g", "--prefix", prefix}, "npm", pkg, scripts)
	return u
}

// withScripts is cmd, a package manager's global install, followed by
// what lets pkg's install script run when scripts (cliSpec.scripts),
// then pkg@latest, last: a retry puts another version there.
func withScripts(cmd []string, via, pkg string, scripts bool) []string {
	if scripts {
		switch via {
		case "npm":
			cmd = append(cmd, "--allow-scripts="+pkg)
		case "bun":
			cmd = append(cmd, "--trust")
		case "pnpm":
			cmd = append(cmd, "--allow-build="+pkg)
		}
	}
	return append(cmd, pkg+"@latest")
}

// bunxNames says whether bin is bun's Windows shim of pkg's program: a
// small opencode.exe that runs what the opencode.bunx beside it names, in
// UTF-16, from bun's folder: install\global\node_modules\opencode-ai\
// bin\opencode.exe (bun 1.4.3, #2192). The .exe itself names no package.
func bunxNames(bin, pkg string) bool {
	if !strings.EqualFold(filepath.Ext(bin), ".exe") {
		return false
	}
	b, err := os.ReadFile(strings.TrimSuffix(bin, filepath.Ext(bin)) + ".bunx")
	if err != nil || len(b) > 64<<10 {
		return false
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	named := strings.ReplaceAll(string(utf16.Decode(u)), `\`, "/")
	return strings.Contains(named, "node_modules/"+pkg+"/")
}

// shimNames says whether bin is a small script naming pkg's folder.
func shimNames(bin, pkg string) bool {
	st, err := os.Stat(bin)
	if err != nil || st.Size() > 64<<10 {
		return false
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ReplaceAll(string(b), `\`, "/"), "node_modules/"+pkg+"/")
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func isFile(p string) bool { dir, ok := statKind(p); return ok && !dir }
func isDir(p string) bool  { dir, ok := statKind(p); return ok && dir }

// ---- versions --------------------------------------------------------------

var versionRe = lazyre.New(`(?:^|[^\w.])v?(\d+\.\d+(?:\.\d+)*(?:-[0-9A-Za-z][0-9A-Za-z.]*)?)`)

// parseVersion is the first version in what a CLI printed.
func parseVersion(out string) string {
	if m := versionRe.FindStringSubmatch(ansiRe.ReplaceAllString(out, " ")); m != nil {
		return strings.TrimRight(m[1], ".")
	}
	return ""
}

// Newer says whether version a is after b: by their numbers, then a release
// after its pre-releases (1.0.87 after 1.0.87-0).
func Newer(a, b string) bool {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	an, apre, _ := strings.Cut(a, "-")
	bn, bpre, _ := strings.Cut(b, "-")
	x, y := strings.Split(an, "."), strings.Split(bn, ".")
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p, _ = strconv.Atoi(x[i])
		}
		if i < len(y) {
			q, _ = strconv.Atoi(y[i])
		}
		if p != q {
			return p > q
		}
	}
	switch {
	case apre == bpre:
		return false
	case apre == "":
		return true
	case bpre == "":
		return false
	}
	return preNewer(apre, bpre)
}

// preNewer orders pre-release tags part by part, numbers as numbers.
func preNewer(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		p, e1 := strconv.Atoi(x[i])
		q, e2 := strconv.Atoi(y[i])
		switch {
		case e1 == nil && e2 == nil:
			if p != q {
				return p > q
			}
		case x[i] != y[i]:
			return x[i] > y[i]
		}
	}
	return len(x) > len(y)
}

// memo keeps an answer that took a process or a request to get, with one
// asking at a time.
type memo struct {
	mu sync.Mutex
	m  map[string]*memoEntry
}

type memoEntry struct {
	v     string
	stamp string
	until time.Time
	ready chan struct{}
}

// get is the answer for key, asked again once stamp changes or it's out of
// date: good for ok, a failure for fail (the answer before kept meanwhile).
func (m *memo) get(key, stamp string, ok, fail time.Duration, ask func() (string, error)) string {
	m.mu.Lock()
	if m.m == nil {
		m.m = map[string]*memoEntry{}
	}
	e := m.m[key]
	if e != nil && e.stamp == stamp && (e.ready != nil || time.Now().Before(e.until)) {
		ready := e.ready
		m.mu.Unlock()
		if ready != nil {
			<-ready
			m.mu.Lock()
			v := e.v
			m.mu.Unlock()
			return v
		}
		return e.v
	}
	old := ""
	if e != nil && e.stamp == stamp {
		old = e.v
	}
	e = &memoEntry{v: old, stamp: stamp, ready: make(chan struct{})}
	m.m[key] = e
	m.mu.Unlock()
	v, err := ask()
	m.mu.Lock()
	if err == nil {
		e.v, e.until = v, time.Now().Add(ok)
	} else {
		e.until = time.Now().Add(fail)
	}
	close(e.ready)
	e.ready = nil
	v = e.v
	m.mu.Unlock()
	return v
}

func (m *memo) forget(key string) {
	m.mu.Lock()
	if e := m.m[key]; e != nil && e.ready == nil {
		delete(m.m, key)
	}
	m.mu.Unlock()
}

// expireAll has every kept answer whose ask isn't running asked again on
// its next read: the Agents page's re-detect. The answer stays as the one
// before, which a failed ask keeps, as get does for an answer out of date.
func (m *memo) expireAll() {
	m.mu.Lock()
	for _, e := range m.m {
		if e.ready == nil {
			e.until = time.Time{}
		}
	}
	m.mu.Unlock()
}

var versions, latests memo

// installedVersion is what the CLI at bin says its version is, asked again
// only once the binary changes: the file its links and junctions lead to,
// its size or its time (an update's release folder is a new path even where
// the archive gave every version's binary the same time).
func installedVersion(bin string) string {
	real, err := proc.RealPath(bin)
	if err != nil {
		real = bin
	}
	st, err := os.Stat(real)
	if err != nil {
		return ""
	}
	stamp := fmt.Sprint(real, st.Size(), st.ModTime().UnixNano())
	return versions.get(bin, stamp, 24*time.Hour, 10*time.Minute, func() (string, error) {
		v := parseVersion(runVersion(bin))
		if v == "" {
			return "", errors.New("no version")
		}
		return v, nil
	})
}

// runVersion is what bin says its version is (proc.Version: read from its
// npm package, else bin --version, not run again after it failed); a var so
// tests can fake it.
var runVersion = proc.Version

// latestVersion is the newest version where u would update from.
func latestVersion(u *updater) string {
	if u.via == "brew" {
		return latests.get("brew:"+u.pkg, "", 6*time.Hour, 15*time.Minute, func() (string, error) { return brewLatest(u) })
	}
	if u.latest != nil {
		return u.latest()
	}
	if u.pkg == "" {
		return ""
	}
	return latests.get("npm:"+u.pkg, "", 6*time.Hour, 15*time.Minute, func() (string, error) { return npmLatest(u.pkg) })
}

// npmRegistry is where npm's packages are read; a var so tests can serve one.
var npmRegistry = "https://registry.npmjs.org/"

func npmLatest(pkg string) (string, error) {
	c := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequest("GET", npmRegistry+pkg+"/latest", nil)
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	resp, err := source.Do(c, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("npm answered %s", resp.Status)
	}
	var r struct{ Version string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return "", err
	}
	if r.Version == "" {
		return "", errors.New("no version")
	}
	return r.Version, nil
}

// grokReleases is where Grok's installers and grok update read the newest
// version of a channel (x.ai/cli/stable), in front of
// storage.googleapis.com/grok-build-public-artifacts/cli; a var so tests can
// serve one.
var grokReleases = []string{"https://x.ai/cli/", "https://storage.googleapis.com/grok-build-public-artifacts/cli/"}

// grokLatest is the newest Grok Build of the channel it follows: the
// installer's GROK_CHANNEL or grok update --alpha, kept under [cli] in its
// config.toml (stable when none).
func grokLatest() string {
	dir := appdir.Getenv("GROK_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grok")
	}
	channel := "stable"
	if cli, _ := edit.GetTOMLTable(filepath.Join(dir, "config.toml"), "cli"); cli["channel"] == "alpha" || cli["channel"] == "enterprise" {
		channel = cli["channel"]
	}
	return latests.get("grok:"+channel, "", 6*time.Hour, 15*time.Minute, func() (v string, err error) {
		for _, base := range grokReleases {
			if v, err = channelVersion(base + channel); err == nil {
				return v, nil
			}
		}
		return "", err
	})
}

// channelVersion is the version a release channel's pointer file names.
func channelVersion(url string) (string, error) {
	c := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "magpie")
	resp, err := source.Do(c, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	// the whole body is the version: 1.0.50
	v := strings.TrimSpace(string(b))
	if v == "" || parseVersion(" "+v) != strings.TrimPrefix(v, "v") {
		return "", fmt.Errorf("%s: no version", url)
	}
	return strings.TrimPrefix(v, "v"), nil
}

// brewLatest is the version Homebrew has for u, as it last updated itself:
// what brew upgrade would install, or newer once it updates first.
var brewLatest = func(u *updater) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"info", "--json=v2", u.pkg}
	if u.cask {
		args = []string{"info", "--json=v2", "--cask", u.pkg}
	}
	cmd := proc.CommandContext(ctx, u.brew, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	var r struct {
		Formulae []struct {
			Versions struct{ Stable string }
		}
		Casks []struct{ Version string }
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", err
	}
	v := ""
	if len(r.Formulae) > 0 {
		v = r.Formulae[0].Versions.Stable
	} else if len(r.Casks) > 0 {
		v, _, _ = strings.Cut(r.Casks[0].Version, ",") // 1.2.3,build
	}
	if v = parseVersion(" " + v); v == "" {
		return "", errors.New("no version")
	}
	return v, nil
}

// loginPath is the PATH a terminal opened now has (proc.LoginPath); a var
// so tests can fake it.
var loginPath = proc.LoginPath

// offPath reports whether bin's folder is none of dirs, a terminal's PATH;
// false when dirs is nil, a PATH that can't be told.
func offPath(bin string, dirs []string) bool {
	if dirs == nil {
		return false
	}
	at := filepath.Clean(filepath.Dir(bin))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		if strings.EqualFold(filepath.Clean(d), at) && (runtime.GOOS == "windows" || filepath.Clean(d) == at) {
			return false
		}
	}
	return true
}

// cliBin is where the agent's CLI is, "" when it has none magpie knows or
// none is installed. One not on magpie's PATH is looked for where a user's
// tools are installed (proc.FindTool), on every system as on Windows: a
// `magpie serve` or a service started with a PATH that lacks ~/.bun/bin, an
// npm prefix or an nvm Node other than the default never found omp there,
// so its version stayed unknown and its models offered xhigh in place of
// max (whqtian on Discord: fixed on Windows and in WSL, still xhigh on
// Ubuntu).
func (a *Agent) cliBin() (string, cliSpec) {
	spec, ok := cliSpecs[a.ID]
	if !ok || a.WSL != "" {
		return "", spec
	}
	if a.Bin == "" {
		// Homebrew's, where PATH has it: what a terminal runs, before a
		// vendor install a brew one replaced
		if bin := brewBin(spec); bin != "" {
			return bin, spec
		}
		home, err := os.UserHomeDir()
		if spec.at == "" || err != nil {
			return "", spec
		}
		if bin := filepath.Join(home, filepath.FromSlash(exe(spec.at))); isFile(bin) {
			return bin, spec
		}
		return "", spec
	}
	bin := binAt(a.Bin)
	if !a.binsOwn(bin) {
		// a launcher of another program's is not the CLI: where else a
		// user's tools are installed, then (VS Code's copilot shim, #3269)
		if at := proc.FindTool(a.Bin); a.binsOwn(at) {
			bin = at
		} else {
			return "", spec
		}
	}
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	return bin, spec
}

// BinPath is where the agent's own program is, as Detected finds its Bin,
// else where cliBin looks for one with no Bin (Grok Build's); "" for an
// agent in a WSL distro and one with no program here (a desktop app).
func (a *Agent) BinPath() string {
	if a.WSL != "" {
		return ""
	}
	if a.Bin == "" {
		bin, _ := a.cliBin()
		return bin
	}
	if bin := binAt(a.Bin); a.binsOwn(bin) {
		return bin
	}
	if bin := proc.FindTool(a.Bin); a.binsOwn(bin) {
		return bin
	}
	return ""
}

// CLI is the agent's CLI: its version, the newest, and whether magpie can
// update it, with its desktop app's version. ok is false for an agent with
// neither a CLI magpie knows nor its app.
func (a *Agent) CLI() (c CLI, ok bool) {
	c.App = a.AppVersion()
	bin, spec := a.cliBin()
	if bin == "" {
		if a.ID == "claude" && a.WSL == "" {
			c.InDesktop, c.DesktopVersion = claudeInDesktop()
		}
		return c, c.App != "" || c.InDesktop
	}
	real, u := installedAt(spec, bin)
	c.Version = installedVersion(real)
	c.Path = bin
	c.OffPath = a.Bin != "" && offPath(bin, loginPath())
	if u == nil {
		return c, true
	}
	c.Via, c.Command, c.Admin = u.via, u.shown(), u.admin
	c.Latest = latestVersion(u)
	c.Update = c.Version != "" && c.Latest != "" && Newer(c.Latest, c.Version)
	return c, true
}

// claudeInDesktop says whether Claude Desktop is here, with the newest
// Claude Code it keeps for its Code tab (desktopdir.ClaudeCode): its
// bundle on a Mac, its folders or its install elsewhere (its detect).
func claudeInDesktop() (here bool, version string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	for _, v := range desktopdir.ClaudeCode(desktopdir.OS, home, os.Getenv) {
		if version == "" || Newer(v, version) {
			version = v
		}
	}
	if version != "" {
		return true, version
	}
	if desktopdir.OS == "darwin" {
		return appHere("Claude.app"), ""
	}
	return claudeDesktop(home).Detected(), ""
}

// InstalledVersion is what the agent's CLI on PATH says its version is: ""
// for an agent without a CLI magpie knows, one not on PATH, or one that
// didn't say. Asked once per binary, like CLI's; nothing is fetched. An
// agent in a WSL distro's is what the distro's own CLI said at its probe,
// for the kinds that ask (wslKind.version), never this machine's.
func (a *Agent) InstalledVersion() string {
	if a.WSL != "" {
		return a.wslVersion
	}
	bin, _ := a.cliBin()
	if bin == "" {
		return ""
	}
	real, _ := throughShim(bin)
	return installedVersion(real)
}

// CLIs is every detected agent's CLI, as far as it is known within wait;
// pending says some are still being asked, and will be ready next time.
func CLIs(wait time.Duration) (out map[string]CLI, pending bool) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out = map[string]CLI{}
	for _, a := range Detected() {
		if _, ok := cliSpecs[a.ID]; !ok || a.WSL != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, ok := a.CLI(); ok {
				mu.Lock()
				out[a.ID] = c
				mu.Unlock()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		pending = true
	}
	mu.Lock()
	defer mu.Unlock()
	snap := make(map[string]CLI, len(out))
	for k, v := range out {
		snap[k] = v
	}
	return snap, pending
}

var updating sync.Map // agent id → an update running

// canWrite is writable, and adminNPM whether an npm whose global folder
// this user can't write in is updated as administrator (on Windows, after
// its prompt) rather than not offered; vars so tests can stand in for
// them.
var (
	canWrite = writable
	adminNPM = update.CanRunElevated()
)

// runAdmin runs an update as administrator; a var so tests never ask for
// one.
var runAdmin = func(ctx context.Context, u *updater) ([]byte, error) {
	home, _ := os.UserHomeDir()
	return update.RunElevated(ctx, home, updateEnv(u), u.cmd...)
}

// AdminError is an install or update that needs an administrator and
// didn't get one: the prompt declined (Canceled), or none to be had (Err).
// The command never ran. Agent is what it installs (an agent, or RTK);
// Prefix the folder only an administrator can change, "" when not known;
// Via the tool, npm or winget, whose fix is said.
type AdminError struct {
	Agent, Prefix, Command, Via string
	Canceled                    bool
	Err                         error
}

func (e *AdminError) Error() string {
	why := "Windows' administrator prompt was declined"
	if !e.Canceled {
		why = "magpie couldn't ask Windows for one (" + e.Err.Error() + ")"
	}
	where := fmt.Sprintf("%s is installed in %s, which only an administrator can change", e.Agent, e.Prefix)
	if e.Prefix == "" {
		where = e.Agent + " needs an administrator here"
	}
	if e.Via == "winget" {
		return fmt.Sprintf("%s, and %s. Try again and allow it, or run %s in a terminal opened as administrator.", where, why, e.Command)
	}
	return fmt.Sprintf(`%s, and %s. Update again and allow it, run %s in a terminal opened as administrator, or move npm's global folder to one of your own (npm config set prefix "%%APPDATA%%\npm") and install %s again.`,
		where, why, e.Command, e.Agent)
}

func (e *AdminError) Unwrap() error { return e.Err }

// runUpdate runs an update with nothing to answer it; a var so tests never
// run a real one.
var runUpdate = func(ctx context.Context, u *updater) ([]byte, error) {
	cmd := proc.CommandContext(ctx, u.cmd[0], u.cmd[1:]...)
	cmd.Stdin = nil
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = updateEnv(u)
	return cmd.CombinedOutput()
}

// updateEnv is the environment an update runs in: the proxy magpie's own
// requests take (a SOCKS5 one bridged for bun, which can't use it: #1409),
// and u's PATH first.
func updateEnv(u *updater) []string {
	env := netproxy.Env(os.Environ())
	if u.via == "bun" {
		env = netproxy.EnvForBun(env)
	}
	if u.path != "" {
		for i, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
				env[i] = k + "=" + u.path + string(os.PathListSeparator) + v
			}
		}
	}
	// the --version run after an update starts Cline, which would otherwise
	// check for (and schedule) an update of its own
	return append(env, "NONINTERACTIVE=1", "HOMEBREW_NO_ENV_HINTS=1", "NO_COLOR=1", "CLINE_NO_AUTO_UPDATE=1")
}

// letGoBeforeUpdate ends what magpie itself keeps running of the agent
// id's CLI, before a package manager on Windows replaces its package: a
// program running from it can't be removed there (EPERM), and npm, which
// takes the old shims away first, stopped partway and left none (CLJY on
// Discord: %APPDATA%\npm with only the sh shim of claude, no claude.cmd,
// and no codex at all). The Claude subscription bridge's runs kept for
// their conversations' next turns, and Codex's app-server daemon with no
// session on it; a var so tests can see it asked.
var letGoBeforeUpdate = func(ctx context.Context, id string) {
	switch id {
	case "claude":
		if n := gateway.LetGoIdleClaudeCode(); n > 0 {
			log.Printf("claude: ended %d Claude Code runs waiting for their next turn, to update it", n)
		}
	case "codex":
		if stopped, err := provider.StopIdleCodexDaemon(ctx); err != nil {
			log.Println("codex: before its update:", err)
		} else if stopped {
			log.Println("codex: stopped its app-server daemon, to update it")
		}
	}
}

// updateGOOS is runtime.GOOS; a var so tests can be Windows.
var updateGOOS = runtime.GOOS

// updateTimeout bounds an update: a download, or Homebrew updating itself first.
var updateTimeout = 10 * time.Minute

// UpdateCLI brings the agent's CLI up to date the way it was installed, and
// says what it is afterwards.
func (a *Agent) UpdateCLI() (CLI, error) {
	bin, spec := a.cliBin()
	if bin == "" {
		return CLI{}, fmt.Errorf("magpie can't find %s's CLI", a.Name)
	}
	real, u := installedAt(spec, bin)
	if u == nil {
		return CLI{}, fmt.Errorf("magpie can't tell how %s was installed at %s — update it the way you installed it", a.Name, bin)
	}
	if _, busy := updating.LoadOrStore(a.ID, true); busy {
		return CLI{}, fmt.Errorf("%s is already being updated", a.Name)
	}
	defer updating.Delete(a.ID)
	before := installedVersion(real)
	if spec.target && u.via == "self" {
		// the version the row promised, so its own updater needn't ask
		// GitHub's API for the newest (see cliSpec.target); npm, bun,
		// pnpm and brew take no version after their command
		if v := latestVersion(u); Newer(v, before) {
			u.cmd = append(u.cmd, v)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	if updateGOOS == "windows" && (u.via == "npm" || u.via == "bun" || u.via == "pnpm") {
		letGoBeforeUpdate(ctx, a.ID)
	}
	run := runUpdate
	if u.admin {
		run = runAdmin
	}
	out, err := run(ctx, u)
	if u.admin && (errors.Is(err, update.ErrCanceled) || errors.Is(err, update.ErrNoAdmin)) {
		// the prompt declined, or none shown: npm never ran
		return CLI{}, &AdminError{Agent: a.Name, Prefix: u.prefix, Command: u.shown(), Via: "npm", Canceled: errors.Is(err, update.ErrCanceled), Err: err}
	}
	if err == nil && u.reshim != nil {
		// what it installed is kept even when no shim is made for it
		runUpdate(ctx, &updater{via: "reshim", cmd: u.reshim})
	}
	versions.forget(real)
	shimTargets.forget(bin)
	// brew upgrade updates Homebrew first, which may know a newer one
	latests.forget("brew:" + u.pkg)
	c, _ := a.CLI()
	if err != nil {
		msg := lastLines(string(out), 3)
		if msg == "" || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = strings.TrimSpace(msg + " " + err.Error())
		}
		return c, fmt.Errorf("%s: %s", u.shown(), msg)
	}
	if updateGOOS == "windows" && !isFile(bin) {
		// npm took the old shims away and stopped before it wrote the new
		// ones: a file of the old package's was still running (EPERM), and
		// what is left of claude is npm's sh shim, which Windows can't run
		// (CLJY on Discord)
		return c, fmt.Errorf("%s was updated, but %s is gone (a copy of it still running keeps %s from finishing): quit it, and %s", a.Name, bin, u.via, mendHint(u, c.Version))
	}
	if c.Update && c.Version == before {
		return c, fmt.Errorf("%s is still %s after %s; %s is out", a.Name, c.Version, u.shown(), c.Latest)
	}
	// the package says its new version, but that isn't the CLI running: npm
	// leaves out an optional dependency it couldn't fetch and still exits 0,
	// and Claude Code without its native package is a stub that only says
	// "claude native binary not installed" (#2128)
	broken := runsBroken(ctx, bin, u)
	if broken != "" && u.via == "npm" && !u.admin {
		// once more, the version just installed with its optional
		// packages: how the reporter's own install was mended
		again := *u
		again.cmd = append([]string{}, u.cmd...)
		at := c.Version
		if at == "" {
			at = "latest"
		}
		again.cmd[len(again.cmd)-1] = u.pkg + "@" + at
		again.cmd = append(again.cmd, "--include=optional")
		if _, err := runUpdate(ctx, &again); err == nil {
			versions.forget(real)
			shimTargets.forget(bin)
			c, _ = a.CLI()
			broken = runsBroken(ctx, bin, u)
		}
	}
	if broken != "" {
		return c, fmt.Errorf("%s was updated, but %s --version fails: %s — %s", a.Name, filepath.Base(bin), broken, mendHint(u, c.Version))
	}
	return c, nil
}

// runsBroken is what bin --version says when it ran and failed without
// saying a version, "" when it said one or what it is can't be told: it
// didn't start, took too long, or its interpreter wasn't found (127), none
// of which says the update broke it. It runs as the update did, its npm's
// folder first on PATH, so a node CLI finds the node it was installed for.
func runsBroken(ctx context.Context, bin string, u *updater) string {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	out, code, err := runCheck(ctx, bin, updateEnv(u))
	if err == nil || ctx.Err() != nil || code <= 0 || code == 127 || parseVersion(out) != "" {
		return ""
	}
	if msg := lastLines(out, 2); msg != "" {
		return msg
	}
	return fmt.Sprintf("exit status %d", code)
}

// checkTimeout bounds the run after an update: a busy machine's first start
// of a CLI that was just written.
var checkTimeout = 30 * time.Second

// runCheck runs bin --version with nothing on its stdin, and says what it
// printed and its exit code (-1 when it didn't run to one); a var so tests
// can fake it.
var runCheck = func(ctx context.Context, bin string, env []string) (string, int, error) {
	cmd := proc.ProbeContext(ctx, bin, "--version")
	cmd.Stdin = nil
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := -1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	return string(out), code, err
}

// scriptsFlag is the flag u's update lets its package's install script
// run with (withScripts), and a space; "" for none.
func scriptsFlag(u *updater) string {
	for _, a := range u.cmd {
		if a == "--trust" || strings.HasPrefix(a, "--allow-scripts=") || strings.HasPrefix(a, "--allow-build=") {
			return a + " "
		}
	}
	return ""
}

// mendHint is how to put back a CLI an update left unable to run.
func mendHint(u *updater, version string) string {
	at := version
	if at == "" {
		at = "latest"
	}
	switch u.via {
	case "npm":
		prefix := u.prefix
		if strings.ContainsAny(prefix, " \t") {
			prefix = `"` + prefix + `"`
		}
		return fmt.Sprintf("its install is incomplete (npm can leave out a native package and still succeed); reinstall it with: npm install -g --prefix %s %s%s@%s --include=optional", prefix, scriptsFlag(u), u.pkg, at)
	case "bun", "pnpm":
		return fmt.Sprintf("its install is incomplete; reinstall it with: %s add -g %s%s@%s", u.via, scriptsFlag(u), u.pkg, at)
	case "brew":
		cask := ""
		if u.cask {
			cask = " --cask"
		}
		return fmt.Sprintf("reinstall it with: brew reinstall%s %s", cask, u.pkg)
	}
	return "run " + u.shown() + " again, or reinstall it the way you installed it"
}

var ansiRe = lazyre.New(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// lastLines is the last n lines of out with something on them, without
// their colours.
func lastLines(out string, n int) string {
	var keep []string
	lines := strings.Split(ansiRe.ReplaceAllString(out, ""), "\n")
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{l}, keep...)
		}
	}
	return strings.Join(keep, " · ")
}
