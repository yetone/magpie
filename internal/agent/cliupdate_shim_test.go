package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// fixture is a file of testdata/mise-windows: bytes mise 2026.10.7 and
// npm 10 wrote on Windows 11 (an npm -g snow-ai under mise's node@22).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mise-windows", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func resetShims(t *testing.T) {
	shimTargets, versions = memo{}, memo{}
	t.Cleanup(func() { shimTargets, versions = memo{}, memo{} })
}

// miseNode lays out mise's node@22 as `mise use -g node@22` and
// `npm install -g snow-ai` left it on a Mac: installs/node/22 a link to
// 22.23.3, whose bin/snow leads into lib/node_modules, beside mise's own npm
// (a script that runs mise reshim after a global install).
func miseNode(t *testing.T, data string) (prefix string) {
	prefix = filepath.Join(data, "installs/node/22.23.3")
	file(t, filepath.Join(prefix, "lib/node_modules/snow-ai/bundle/cli.mjs"), "#!/bin/sh\necho 0.9.2\n")
	file(t, filepath.Join(prefix, "lib/node_modules/snow-ai/package.json"), `{"name":"snow-ai","version":"0.9.2"}`)
	file(t, filepath.Join(prefix, "bin/npm"), "#!/usr/bin/env bash\n# This script wraps npm so to run `mise reshim` after global installs and uninstalls\n")
	link(t, "../lib/node_modules/snow-ai/bundle/cli.mjs", filepath.Join(prefix, "bin/snow"))
	link(t, "./22.23.3", filepath.Join(data, "installs/node/22"))
	return prefix
}

// An agent installed with npm -g under mise's Node is found on PATH as
// mise's shim, ~/.local/share/mise/shims/snow, a link to the mise binary,
// which says nothing of npm (flp on Discord). The program it runs is asked
// of mise (mise which snow, as it answered: …/installs/node/22/bin/snow),
// and updated with that Node's own npm into its own prefix, then mise
// reshim.
func TestMiseShimIsNodesNpm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mise's shims are links to mise here; Windows' are TestMiseShimWindows")
	}
	resetShims(t)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	data := filepath.Join(home, ".local/share/mise")
	prefix := miseNode(t, data)
	asked := filepath.Join(tmp, "asked")
	// mise's which: run from the home folder, with the shim's command
	mise := file(t, filepath.Join(tmp, "mise-dl/mise"), "#!/bin/sh\necho \"$PWD $*\" >> '"+asked+"'\n"+
		"[ \"$1 $2\" = 'which snow' ] || { echo \"mise ERROR $2 is not a mise bin. Perhaps you need to install it first.\" >&2; exit 1; }\n"+
		"echo '"+filepath.Join(data, "installs/node/22/bin/snow")+"'\n")
	shims := filepath.Join(data, "shims")
	snow := link(t, mise, filepath.Join(shims, "snow"))
	link(t, mise, filepath.Join(shims, "npm"))
	t.Setenv("PATH", shims)

	real, u := installedAt(cliSpecs["snow"], snow)
	if real != filepath.Join(data, "installs/node/22/bin/snow") {
		t.Errorf("program: %q", real)
	}
	if u == nil || u.via != "npm" || u.path != filepath.Join(prefix, "bin") {
		t.Fatalf("mise's shim: %+v", u)
	}
	if want := []string{filepath.Join(prefix, "bin/npm"), "install", "-g", "--prefix", prefix, "--allow-scripts=snow-ai", "snow-ai@latest"}; !reflect.DeepEqual(u.cmd, want) {
		t.Errorf("update %q, want %q", u.cmd, want)
	}
	if want := []string{mise, "reshim"}; !reflect.DeepEqual(u.reshim, want) {
		t.Errorf("reshim %q, want %q", u.reshim, want)
	}
	if b, _ := os.ReadFile(asked); string(b) != home+" which snow\n" {
		t.Errorf("mise was asked %q", b)
	}
	// asked once, not on every look at the Agents page
	installedAt(cliSpecs["snow"], snow)
	if b, _ := os.ReadFile(asked); strings.Count(string(b), "\n") != 1 {
		t.Errorf("mise asked again: %q", b)
	}
	// its version from its package, not by running the shim
	if v := installedVersion(real); v != "0.9.2" {
		t.Errorf("version %q", v)
	}

	// a command mise has no program for: nothing offered, as before
	other := link(t, mise, filepath.Join(shims, "gemini"))
	if real, u := installedAt(cliSpecs["gemini"], other); u != nil || real != other {
		t.Errorf("mise which failing: %q %+v", real, u)
	}
	// another agent's package under the same Node is not this one's
	if _, u := installedAt(cliSpecs["cline"], snow); u != nil {
		t.Errorf("snow's package taken for cline: %+v", u)
	}
	// a program that isn't a shim is read as it is: mise not asked
	os.Remove(asked)
	direct := filepath.Join(prefix, "bin/snow")
	if real, u := installedAt(cliSpecs["snow"], direct); real != direct || u == nil || u.reshim != nil {
		t.Errorf("mise activate's PATH: %q %+v", real, u)
	}
	if _, err := os.Stat(asked); err == nil {
		t.Error("mise asked about a program that is no shim")
	}
}

// The update itself: mise's npm, then mise reshim, and the version after
// read from the program again.
func TestUpdateCLIThroughMiseShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	resetShims(t)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	data := filepath.Join(home, ".local/share/mise")
	prefix := miseNode(t, data)
	mise := file(t, filepath.Join(tmp, "bin/mise"), "#!/bin/sh\necho '"+filepath.Join(data, "installs/node/22/bin/snow")+"'\n")
	shims := filepath.Join(data, "shims")
	link(t, mise, filepath.Join(shims, "snow"))
	t.Setenv("PATH", shims)

	pkg := filepath.Join(prefix, "lib/node_modules/snow-ai/package.json")
	os.WriteFile(pkg, []byte(`{"name":"snow-ai","version":"0.9.1"}`), 0o644)
	oldReg, oldRun := npmRegistry, runUpdate
	t.Cleanup(func() { npmRegistry, runUpdate = oldReg, oldRun; latests = memo{} })
	latests = memo{}
	latests.get("npm:snow-ai", "", 1<<40, 1<<40, func() (string, error) { return "0.9.2", nil })
	var ran [][]string
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, u.cmd)
		if u.via == "npm" {
			os.WriteFile(pkg, []byte(`{"name":"snow-ai","version":"0.9.2"}`), 0o644)
		}
		return nil, nil
	}

	a := &Agent{ID: "snow", Name: "Snow CLI", Bin: "snow"}
	c, ok := a.CLI()
	if !ok || c.Version != "0.9.1" || c.Via != "npm" || !c.Update || c.Path != filepath.Join(shims, "snow") {
		t.Fatalf("before: %+v", c)
	}
	c, err := a.UpdateCLI()
	if err != nil || c.Version != "0.9.2" || c.Update {
		t.Fatalf("after: %+v, %v", c, err)
	}
	want := [][]string{{filepath.Join(prefix, "bin/npm"), "install", "-g", "--prefix", prefix, "--allow-scripts=snow-ai", "snow-ai@latest"}, {mise, "reshim"}}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("ran %q, want %q", ran, want)
	}
}

// asdf's shim is a script of its own, as asdf 0.20.2's reshim wrote it; the
// program is asked of asdf which. asdf-nodejs's npm reshims by itself.
func TestAsdfShimIsNodesNpm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asdf has no Windows build")
	}
	resetShims(t)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	prefix := filepath.Join(home, ".asdf/installs/nodejs/22.0.0")
	file(t, filepath.Join(prefix, "lib/node_modules/snow-ai/bundle/cli.mjs"), "#!/bin/sh\n")
	npm := file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\n")
	link(t, "../lib/node_modules/snow-ai/bundle/cli.mjs", filepath.Join(prefix, "bin/snow"))
	shims := filepath.Join(home, ".asdf/shims")
	snow := filepath.Join(shims, "snow")
	testenv.Program(t, snow, "#!/usr/bin/env bash\n# asdf-plugin: nodejs 22.0.0\nexec asdf exec \"snow\" \"$@\"")
	fake := filepath.Join(tmp, "asdf-dl")
	t.Setenv("PATH", shims+string(os.PathListSeparator)+fake)
	if _, u := installedAt(cliSpecs["snow"], snow); u != nil {
		t.Errorf("no asdf to ask, yet: %+v", u)
	}
	resetShims(t)
	file(t, filepath.Join(fake, "asdf"), "#!/bin/sh\n[ \"$1 $2\" = 'which snow' ] && echo '"+filepath.Join(prefix, "bin/snow")+"'\n")
	real, u := installedAt(cliSpecs["snow"], snow)
	if real != filepath.Join(prefix, "bin/snow") || u == nil || u.reshim != nil {
		t.Fatalf("asdf's shim: %q %+v", real, u)
	}
	if want := []string{npm, "install", "-g", "--prefix", prefix, "--allow-scripts=snow-ai", "snow-ai@latest"}; !reflect.DeepEqual(u.cmd, want) {
		t.Errorf("update %q, want %q", u.cmd, want)
	}
}

// mise's shims on Windows, each mode as mise 2026.10.7 made them in
// %LOCALAPPDATA%\mise\shims: snow.exe, a copy of mise-shim.exe (exe, the
// default; its 277 KB aren't kept here, only .mode), snow.cmd and an
// extensionless bash script (file), or a hard link to mise.exe (hardlink).
// mise which snow names npm's extensionless sh script in the Node folder,
// beside snow.cmd, which names node_modules\snow-ai.
func TestMiseShimWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows' npm layout")
	}
	resetShims(t)
	tmp := t.TempDir()
	data := filepath.Join(tmp, `AppData\Local\mise`)
	prefix := filepath.Join(data, `installs\node\22.23.3`)
	file(t, filepath.Join(prefix, `node_modules\snow-ai\bundle\cli.mjs`), "")
	npm := file(t, filepath.Join(prefix, "npm.cmd"), "")
	file(t, filepath.Join(prefix, "snow"), fixture(t, "node/snow"))
	file(t, filepath.Join(prefix, "snow.cmd"), fixture(t, "node/snow.cmd"))
	mise := file(t, filepath.Join(tmp, `mise\bin\mise.exe`), "MZ")
	t.Setenv("PATH", filepath.Dir(mise))
	var asked []string
	was := shimWhich
	shimWhich = func(s *shim) (string, error) {
		asked = append(asked, s.mgr+" which "+s.name)
		return filepath.Join(prefix, "snow") + "\r\n", nil
	}
	t.Cleanup(func() { shimWhich = was })

	shims := filepath.Join(data, "shims")
	for _, c := range []struct {
		mode string
		bins map[string]string
		bin  string
	}{
		{"exe", map[string]string{".mode": fixture(t, "shims-exe/.mode"), "snow.exe": "MZ"}, "snow.exe"},
		{"file", map[string]string{".mode": fixture(t, "shims-file/.mode"), "snow.cmd": fixture(t, "shims-file/snow.cmd"), "snow": fixture(t, "shims-file/snow")}, "snow.cmd"},
		{"hardlink", map[string]string{".mode": "hardlink"}, "snow.exe"},
	} {
		os.RemoveAll(shims)
		for n, b := range c.bins {
			file(t, filepath.Join(shims, n), b)
		}
		if c.mode == "hardlink" {
			if err := os.Link(mise, filepath.Join(shims, "snow.exe")); err != nil {
				t.Fatal(err)
			}
		}
		resetShims(t)
		asked = nil
		real, u := installedAt(cliSpecs["snow"], filepath.Join(shims, c.bin))
		if real != filepath.Join(prefix, "snow.cmd") {
			t.Errorf("%s: program %q", c.mode, real)
		}
		if u == nil || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", prefix, "--allow-scripts=snow-ai", "snow-ai@latest"}) || u.path != prefix {
			t.Errorf("%s: %+v", c.mode, u)
			continue
		}
		if !reflect.DeepEqual(u.reshim, []string{mise, "reshim"}) {
			t.Errorf("%s: reshim %q", c.mode, u.reshim)
		}
		if want := []string{mise + " which snow"}; !reflect.DeepEqual(asked, want) {
			t.Errorf("%s: asked %q", c.mode, asked)
		}
	}

	// a folder named shims that isn't mise's is nobody's shim
	other := file(t, filepath.Join(tmp, `tools\shims\snow.cmd`), "@echo off\r\n")
	if s := shimAt(other); s != nil {
		t.Errorf("another shims folder: %+v", s)
	}
}

// TestMiseShimWindowsFiveAgents is flp's own layout: node 24.20.0 under
// mise, `where claude` finding installs\node\24.20.0\claude (npm's sh
// script), claude.cmd beside it and shims\claude.exe, and `mise which
// claude` naming the extensionless script. magpie, started without mise's
// activation, finds shims\<cmd>.exe; each agent npm installs this way is
// read through npm's claude.cmd (whose --version is its version) and
// updated with that Node's npm.cmd, then mise reshim.
func TestMiseShimWindowsFiveAgents(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows' npm layout")
	}
	resetShims(t)
	tmp := t.TempDir()
	data := filepath.Join(tmp, `Users\flp\AppData\Local\mise`)
	prefix := filepath.Join(data, `installs\node\24.20.0`)
	npm := file(t, filepath.Join(prefix, "npm.cmd"), "")
	mise := file(t, filepath.Join(tmp, `mise\bin\mise.exe`), "MZ")
	t.Setenv("PATH", filepath.Dir(mise))
	shims := filepath.Join(data, "shims")
	file(t, filepath.Join(shims, ".mode"), fixture(t, "shims-exe/.mode"))
	var asked []string
	was := shimWhich
	shimWhich = func(s *shim) (string, error) {
		asked = append(asked, s.name)
		return filepath.Join(prefix, s.name) + "\r\n", nil
	}
	t.Cleanup(func() { shimWhich = was })
	// npm's own scripts for snow-ai, naming each package's bin script
	sh, cmdShim := fixture(t, "node/snow"), fixture(t, "node/snow.cmd")
	for _, c := range []struct{ id, cmd, pkg, script string }{
		{"claude", "claude", "@anthropic-ai/claude-code", `@anthropic-ai\claude-code\cli.js`},
		{"pi", "pi", "@earendil-works/pi-coding-agent", `@earendil-works\pi-coding-agent\dist\cli.js`},
		{"codex", "codex", "@openai/codex", `@openai\codex\bin\codex.js`},
		{"opencode", "opencode", "opencode-ai", `opencode-ai\bin\opencode`},
		{"codebuddy", "codebuddy", "@tencent-ai/codebuddy-code", `@tencent-ai\codebuddy-code\bin\codebuddy`},
	} {
		file(t, filepath.Join(prefix, "node_modules", c.script), "")
		file(t, filepath.Join(prefix, "node_modules", filepath.FromSlash(c.pkg), "package.json"), `{"name":"`+c.pkg+`","version":"1.2.3"}`)
		file(t, filepath.Join(prefix, c.cmd), strings.ReplaceAll(sh, "snow-ai/bundle/cli.mjs", filepath.ToSlash(c.script)))
		cmd := file(t, filepath.Join(prefix, c.cmd+".cmd"), strings.ReplaceAll(cmdShim, `snow-ai\bundle\cli.mjs`, c.script))
		shim := file(t, filepath.Join(shims, c.cmd+".exe"), "MZ")
		asked = nil
		real, u := installedAt(cliSpecs[c.id], shim)
		if !reflect.DeepEqual(asked, []string{c.cmd}) {
			t.Errorf("%s: asked mise of %q", c.id, asked)
		}
		if real != cmd {
			t.Errorf("%s: program %q", c.id, real)
		}
		if u == nil || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", prefix, c.pkg + "@latest"}) {
			t.Errorf("%s: %+v", c.id, u)
			continue
		}
		if !reflect.DeepEqual(u.reshim, []string{mise, "reshim"}) {
			t.Errorf("%s: reshim %q", c.id, u.reshim)
		}
	}
}
