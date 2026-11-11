package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestNewerVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"2.1.3", "2.1.2", true},
		{"2.1.2", "2.1.3", false},
		{"2.1.3", "2.1.3", false},
		{"0.10.0", "0.9.9", true},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0", true},
		{"v1.2.0", "1.1.9", true},
		{"1.0.89", "1.0.87-0", true},
		{"1.0.87", "1.0.87-0", true},
		{"1.0.87-0", "1.0.87", false},
		{"1.0.87-beta.10", "1.0.87-beta.9", true},
		{"1.0.87-beta.2", "1.0.87-alpha.9", true},
		{"0.74.0", "0.3.0", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// InstalledVersion asks the CLI on PATH itself (the library tells Pi 0.99's
// own MCP by it), again once the binary changes, and "" when there is none.
func TestInstalledVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script for pi")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	a := &Agent{ID: "pi", Bin: "pi"}
	if v := a.InstalledVersion(); v != "" {
		t.Errorf("none on PATH: %q", v)
	}
	p := filepath.Join(bin, "pi")
	for _, c := range []struct{ out, want string }{{"0.98.2", "0.98.2"}, {"pi 0.99.0\n", "0.99.0"}} {
		testenv.Program(t, p, "#!/bin/sh\necho '"+c.out+"'\n")
		if v := a.InstalledVersion(); v != c.want {
			t.Errorf("%q: %q", c.out, v)
		}
	}
	if v := (&Agent{ID: "unknown-cli", Bin: "pi"}).InstalledVersion(); v != "" {
		t.Errorf("a CLI magpie doesn't know: %q", v)
	}
}

func TestParseCLIVersion(t *testing.T) {
	for out, want := range map[string]string{
		"2.1.284 (Claude Code)\n": "2.1.284",
		"codex-cli 0.155.1":       "0.155.1",
		"0.61.0":                  "0.61.0",
		"GitHub Copilot CLI 1.0.87-0.\nRun 'copilot update' to check for updates.": "1.0.87-0",
		"crush version v0.3.0":  "0.3.0",
		"omp/16.3.5":            "16.3.5",
		"\x1b[1m1.18.32\x1b[0m": "1.18.32",
		"command not found":     "",
		"":                      "",
	} {
		if got := parseVersion(out); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", out, got, want)
		}
	}
}

// file makes a file (and its folders) in a temp tree, executable.
func file(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(body, "#!") {
		testenv.Program(t, path, body)
	} else if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// link makes a symlink at path to target, as npm, bun, Homebrew and the
// vendors' installers do.
func link(t *testing.T, target, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHowInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks; the Windows shim is TestHowInstalledWindowsShim")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir()) // /var is /private/var on a Mac
	// no brew, npm, bun or pnpm of the machine's: only the fakes here
	fake := filepath.Join(tmp, "fakebin")
	t.Setenv("PATH", fake)

	// npm, in a prefix of its own with its own npm
	prefix := filepath.Join(tmp, "node")
	js := file(t, filepath.Join(prefix, "lib/node_modules/@google/gemini-cli/bundle/gemini.js"), "#!/bin/sh\n")
	npm := file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\n")
	gem := link(t, "../lib/node_modules/@google/gemini-cli/bundle/gemini.js", filepath.Join(prefix, "bin/gemini"))
	_ = js
	u := howInstalled(cliSpecs["gemini"], gem)
	if u == nil || u.via != "npm" || u.path != filepath.Join(prefix, "bin") {
		t.Fatalf("npm: %+v", u)
	}
	if want := []string{npm, "install", "-g", "--prefix", prefix, "@google/gemini-cli@latest"}; !reflect.DeepEqual(u.cmd, want) {
		t.Errorf("npm: %q, want %q", u.cmd, want)
	}
	if u.shown() != "npm install -g --prefix "+prefix+" @google/gemini-cli@latest" {
		t.Errorf("npm shown: %q", u.shown())
	}
	// another agent's package in the same place is not the agent's
	if u := howInstalled(cliSpecs["codex"], gem); u != nil {
		t.Errorf("gemini's package taken for codex: %+v", u)
	}
	atomcodePkg := "@atomgit.com/atomcode"
	file(t, filepath.Join(prefix, "lib/node_modules", atomcodePkg, "bin/atomcode.js"), "")
	atomcodeBin := link(t, "../lib/node_modules/"+atomcodePkg+"/bin/atomcode.js", filepath.Join(prefix, "bin/atomcode"))
	if u := howInstalled(cliSpecs["atomcode"], atomcodeBin); u == nil || u.via != "npm" || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", prefix, atomcodePkg + "@latest"}) {
		t.Errorf("AtomCode npm: %+v", u)
	}
	// npm with no npm of its own takes the one on PATH, or none
	prefix2 := filepath.Join(tmp, "node2")
	file(t, filepath.Join(prefix2, "lib/node_modules/@github/copilot/npm-loader.js"), "")
	cop := link(t, "../lib/node_modules/@github/copilot/npm-loader.js", filepath.Join(prefix2, "bin/copilot"))
	if u := howInstalled(cliSpecs["copilot"], cop); u != nil {
		t.Errorf("no npm anywhere, yet: %+v", u)
	}
	pathNpm := file(t, filepath.Join(fake, "npm"), "#!/bin/sh\n")
	if u := howInstalled(cliSpecs["copilot"], cop); u == nil || u.cmd[0] != pathNpm || u.path != "" {
		t.Errorf("npm on PATH: %+v", u)
	}
	// a node_modules that isn't a global prefix's (…/lib/node_modules)
	file(t, filepath.Join(tmp, "proj/node_modules/cline/bin/cline.js"), "")
	if u := howInstalled(cliSpecs["cline"], link(t, filepath.Join(tmp, "proj/node_modules/cline/bin/cline.js"), filepath.Join(tmp, "proj/bin/cline"))); u != nil {
		t.Errorf("a project's node_modules: %+v", u)
	}
	// one this user can't write to (the system's) is left alone
	if os.Getuid() != 0 {
		sys := filepath.Join(tmp, "usr")
		file(t, filepath.Join(sys, "lib/node_modules/cline/bin/cline.js"), "")
		cl := link(t, "../lib/node_modules/cline/bin/cline.js", filepath.Join(sys, "bin/cline"))
		os.Chmod(filepath.Join(sys, "lib/node_modules"), 0o555)
		t.Cleanup(func() { os.Chmod(filepath.Join(sys, "lib/node_modules"), 0o755) })
		if u := howInstalled(cliSpecs["cline"], cl); u != nil {
			t.Errorf("a read-only prefix: %+v", u)
		}
	}

	// bun's global, with bun beside the link
	home := filepath.Join(tmp, "home")
	file(t, filepath.Join(home, ".bun/install/global/node_modules/@oh-my-pi/pi-coding-agent/dist/cli.js"), "")
	bun := file(t, filepath.Join(home, ".bun/bin/bun"), "")
	omp := link(t, "../install/global/node_modules/@oh-my-pi/pi-coding-agent/dist/cli.js", filepath.Join(home, ".bun/bin/omp"))
	if u := howInstalled(cliSpecs["omp"], omp); u == nil || !reflect.DeepEqual(u.cmd, []string{bun, "add", "-g", "@oh-my-pi/pi-coding-agent@latest"}) {
		t.Errorf("bun: %+v", u)
	}

	// pnpm's shim, a script naming its store
	shim := file(t, filepath.Join(home, "Library/pnpm/claude"), "#!/bin/sh\nexec node \""+home+"/Library/pnpm/global/5/.pnpm/@anthropic-ai+claude-code@2.1.0/node_modules/@anthropic-ai/claude-code/cli.js\" \"$@\"\n")
	if u := howInstalled(cliSpecs["claude"], shim); u != nil {
		t.Errorf("pnpm without pnpm: %+v", u)
	}
	pnpm := file(t, filepath.Join(fake, "pnpm"), "#!/bin/sh\n")
	if u := howInstalled(cliSpecs["claude"], shim); u == nil || !reflect.DeepEqual(u.cmd, []string{pnpm, "add", "-g", "--allow-build=@anthropic-ai/claude-code", "@anthropic-ai/claude-code@latest"}) {
		t.Errorf("pnpm: %+v", u)
	}

	// Homebrew: a formula, a cask, and a formula not the agent's
	brewDir := filepath.Join(tmp, "homebrew")
	brew := file(t, filepath.Join(brewDir, "bin/brew"), "#!/bin/sh\n")
	file(t, filepath.Join(brewDir, "Cellar/crush/0.3.0/bin/crush"), "")
	crush := link(t, "../Cellar/crush/0.3.0/bin/crush", filepath.Join(brewDir, "bin/crush"))
	if u := howInstalled(cliSpecs["crush"], crush); u == nil || u.via != "brew" || u.cask || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", "crush"}) {
		t.Errorf("brew formula: %+v", u)
	}
	file(t, filepath.Join(brewDir, "Caskroom/claude-code/2.1.0/claude"), "")
	cc := link(t, "../Caskroom/claude-code/2.1.0/claude", filepath.Join(brewDir, "bin/claude"))
	if u := howInstalled(cliSpecs["claude"], cc); u == nil || !u.cask || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", "--cask", "claude-code"}) {
		t.Errorf("brew cask: %+v", u)
	}
	file(t, filepath.Join(brewDir, "Caskroom/atomcode/5.2.1/atomcode"), "")
	atomcode := link(t, "../Caskroom/atomcode/5.2.1/atomcode", filepath.Join(brewDir, "bin/atomcode"))
	if u := howInstalled(cliSpecs["atomcode"], atomcode); u == nil || !u.cask || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", "--cask", "atomcode"}) {
		t.Errorf("AtomCode brew cask: %+v", u)
	}
	// Node formulae keep the npm package in their keg, as brew's gemini-cli
	// 0.35.3 does on this Mac: bin/gemini → ../Cellar/gemini-cli/0.35.3/
	// bin/gemini → ../libexec/bin/gemini → ../lib/node_modules/
	// @google/gemini-cli/bundle/gemini.js. That is brew's, not npm's: npm
	// install -g --prefix …/libexec would change the keg behind brew's back.
	for _, k := range []struct{ id, formula, pkg, js, name string }{
		{"gemini", "gemini-cli", "@google/gemini-cli", "bundle/gemini.js", "gemini"},
		{"pi", "pi-coding-agent", "@earendil-works/pi-coding-agent", "dist/cli.js", "pi"},
		{"cline", "cline", "cline", "bin/cline.js", "cline"},
	} {
		keg := filepath.Join(brewDir, "Cellar", k.formula, "1.0.0")
		file(t, filepath.Join(keg, "libexec/lib/node_modules", k.pkg, k.js), "")
		link(t, "../lib/node_modules/"+k.pkg+"/"+k.js, filepath.Join(keg, "libexec/bin", k.name))
		link(t, "../libexec/bin/"+k.name, filepath.Join(keg, "bin", k.name))
		bin := link(t, "../Cellar/"+k.formula+"/1.0.0/bin/"+k.name, filepath.Join(brewDir, "bin", k.name))
		if u := howInstalled(cliSpecs[k.id], bin); u == nil || u.via != "brew" || u.cask || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", k.formula}) {
			t.Errorf("%s's Node formula %s: %+v", k.id, k.formula, u)
		}
	}
	// npm under a keg-only node's Cellar (node@20) is still npm's
	nodeKeg := filepath.Join(brewDir, "Cellar/node@20/20.19.0")
	file(t, filepath.Join(nodeKeg, "lib/node_modules/@github/copilot/npm-loader.js"), "")
	file(t, filepath.Join(nodeKeg, "bin/npm"), "#!/bin/sh\n")
	if u := howInstalled(cliSpecs["copilot"], link(t, "../lib/node_modules/@github/copilot/npm-loader.js", filepath.Join(nodeKeg, "bin/copilot"))); u == nil || u.via != "npm" {
		t.Errorf("npm under node@20's keg: %+v", u)
	}
	// Grok Build's cask: bin/grok → Caskroom/grok-build/1.0.52/grok-1.0.52-macos-aarch64
	file(t, filepath.Join(brewDir, "Caskroom/grok-build/1.0.52/grok-1.0.52-macos-aarch64"), "")
	grokBin := link(t, "../Caskroom/grok-build/1.0.52/grok-1.0.52-macos-aarch64", filepath.Join(brewDir, "bin/grok"))
	if u := howInstalled(cliSpecs["grok"], grokBin); u == nil || !u.cask || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", "--cask", "grok-build"}) {
		t.Errorf("Grok Build's cask: %+v", u)
	}
	os.Remove(filepath.Join(brewDir, "bin/gemini"))
	file(t, filepath.Join(brewDir, "Cellar/something/1.0/bin/gemini"), "")
	if u := howInstalled(cliSpecs["gemini"], link(t, "../Cellar/something/1.0/bin/gemini", filepath.Join(brewDir, "bin/gemini"))); u != nil {
		t.Errorf("another formula: %+v", u)
	}

	// the vendors' own installers, updated with the CLI's own updater
	file(t, filepath.Join(home, ".local/share/claude/versions/2.1.284"), "")
	claude := link(t, filepath.Join(home, ".local/share/claude/versions/2.1.284"), filepath.Join(home, ".local/bin/claude"))
	if u := howInstalled(cliSpecs["claude"], claude); u == nil || u.via != "self" || u.pkg != "@anthropic-ai/claude-code" || !reflect.DeepEqual(u.cmd, []string{claude, "update"}) {
		t.Errorf("claude's installer: %+v", u)
	}
	file(t, filepath.Join(home, ".codex/packages/standalone/releases/0.155.1-aarch64-apple-darwin/bin/codex"), "")
	codex := link(t, filepath.Join(home, ".codex/packages/standalone/releases/0.155.1-aarch64-apple-darwin/bin/codex"), filepath.Join(home, ".local/bin/codex"))
	if u := howInstalled(cliSpecs["codex"], codex); u == nil || u.via != "self" || !reflect.DeepEqual(u.cmd, []string{codex, "update"}) {
		t.Errorf("codex's installer: %+v", u)
	}
	oc := file(t, filepath.Join(home, ".opencode/bin/opencode"), "")
	if u := howInstalled(cliSpecs["opencode"], oc); u == nil || u.shown() != "opencode upgrade" {
		t.Errorf("opencode's installer: %+v", u)
	}
	// DevEco Code's install: ~/.local/bin/deveco → its versions folder
	file(t, filepath.Join(home, ".local/share/deveco/versions/0.2.3/deveco"), "")
	deveco := link(t, filepath.Join(home, ".local/share/deveco/versions/0.2.3/deveco"), filepath.Join(home, ".local/bin/deveco"))
	if u := howInstalled(cliSpecs["deveco"], deveco); u == nil || u.via != "self" || u.pkg != "@deveco/deveco-code" || !reflect.DeepEqual(u.cmd, []string{deveco, "upgrade"}) {
		t.Errorf("deveco's installer: %+v", u)
	}

	// a binary that says nothing of how it got there: the version only
	if u := howInstalled(cliSpecs["claude"], file(t, filepath.Join(tmp, "opt/bin/claude"), "\x7fELF")); u != nil {
		t.Errorf("unknown: %+v", u)
	}
	// an agent with no updater of its own, from its vendor's folder
	if u := howInstalled(cliSpecs["gemini"], file(t, filepath.Join(home, ".local/bin/gemini"), "")); u != nil {
		t.Errorf("gemini in ~/.local/bin: %+v", u)
	}
}

// npm's shim on Windows is a .cmd beside node_modules, naming it.
func TestHowInstalledWindowsShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows' npm layout")
	}
	dir := filepath.Join(t.TempDir(), "npm")
	file(t, filepath.Join(dir, `node_modules\@anthropic-ai\claude-code\cli.js`), "")
	npm := file(t, filepath.Join(dir, "npm.cmd"), "")
	shim := file(t, filepath.Join(dir, "claude.cmd"), "@ECHO off\r\n\"%dp0%\\node_modules\\@anthropic-ai\\claude-code\\cli.js\" %*\r\n")
	u := howInstalled(cliSpecs["claude"], shim)
	if u == nil || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", dir, "@anthropic-ai/claude-code@latest"}) {
		t.Fatalf("windows shim: %+v", u)
	}
	// the native claude.exe in ~/.local/bin updates itself
	exe := file(t, filepath.Join(t.TempDir(), `.local\bin\claude.exe`), "MZ")
	if u := howInstalled(cliSpecs["claude"], exe); u == nil || u.via != "self" {
		t.Errorf("claude.exe: %+v", u)
	}
}

// The whole of it with a fake CLI, a fake registry and a fake update:
// nothing real is installed or updated.
func TestUpdateCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as the CLI")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	prefix := filepath.Join(tmp, "node")
	verFile := filepath.Join(tmp, "version")
	os.WriteFile(verFile, []byte("0.60.0\n"), 0o644)
	file(t, filepath.Join(prefix, "lib/node_modules/@google/gemini-cli/bundle/gemini.js"), "#!/bin/sh\n/bin/cat "+verFile+"\n")
	file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\nexit 1\n")
	link(t, "../lib/node_modules/@google/gemini-cli/bundle/gemini.js", filepath.Join(prefix, "bin/gemini"))
	t.Setenv("PATH", filepath.Join(prefix, "bin"))

	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@google/gemini-cli/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"name":"@google/gemini-cli","version":"0.61.0"}`))
	}))
	defer reg.Close()
	oldReg, oldRun := npmRegistry, runUpdate
	npmRegistry = reg.URL + "/"
	versions, latests = memo{}, memo{}
	var ran [][]string
	bump := "0.61.0"
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, u.cmd)
		os.WriteFile(verFile, []byte(bump+"\n"), 0o644)
		return []byte("added 1 package"), nil
	}
	t.Cleanup(func() { npmRegistry, runUpdate = oldReg, oldRun; versions, latests = memo{}, memo{} })

	a := &Agent{ID: "gemini", Name: "Gemini CLI", Bin: "gemini"}
	c, ok := a.CLI()
	if !ok || c.Version != "0.60.0" || c.Latest != "0.61.0" || !c.Update || c.Via != "npm" {
		t.Fatalf("before: %+v", c)
	}
	c, err := a.UpdateCLI()
	if err != nil || c.Version != "0.61.0" || c.Update {
		t.Fatalf("after: %+v, %v", c, err)
	}
	if len(ran) != 1 || !strings.HasSuffix(strings.Join(ran[0], " "), "install -g --prefix "+prefix+" @google/gemini-cli@latest") {
		t.Errorf("ran %q", ran)
	}

	// an update that leaves it as it was says so
	os.WriteFile(verFile, []byte("0.59.0\n"), 0o644)
	versions.forget(filepath.Join(prefix, "bin/gemini"))
	bump = "0.59.0"
	if _, err := a.UpdateCLI(); err == nil || !strings.Contains(err.Error(), "still 0.59.0") {
		t.Errorf("no change: %v", err)
	}

	// an agent magpie has no CLI for, or one in WSL, has nothing
	if _, ok := (&Agent{ID: "alma"}).CLI(); ok {
		t.Error("alma has a CLI")
	}
	if _, ok := (&Agent{ID: "codex", Bin: "codex", WSL: "Ubuntu"}).CLI(); ok {
		t.Error("a WSL codex has a CLI here")
	}
}

// An opencode 1.19.0's upgrade asks GitHub's API for the newest release,
// which answers an address 60 times an hour without a token before a 403
// (ggbdpq: "opencode upgrade: ... 403 GET https://api.github.com/repos/
// anomalyco/opencode/releases/latest"). magpie gives it the version the row
// promised, which its installer downloads from the release itself.
func TestUpdateCLIOpenCodeTakesVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as the CLI")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	verFile := filepath.Join(tmp, "version")
	os.WriteFile(verFile, []byte("1.19.2\n"), 0o644)
	oc := file(t, filepath.Join(tmp, "home/.opencode/bin/opencode"), "#!/bin/sh\n/bin/cat "+verFile+"\n")
	bin := link(t, oc, filepath.Join(tmp, "bin/opencode"))
	t.Setenv("PATH", filepath.Dir(bin))

	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/opencode-ai/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"name":"opencode-ai","version":"1.19.3"}`))
	}))
	defer reg.Close()
	oldReg, oldRun := npmRegistry, runUpdate
	npmRegistry = reg.URL + "/"
	versions, latests = memo{}, memo{}
	var ran [][]string
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, u.cmd)
		os.WriteFile(verFile, []byte("1.19.3\n"), 0o644)
		return nil, nil
	}
	t.Cleanup(func() { npmRegistry, runUpdate = oldReg, oldRun; versions, latests = memo{}, memo{} })

	a := &Agent{ID: "opencode", Name: "OpenCode", Bin: "opencode"}
	if c, ok := a.CLI(); !ok || c.Via != "self" || c.Version != "1.19.2" || c.Latest != "1.19.3" || !c.Update {
		t.Fatalf("before: %+v", c)
	}
	if c, err := a.UpdateCLI(); err != nil || c.Version != "1.19.3" || c.Update {
		t.Fatalf("after: %+v, %v", c, err)
	}
	if len(ran) != 1 || !reflect.DeepEqual(ran[0], []string{bin, "upgrade", "1.19.3"}) {
		t.Errorf("ran %q", ran)
	}
}

// An opencode npm installed is updated by npm as it was: the version
// opencode's own upgrade takes is never put after npm's install, where npm
// would read it as one more package to install.
func TestUpdateCLIOpenCodeByNpmTakesNoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as the CLI")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	prefix := filepath.Join(tmp, "node")
	verFile := filepath.Join(tmp, "version")
	os.WriteFile(verFile, []byte("1.19.2\n"), 0o644)
	file(t, filepath.Join(prefix, "lib/node_modules/opencode-ai/bin/opencode"), "#!/bin/sh\n/bin/cat "+verFile+"\n")
	file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\nexit 1\n")
	link(t, "../lib/node_modules/opencode-ai/bin/opencode", filepath.Join(prefix, "bin/opencode"))
	t.Setenv("PATH", filepath.Join(prefix, "bin"))

	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/opencode-ai/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"name":"opencode-ai","version":"1.19.3"}`))
	}))
	defer reg.Close()
	oldReg, oldRun := npmRegistry, runUpdate
	npmRegistry = reg.URL + "/"
	versions, latests = memo{}, memo{}
	var ran [][]string
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, u.cmd)
		os.WriteFile(verFile, []byte("1.19.3\n"), 0o644)
		return nil, nil
	}
	t.Cleanup(func() { npmRegistry, runUpdate = oldReg, oldRun; versions, latests = memo{}, memo{} })

	a := &Agent{ID: "opencode", Name: "OpenCode", Bin: "opencode"}
	if c, ok := a.CLI(); !ok || c.Via != "npm" || c.Latest != "1.19.3" || !c.Update {
		t.Fatalf("before: %+v", c)
	}
	if _, err := a.UpdateCLI(); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || !strings.HasSuffix(strings.Join(ran[0], " "), "install -g --prefix "+prefix+" --allow-scripts=opencode-ai opencode-ai@latest") {
		t.Errorf("ran %q", ran)
	}
}

// An agent's CLI installed with bun is updated by bun add -g, which can't
// use a SOCKS5 proxy (#1409): it is given the bridge in front of it. npm,
// which can, is given the proxy as it is.
func TestUpdateWithBunBridgesSOCKS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const socks = "socks5://127.0.0.1:4401"
	names := []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}
	for _, k := range names {
		t.Setenv(k, socks)
	}
	// the proxy URLs only: NO_PROXY's hosts or Node's NODE_USE_ENV_PROXY=1,
	// set on the machine running the test, name no proxy
	proxies := func(env []string) []string {
		var out []string
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); slices.Contains(names, k) && v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	bun := proxies(updateEnv(&updater{via: "bun", cmd: []string{"bun", "add", "-g", "x@latest"}}))
	if len(bun) == 0 {
		t.Fatal("bun is given no proxy")
	}
	for _, v := range bun {
		if !strings.HasPrefix(v, "http://127.0.0.1:") {
			t.Fatalf("bun is given %s", v)
		}
	}
	for _, v := range proxies(updateEnv(&updater{via: "npm", cmd: []string{"npm", "i", "-g", "x@latest"}})) {
		if v != socks {
			t.Fatalf("npm is given %s", v)
		}
	}
}

// Grok Build from xAI's installer (xiaoxiaofeixz on Discord: Grok at
// C:\Users\<user>\.grok\bin\grok.exe showed no newest version and no
// update): install.sh's ~/.grok/bin/grok links to
// ../downloads/grok-<platform>, which grok update replaces with
// ../downloads/grok-<version>-<platform>; install.ps1 copies grok.exe into
// ~\.grok\bin, and grok update copies the new one over it. The CLI is read
// from there, not PATH, where grok may be Homebrew's regular-expression tool.
func TestGrokInstalled(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_HOME", "")
	other := t.TempDir() // another grok on PATH
	file(t, filepath.Join(other, exe("grok")), "MZ")
	t.Setenv("PATH", other)

	a := &Agent{ID: "grok", Name: "Grok Build"} // no Bin, as grok(home)
	if _, ok := a.CLI(); ok {
		t.Fatal("PATH's grok taken for Grok Build")
	}

	platform := "macos-aarch64"
	if runtime.GOOS == "windows" {
		platform = "windows-x86_64"
	}
	when := time.Date(2026, 10, 2, 9, 32, 0, 0, time.UTC)
	// install puts the release at the path the CLI is run by; the same bytes
	// and time for every release, as nothing but the path need tell them apart
	var bin string
	install := func(version string) {
		t.Helper()
		if runtime.GOOS == "windows" {
			bin = filepath.Join(home, `.grok\bin\grok.exe`)
			os.Remove(bin)
			file(t, bin, "MZ")
			os.Chtimes(bin, when, when)
			return
		}
		name := "grok-" + platform
		if version != "" {
			name = "grok-" + version + "-" + platform
		}
		os.Chtimes(file(t, filepath.Join(home, ".grok/downloads", name), "not a program"), when, when)
		bin = filepath.Join(home, ".grok/bin/grok")
		os.Remove(bin)
		link(t, "../downloads/"+name, bin)
	}
	install("")

	served := map[string]string{"/stable": "1.0.50\n", "/alpha": "1.0.51-alpha.2\n"}
	rel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(v))
	}))
	defer rel.Close()
	says := "grok 1.0.46 (2765805b9442)"
	oldRel, oldVer, oldRun := grokReleases, runVersion, runUpdate
	grokReleases = []string{rel.URL + "/"}
	runVersion = func(string) string { return says }
	versions, latests = memo{}, memo{}
	var ran [][]string
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, u.cmd)
		install("1.0.50")
		says = "grok 1.0.50 (c58f321264ba)"
		return []byte("✓ grok v1.0.50 installed successfully!"), nil
	}
	t.Cleanup(func() {
		grokReleases, runVersion, runUpdate = oldRel, oldVer, oldRun
		versions, latests = memo{}, memo{}
	})

	c, ok := a.CLI()
	if !ok || c.Version != "1.0.46" || c.Latest != "1.0.50" || !c.Update || c.Via != "self" || c.Command != exe("grok")+" update" {
		t.Fatalf("before: %+v %v", c, ok)
	}
	if c, err := a.UpdateCLI(); err != nil || c.Version != "1.0.50" || c.Update {
		t.Fatalf("after: %+v, %v", c, err)
	}
	if len(ran) != 1 || !reflect.DeepEqual(ran[0], []string{bin, "update"}) {
		t.Errorf("ran %q", ran)
	}

	// grok's own update at launch (auto_update), not through magpie: the
	// version is asked again
	install("1.0.51")
	says = "grok 1.0.51 (0a1b2c3d4e5f)"
	if runtime.GOOS == "windows" {
		later := when.Add(time.Hour) // the copy of a new download
		os.Chtimes(bin, later, later)
	}
	if v := a.InstalledVersion(); v != "1.0.51" {
		t.Errorf("after grok's own update: %q", v)
	}

	// on the alpha channel (grok update --alpha), the newest is alpha's
	file(t, filepath.Join(home, ".grok", "config.toml"), "[cli]\ninstaller = \"internal\"\nchannel = \"alpha\"\n")
	if c, _ := a.CLI(); c.Latest != "1.0.51-alpha.2" {
		t.Errorf("alpha: %+v", c)
	}
}

// Grok Build from Homebrew's grok-build cask (Will on Discord): grok on PATH
// leads into Caskroom/grok-build, with no ~/.grok/bin/grok. It is found,
// detected before grok first runs, and updated with brew; Homebrew's grok
// formula, a regular-expression tool, never is.
func TestGrokBrewCask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Homebrew's links")
	}
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", "")
	brewDir := filepath.Join(home, "homebrew")
	t.Setenv("PATH", filepath.Join(brewDir, "bin"))
	file(t, filepath.Join(brewDir, "bin/brew"), "#!/bin/sh\n")

	oldVer, oldLatest := runVersion, brewLatest
	says := "grok 1.0.46 (2765805b9442)"
	runVersion = func(string) string { return says }
	brewLatest = func(u *updater) (string, error) { return "1.0.52", nil }
	versions, latests = memo{}, memo{}
	t.Cleanup(func() {
		runVersion, brewLatest = oldVer, oldLatest
		versions, latests = memo{}, memo{}
	})

	// the regular-expression tool's grok, the formula of that name
	file(t, filepath.Join(brewDir, "Cellar/grok/0.9.2/bin/grok"), "")
	link(t, "../Cellar/grok/0.9.2/bin/grok", filepath.Join(brewDir, "bin/grok"))
	a := grok(home)
	if a.Detected() {
		t.Error("Homebrew's grok formula detected as Grok Build")
	}
	if c, ok := a.CLI(); ok {
		t.Errorf("Homebrew's grok formula taken for Grok Build: %+v", c)
	}

	// the cask, as formulae.brew.sh/api/cask/grok-build.json has it: binary
	// grok-1.0.46-macos-aarch64, target grok
	os.Remove(filepath.Join(brewDir, "bin/grok"))
	file(t, filepath.Join(brewDir, "Caskroom/grok-build/1.0.46/grok-1.0.46-macos-aarch64"), "")
	bin := link(t, "../Caskroom/grok-build/1.0.46/grok-1.0.46-macos-aarch64", filepath.Join(brewDir, "bin/grok"))
	if !a.Detected() {
		t.Error("the grok-build cask, before grok has run, not detected")
	}
	c, ok := a.CLI()
	if !ok || c.Path != bin || c.Version != "1.0.46" || c.Via != "brew" || c.Command != "brew upgrade --cask grok-build" || c.Latest != "1.0.52" || !c.Update {
		t.Fatalf("the grok-build cask: %+v %v", c, ok)
	}

	// with an old ~/.grok/bin/grok from x.ai's installer beside it, the
	// cask PATH runs is the one shown and updated
	file(t, filepath.Join(home, ".grok/downloads/grok-macos-aarch64"), "")
	link(t, "../downloads/grok-macos-aarch64", filepath.Join(home, ".grok/bin/grok"))
	if c, _ := a.CLI(); c.Path != bin || c.Via != "brew" {
		t.Errorf("cask and installer: %+v", c)
	}
	// PATH running the installer's instead: that one, updated by grok update
	os.Remove(bin)
	if c, _ := a.CLI(); c.Path != filepath.Join(home, ".grok/bin/grok") || c.Via != "self" {
		t.Errorf("installer only: %+v", c)
	}
}

// bunxOf is a .bunx as bun 1.4.3 wrote it beside its Windows shims, on the
// Windows box after bun add -g opencode-ai @oh-my-pi/pi-coding-agent: the
// program's path from bun's folder in UTF-16, a quote, then bun's own
// bytes (omp's names bun as its runner).
func bunxOf(t *testing.T, path string, tail []byte) string {
	t.Helper()
	var b []byte
	for _, r := range path + `"` {
		b = append(b, byte(r), 0)
	}
	return string(append(append(b, 0, 0), tail...))
}

// opencode and omp bun installed on Windows are updated with bun: bun's
// shim there is an .exe naming no package, and the .bunx beside it names
// it (#2192, xiaozhu1337: opencode 2.0.24 and omp 18.6.1 with newer out,
// no update offered). omp's own -Binary install updates itself.
func TestHowInstalledBunWindowsShim(t *testing.T) {
	bunDir := filepath.Join(t.TempDir(), "bun-home") // a BUN_INSTALL with no .bun in it
	bun := file(t, filepath.Join(bunDir, "bin", exe("bun")), "MZ")
	file(t, filepath.Join(bunDir, "install/global/node_modules/opencode-ai/bin/opencode.exe"), "MZ")
	file(t, filepath.Join(bunDir, "install/global/node_modules/@oh-my-pi/pi-coding-agent/dist/cli.js"), "")
	opencode := file(t, filepath.Join(bunDir, "bin/opencode.exe"), "MZ\x90\x00 node_modules") // bun's 8 KB shim
	file(t, filepath.Join(bunDir, "bin/opencode.bunx"), bunxOf(t, `install\global\node_modules\opencode-ai\bin\opencode.exe`, []byte{0x30, 0xab}))
	omp := file(t, filepath.Join(bunDir, "bin/omp.exe"), "MZ\x90\x00 node_modules")
	file(t, filepath.Join(bunDir, "bin/omp.bunx"), bunxOf(t, `install\global\node_modules\@oh-my-pi\pi-coding-agent\dist\cli.js`,
		[]byte{'b', 0, 'u', 0, 'n', 0, ' ', 0, 0x82, 0, 0, 0, 8, 0, 0, 0, 0x35, 0xab}))
	if u := howInstalled(cliSpecs["opencode"], opencode); u == nil || u.via != "bun" || !reflect.DeepEqual(u.cmd, []string{bun, "add", "-g", "--trust", "opencode-ai@latest"}) {
		t.Errorf("opencode's bun shim: %+v", u)
	}
	if u := howInstalled(cliSpecs["omp"], omp); u == nil || u.via != "bun" || !reflect.DeepEqual(u.cmd, []string{bun, "add", "-g", "@oh-my-pi/pi-coding-agent@latest"}) {
		t.Errorf("omp's bun shim: %+v", u)
	}
	// a .bunx naming another package is not this one's
	if u := howInstalled(cliSpecs["omp"], opencode); u != nil {
		t.Errorf("opencode's shim taken as omp's: %+v", u)
	}

	// omp.sh's binary install: %LOCALAPPDATA%\omp\omp.exe, ~/.local/bin/omp
	home := t.TempDir()
	for _, p := range []string{"AppData/Local/omp/omp.exe", ".local/bin/omp"} {
		bin := file(t, filepath.Join(home, p), "MZ")
		if u := howInstalled(cliSpecs["omp"], bin); u == nil || u.via != "self" || !reflect.DeepEqual(u.cmd, []string{bin, "update"}) || u.pkg != "@oh-my-pi/pi-coding-agent" {
			t.Errorf("omp's binary at %s: %+v", p, u)
		}
	}
}
