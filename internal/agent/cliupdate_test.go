package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
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
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+c.out+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
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
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
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
	if u := howInstalled(cliSpecs["claude"], shim); u == nil || !reflect.DeepEqual(u.cmd, []string{pnpm, "add", "-g", "@anthropic-ai/claude-code@latest"}) {
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
