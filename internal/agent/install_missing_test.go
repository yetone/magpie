package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/testenv"
)

// An agent whose settings are left but whose CLI is gone (#843: dsh
// uninstalled, ~/.dsh kept) is still listed, and Install another agent
// offers it again, marked; one found where users' tools go though not on
// PATH (#839) isn't missing. Codex's folder with neither its CLI nor
// anything else that reads it is a leftover too (#2177).
func TestCLIMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	judgeOffWindows(t)
	// no desktop app either, whatever this machine has installed
	was := appFolders
	appFolders = func() []string { return nil }
	t.Cleanup(func() { appFolders = was })
	bin := "magpie-test-gone-dsh"
	dir := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dsh := &Agent{ID: "dsh", Name: "DeepSeek Harness", Icon: "deepseek-color", Bin: bin, Dir: dir, Path: filepath.Join(dir, "config.yaml")}
	codex := &Agent{ID: "codex", Name: "Codex", Bin: "magpie-test-gone-codex", Dir: dir, Path: filepath.Join(dir, "config.toml")}
	never := &Agent{ID: "crush", Name: "Crush", Bin: "magpie-test-gone-crush", Dir: filepath.Join(home, "nope"), Path: filepath.Join(home, "nope", "crush.json")}
	wsl := &Agent{ID: "dsh@wsl:Ubuntu", Name: "DeepSeek Harness", Bin: bin, WSL: "Ubuntu", Dir: dir, Path: filepath.Join(dir, "config.yaml")}
	if !dsh.Detected() || !dsh.CLIMissing() {
		t.Fatalf("dsh with ~/.dsh and no dsh: detected %v, missing %v", dsh.Detected(), dsh.CLIMissing())
	}
	if !codex.CLIMissing() {
		t.Fatal("codex's folder alone, nothing reading it, not taken for missing")
	}
	if never.CLIMissing() || wsl.CLIMissing() {
		t.Fatalf("missing: crush not here %v, wsl %v", never.CLIMissing(), wsl.CLIMissing())
	}
	got := installsOf([]*Agent{dsh, codex, never, wsl}, "darwin", true)
	ids := map[string]Install{}
	for _, x := range got {
		ids[x.ID] = x
	}
	if x, ok := ids["dsh"]; !ok || !x.Missing || len(x.Commands) != 1 || x.Commands[0].Command != "npm install -g @deepseek-ai/dsh" {
		t.Fatalf("dsh offered as %+v (all: %+v)", x, got)
	}
	if x, ok := ids["crush"]; !ok || x.Missing {
		t.Fatalf("crush, not here at all, offered as %+v", x)
	}
	if x, ok := ids["codex"]; !ok || !x.Missing {
		t.Fatalf("codex, its folder left, offered as %+v", x)
	}

	// installed in ~/.local/bin, which magpie's PATH hasn't: found
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	name := bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	testenv.Program(t, filepath.Join(local, name), "#!/bin/sh\n")
	if dsh.CLIMissing() {
		t.Fatal("dsh in ~/.local/bin taken for missing")
	}
	for _, x := range installsOf([]*Agent{dsh}, "darwin", true) {
		t.Fatalf("installed dsh offered: %+v", x)
	}
}

// noApps points the app folders at an empty folder of the test's, so
// whatever this machine has installed counts for nothing.
func noApps(t *testing.T) string {
	t.Helper()
	apps := t.TempDir()
	was := appFolders
	appFolders = func() []string { return []string{apps} }
	t.Cleanup(func() { appFolders = was })
	return apps
}

func mkdirT(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeT(t *testing.T, p, body string) {
	t.Helper()
	mkdirT(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #2173 (SkyTodInfi): Hermes uninstalled, ~/.hermes/config.yaml left with
// 37 bytes, no hermes anywhere: listed, as gone. Its own install.sh's
// checkout, or hermes in ~/.local/bin, is Hermes installed.
func TestCLIMissingHermes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	noApps(t)
	dir := filepath.Join(home, ".hermes")
	writeT(t, filepath.Join(dir, "config.yaml"), "custom_providers: []\nmcp_servers: {}\n")
	h := &Agent{ID: "hermes", Name: "Hermes Agent", Bin: "magpie-test-gone-hermes", Dir: dir, Path: filepath.Join(dir, "config.yaml")}
	if !h.Detected() || !h.CLIMissing() {
		t.Fatalf("hermes by its config.yaml alone: detected %v, missing %v", h.Detected(), h.CLIMissing())
	}
	// no install command magpie knows: not offered, though gone
	for _, x := range installsOf([]*Agent{h}, runtime.GOOS, true) {
		t.Fatalf("hermes offered: %+v", x)
	}
	mkdirT(t, filepath.Join(dir, "hermes-agent"))
	if h.CLIMissing() {
		t.Fatal("hermes with its checkout taken for missing")
	}
	// connected in the profile `hermes profile use` made sticky: the
	// checkout is in the root
	bot := filepath.Join(dir, "profiles", "coder")
	writeT(t, filepath.Join(bot, "config.yaml"), "model: {}\n")
	if p := (&Agent{ID: "hermes", Name: "Hermes Agent", Bin: h.Bin, Dir: bot, Path: filepath.Join(bot, "config.yaml")}); p.CLIMissing() {
		t.Fatal("hermes in a profile, with the root's checkout, taken for missing")
	}
	if err := os.RemoveAll(filepath.Join(dir, "hermes-agent")); err != nil {
		t.Fatal(err)
	}
	name := h.Bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	local := filepath.Join(home, ".local", "bin")
	mkdirT(t, local)
	testenv.Program(t, filepath.Join(local, name), "#!/bin/sh\n")
	if h.CLIMissing() {
		t.Fatal("hermes in ~/.local/bin taken for missing")
	}
}

// An agent whose folder isn't only its CLI's is gone only when nothing
// else that reads it is here: an editor's extension, a desktop app, an
// IDE whose agents bring their own program. Never one magpie can't tell
// about (OpenCode off a Mac, Gemini, detected its own way).
type goneCase struct {
	id      string
	folder  string
	with    func(t *testing.T, home, apps string)
	missing bool
}

func TestCLIMissingSharedFolders(t *testing.T) {
	judgeOffWindows(t)
	cases := []goneCase{
		{"claude", ".claude", nil, true},
		{"claude", ".claude", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".vscode", "extensions", "anthropic.claude-code-2.1.3-darwin-arm64"))
		}, false},
		{"claude", ".claude", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".cursor", "extensions", "anthropic.claude-code-2.1.3"))
		}, false},
		{"claude", ".claude", func(t *testing.T, home, _ string) {
			writeT(t, filepath.Join(home, ".claude", "local", exe("claude")), "")
		}, false},
		{"claude", ".claude", func(t *testing.T, home, _ string) {
			mkdirT(t, jetBrainsDirFor(home))
		}, false},
		// the Claude Code magpie downloaded, no other (Wing8Aqua on X)
		{"claude", ".claude", func(t *testing.T, _, _ string) {
			writeT(t, filepath.Join(claudecode.Root(), "2.1.400", exe("claude")), "")
		}, false},
		// a version folder without its binary is no download
		{"claude", ".claude", func(t *testing.T, _, _ string) {
			mkdirT(t, filepath.Join(claudecode.Root(), "2.1.400"))
		}, true},
		{"codex", ".codex", nil, true},
		{"codex", ".codex", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".vscode-server", "extensions", "openai.chatgpt-26.5.1"))
		}, false},
		{"codex", ".codex", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".codex", "packages", "standalone"))
		}, false},
		{"codex", ".codex", func(t *testing.T, home, _ string) {
			mkdirT(t, zedAgentsDirFor(home))
		}, false},
		// another extension's folder of the same prefix is not Codex's
		{"codex", ".codex", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt.other"))
		}, true},
		{"cline", ".cline", nil, true},
		{"cline", ".cline", func(t *testing.T, home, _ string) {
			mkdirT(t, filepath.Join(home, ".windsurf", "extensions", "saoudrizwan.claude-dev-3.40.0"))
		}, false},
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases,
			goneCase{"claude", ".claude", func(t *testing.T, _, apps string) { mkdirT(t, filepath.Join(apps, "Claude.app")) }, false},
			goneCase{"opencode", ".opencode-config", nil, true},
			goneCase{"opencode", ".opencode-config", func(t *testing.T, _, apps string) { mkdirT(t, filepath.Join(apps, "OpenCode.app")) }, false},
		)
	} else {
		cases = append(cases, goneCase{"opencode", ".opencode-config", nil, false})
	}
	for i, c := range cases {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		t.Setenv("PATH", t.TempDir())
		apps := noApps(t)
		dir := filepath.Join(home, c.folder)
		writeT(t, filepath.Join(dir, "settings.json"), "{}")
		a := &Agent{ID: c.id, Name: c.id, Bin: "magpie-test-gone-" + c.id, Dir: dir, Path: filepath.Join(dir, "settings.json")}
		if c.with != nil {
			c.with(t, home, apps)
		}
		if got := a.CLIMissing(); got != c.missing {
			t.Errorf("case %d (%s): missing %v, want %v", i, c.id, got, c.missing)
		}
	}

	// detected its own way: never taken for gone
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := &Agent{ID: "gemini", Bin: "magpie-test-gone-gemini", Dir: home, Path: filepath.Join(home, "x"), detect: func() bool { return true }}
	if g.CLIMissing() {
		t.Fatal("gemini, detected its own way, taken for missing")
	}
}

// judgeOffWindows has cliHeld judge as on Linux when the tests run on
// Windows, where a shared folder is never gone (#1482): the cases above
// are about the systems where its readers can be told.
func judgeOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		judgeFor(t, "linux")
	}
}

func judgeFor(t *testing.T, goos string) {
	t.Helper()
	was := goneOS
	goneOS = goos
	t.Cleanup(func() { goneOS = was })
}

// #1482 (edussss, Windows 11): from v0.1.1238 Claude Code and Codex both
// showed "CLI not found", their settings hidden, though v0.1.1231 listed
// them and the user runs them. On Windows what reads ~\.claude and
// ~\.codex can't all be told (a Store app, a portable editor, a PATH only
// a shell profile sets), so a shared folder is never gone there. An agent
// whose folder is only its CLI's still is.
func TestCLIMissingSharedFoldersWindows(t *testing.T) {
	judgeFor(t, "windows")
	for _, c := range []struct{ id, folder string }{{"claude", ".claude"}, {"codex", ".codex"}, {"cline", ".cline"}} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		t.Setenv("PATH", t.TempDir())
		noApps(t)
		dir := filepath.Join(home, c.folder)
		writeT(t, filepath.Join(dir, "settings.json"), "{}")
		a := &Agent{ID: c.id, Name: c.id, Bin: "magpie-test-gone-" + c.id, Dir: dir, Path: filepath.Join(dir, "settings.json")}
		if !a.Detected() {
			t.Fatalf("%s: its folder not detected", c.id)
		}
		if a.CLIMissing() {
			t.Errorf("%s: its folder alone taken for gone on Windows", c.id)
		}
		for _, x := range installsOf([]*Agent{a}, "windows", true) {
			if x.Missing {
				t.Errorf("%s offered again as gone: %+v", c.id, x)
			}
		}
	}

	// an agent whose folder only its CLI keeps is still gone without it
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	noApps(t)
	hdir := filepath.Join(home, ".hermes")
	writeT(t, filepath.Join(hdir, "config.yaml"), "custom_providers: []\n")
	h := &Agent{ID: "hermes", Name: "Hermes Agent", Bin: "magpie-test-gone-hermes", Dir: hdir, Path: filepath.Join(hdir, "config.yaml")}
	if !h.CLIMissing() {
		t.Fatal("hermes by its config.yaml alone not gone when judged for Windows")
	}
}

// #2300 (yangkai2, Windows) and #2304 (wlj521): from v0.1.1239 DeepSeek
// Harness showed 未找到 CLI with no model to pick. ~\.dsh is read by
// DeepSeek Harness Desktop too, which installs to any folder and puts
// its dsh.cmd in one the user picks, so on Windows a ~\.dsh whose dsh
// isn't found where magpie looks is never taken for a leftover. Judged
// for a Mac, the same folder is still gone (#843).
func TestCLIMissingDshWindows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("PATH", t.TempDir())
	noApps(t)
	dir := filepath.Join(home, ".dsh")
	// the desktop app's profile, as it leaves it after its first start
	writeT(t, filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml"), dshDesktopPatch)
	writeT(t, filepath.Join(dir, "profiles", "desktop", "package.json"), "{}\n")
	dsh := &Agent{ID: "dsh", Name: "DeepSeek Harness", Bin: "magpie-test-gone-dsh", Dir: dir, Path: filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")}
	if !dsh.Detected() {
		t.Fatal("~\\.dsh not detected")
	}
	judgeFor(t, "windows")
	if dsh.CLIMissing() {
		t.Fatal("dsh taken for uninstalled on Windows, its settings hidden")
	}
	for _, x := range installsOf([]*Agent{dsh}, "windows", true) {
		if x.Missing {
			t.Fatalf("dsh offered again as gone on Windows: %+v", x)
		}
	}
	judgeFor(t, "darwin")
	if !dsh.CLIMissing() {
		t.Fatal("~/.dsh with no dsh and no app not gone when judged for a Mac")
	}
}

// jetBrainsDirFor and zedAgentsDirFor are where jetBrainsHere and
// zedAgentsHere look on this system.
func jetBrainsDirFor(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "JetBrains")
	}
	return filepath.Join(home, ".config", "JetBrains")
}

func zedAgentsDirFor(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Zed", "external_agents")
	}
	return filepath.Join(home, ".local", "share", "zed", "external_agents")
}
