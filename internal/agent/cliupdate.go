package agent

// An agent's command-line program is kept up to date from the Agents page
// (#202): its version, the newest there is, and a click that updates it the
// way it was installed — with the CLI's own updater where it came from the
// vendor's installer (claude update, codex update, opencode upgrade), or by
// the package manager that owns it (npm, bun, pnpm, Homebrew). How it was
// installed is read from where its binary is; when that says nothing sure,
// the version is shown and nothing offered.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/source"
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
}

// cliSpec is where an agent's CLI is published.
type cliSpec struct {
	npm  []string // its packages on npm, the current one first
	brew []string // its Homebrew formulae and casks
	// self is the CLI's own updater, when the binary (real is where its
	// links lead, with forward slashes) is the one its vendor's installer
	// put there
	self func(bin, real string) []string
}

var cliSpecs = map[string]cliSpec{
	"claude": {npm: []string{"@anthropic-ai/claude-code"}, brew: []string{"claude-code"},
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
			// its standalone installer's ~/.codex/packages/standalone/releases/…
			if strings.Contains(real, "/.codex/packages/standalone/") {
				return []string{bin, "update"}
			}
			return nil
		}},
	"gemini": {npm: []string{"@google/gemini-cli"}, brew: []string{"gemini-cli"}},
	"opencode": {npm: []string{"opencode-ai"}, brew: []string{"opencode"},
		self: func(bin, real string) []string {
			if strings.Contains(real, "/.opencode/bin/") {
				return []string{bin, "upgrade"}
			}
			return nil
		}},
	"pi":        {npm: []string{"@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"}},
	"omp":       {npm: []string{"@oh-my-pi/pi-coding-agent"}},
	"copilot":   {npm: []string{"@github/copilot"}, brew: []string{"copilot-cli"}},
	"crush":     {npm: []string{"@charmland/crush"}, brew: []string{"crush"}},
	"cline":     {npm: []string{"cline"}},
	"dsh":       {npm: []string{"@deepseek-ai/dsh"}},
	"codebuddy": {npm: []string{"@tencent-ai/codebuddy-code"}},
	"goose":     {brew: []string{"block-goose-cli"}},
	// AtomCode's installer chooses /usr/local/bin or ~/.local/bin by
	// writability (`.local\bin\atomcode.exe` on Windows); `atomcode upgrade`
	// exists, but its installed-binary update path is not exercised here, so
	// only its npm package and brew cask are recognized
	"atomcode": {npm: []string{"@atomgit.com/atomcode"}, brew: []string{"atomcode"}},
	// omo update, however it was installed: OmO's own updater, which moves
	// the engine it pins (senpi) with it
	"omo": {npm: []string{"omo-ai"},
		self: func(bin, real string) []string { return []string{bin, "update"} }},
}

// updater is how an installed CLI is brought up to date.
type updater struct {
	via  string   // npm, bun, pnpm, brew, self
	pkg  string   // the npm package, or the Homebrew formula or cask
	cask bool     // pkg is a Homebrew cask
	brew string   // the brew that has it
	cmd  []string // the update
	path string   // a folder to put first on PATH for it (its npm's node)
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

var brewPath = regexp.MustCompile(`/(Cellar|Caskroom)/([^/]+)/`)

// howInstalled says how the CLI at bin was installed, from where it is;
// nil when that says nothing sure.
func howInstalled(spec cliSpec, bin string) *updater {
	real, err := filepath.EvalSymlinks(bin)
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
			return &updater{via: "self", pkg: pkg, cmd: c}
		}
	}
	// a package manager's before Homebrew's: npm under Homebrew's node
	// is npm's
	for _, pkg := range spec.npm {
		if u := fromNodeModules(pkg, bin, slash); u != nil {
			return u
		}
	}
	if m := brewPath.FindStringSubmatch(slash); m != nil {
		if !contains(spec.brew, m[2]) {
			return nil
		}
		prefix := slash[:strings.Index(slash, m[0])]
		brew := filepath.FromSlash(prefix + "/bin/brew")
		if !isFile(brew) {
			if brew, err = exec.LookPath("brew"); err != nil {
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
	return nil
}

// fromNodeModules is the updater of a CLI the package pkg installed in a
// global node_modules: the binary leads there (npm's and bun's links), or
// is a shim that names it (npm's .cmd on Windows, pnpm's scripts).
func fromNodeModules(pkg, bin, real string) *updater {
	mark := "/node_modules/" + pkg + "/"
	modules := ""
	if i := strings.Index(real, mark); i >= 0 {
		modules = real[:i] + "/node_modules"
	} else if shimNames(bin, pkg) {
		modules = filepath.ToSlash(filepath.Join(filepath.Dir(bin), "node_modules"))
		if !isDir(filepath.Join(filepath.FromSlash(modules), filepath.FromSlash(pkg))) {
			modules = "" // pnpm's shims name a store elsewhere
		}
	} else {
		return nil
	}
	lower := strings.ToLower(real + "|" + filepath.ToSlash(bin))
	switch {
	case strings.Contains(lower, "/.bun/"):
		bun := filepath.Join(filepath.Dir(bin), exe("bun"))
		if !isFile(bun) {
			var err error
			if bun, err = exec.LookPath("bun"); err != nil {
				return nil
			}
		}
		return &updater{via: "bun", pkg: pkg, cmd: []string{bun, "add", "-g", pkg + "@latest"}}
	case strings.Contains(lower, "/pnpm/") || strings.Contains(lower, "/.pnpm/"):
		pnpm, err := exec.LookPath("pnpm")
		if err != nil {
			return nil
		}
		return &updater{via: "pnpm", pkg: pkg, cmd: []string{pnpm, "add", "-g", pkg + "@latest"}}
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
	if !writable(m) {
		return nil // the system's (/usr/lib): npm would want sudo
	}
	u := &updater{via: "npm", pkg: pkg}
	if isFile(npm) {
		u.path = filepath.Dir(npm) // its own node, not another on PATH
	} else {
		var err error
		if npm, err = exec.LookPath("npm"); err != nil {
			return nil
		}
	}
	u.cmd = []string{npm, "install", "-g", "--prefix", prefix, pkg + "@latest"}
	return u
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

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

// ---- versions --------------------------------------------------------------

var versionRe = regexp.MustCompile(`(?:^|[^\w.])v?(\d+\.\d+(?:\.\d+)*(?:-[0-9A-Za-z][0-9A-Za-z.]*)?)`)

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

var versions, latests memo

// installedVersion is what the CLI at bin says its version is, asked again
// only once the binary changes.
func installedVersion(bin string) string {
	real, err := filepath.EvalSymlinks(bin)
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

// cliBin is where the agent's CLI is, "" when it has none magpie knows or
// it isn't on PATH.
func (a *Agent) cliBin() (string, cliSpec) {
	spec, ok := cliSpecs[a.ID]
	if !ok || a.WSL != "" || a.Bin == "" {
		return "", spec
	}
	bin, err := exec.LookPath(a.Bin)
	if err != nil {
		return "", spec
	}
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	return bin, spec
}

// CLI is the agent's CLI: its version, the newest, and whether magpie can
// update it, with its desktop app's version. ok is false for an agent with
// neither a CLI magpie knows nor its app.
func (a *Agent) CLI() (c CLI, ok bool) {
	c.App = a.AppVersion()
	bin, spec := a.cliBin()
	if bin == "" {
		return c, c.App != ""
	}
	c.Version = installedVersion(bin)
	u := howInstalled(spec, bin)
	if u == nil {
		return c, true
	}
	c.Via, c.Command = u.via, u.shown()
	c.Latest = latestVersion(u)
	c.Update = c.Version != "" && c.Latest != "" && Newer(c.Latest, c.Version)
	return c, true
}

// InstalledVersion is what the agent's CLI on PATH says its version is: ""
// for an agent without a CLI magpie knows, one not on PATH, or one that
// didn't say. Asked once per binary, like CLI's; nothing is fetched.
func (a *Agent) InstalledVersion() string {
	bin, _ := a.cliBin()
	if bin == "" {
		return ""
	}
	return installedVersion(bin)
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

// runUpdate runs an update with nothing to answer it; a var so tests never
// run a real one.
var runUpdate = func(ctx context.Context, u *updater) ([]byte, error) {
	cmd := proc.CommandContext(ctx, u.cmd[0], u.cmd[1:]...)
	cmd.Stdin = nil
	cmd.Dir, _ = os.UserHomeDir()
	env := netproxy.Env(os.Environ())
	if u.path != "" {
		for i, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
				env[i] = k + "=" + u.path + string(os.PathListSeparator) + v
			}
		}
	}
	cmd.Env = append(env, "NONINTERACTIVE=1", "HOMEBREW_NO_ENV_HINTS=1", "NO_COLOR=1")
	return cmd.CombinedOutput()
}

// updateTimeout bounds an update: a download, or Homebrew updating itself first.
var updateTimeout = 10 * time.Minute

// UpdateCLI brings the agent's CLI up to date the way it was installed, and
// says what it is afterwards.
func (a *Agent) UpdateCLI() (CLI, error) {
	bin, spec := a.cliBin()
	if bin == "" {
		return CLI{}, fmt.Errorf("magpie can't find %s's CLI", a.Name)
	}
	u := howInstalled(spec, bin)
	if u == nil {
		return CLI{}, fmt.Errorf("magpie can't tell how %s was installed at %s — update it the way you installed it", a.Name, bin)
	}
	if _, busy := updating.LoadOrStore(a.ID, true); busy {
		return CLI{}, fmt.Errorf("%s is already being updated", a.Name)
	}
	defer updating.Delete(a.ID)
	before := installedVersion(bin)
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	out, err := runUpdate(ctx, u)
	versions.forget(bin)
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
	if c.Update && c.Version == before {
		return c, fmt.Errorf("%s is still %s after %s; %s is out", a.Name, c.Version, u.shown(), c.Latest)
	}
	return c, nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

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
