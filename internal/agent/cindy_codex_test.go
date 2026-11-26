package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

// T_t__biu on X: with no Codex of the user's here, the Agents page listed
// Codex, as it listed Pi and Claude Code (#7872) for Cindy's own copies.
// Cindy's bundled Codex (<Cindy's data folder>/codex-package/<version>/
// bin/codex) leaves ~/.codex behind: a run of it without CODEX_HOME makes
// tmp/arg0-<os> there, and Cindy installs skills into ~/.codex/skills.
// With only Cindy's copy here the folder is Cindy's, in either build's
// data folder: Codex isn't listed, and the Library still gives the folder
// what Cindy's copy reads there (SharedHere).
func TestCodexFolderOfCindysCopyIsNotCodex(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	made := func(t *testing.T, home string) {
		writeAt(t, filepath.Join(home, ".codex", "tmp", "arg0-"+runtime.GOOS, "codex-arg0Ab12", "apply_patch"), "")
		writeAt(t, filepath.Join(home, ".codex", "skills", "learned", "SKILL.md"), "x")
		writeAt(t, filepath.Join(home, ".codex", "config.toml"), "")
	}
	cindys := func(t *testing.T, dir string) {
		writeAt(t, filepath.Join(dir, "codex-package", "0.130.0", "bin", exe("codex")), "")
		writeAt(t, filepath.Join(dir, "codex-package", "0.130.0", "codex-package.json"), "{}")
	}

	for _, build := range []string{"Cindy", "CindyGlobal"} {
		home, cn := cindyDetectHome(t)
		dir := filepath.Join(filepath.Dir(cn), build)
		made(t, home)
		if !codex(home).Detected() {
			t.Fatalf("%s: ~/.codex with no Cindy here: Codex not detected", build)
		}
		cindys(t, dir)
		a := codex(home)
		if a.Detected() || a.CLIMissing() {
			t.Fatalf("%s: ~/.codex Cindy's copy made: detected %v, CLI missing %v", build, a.Detected(), a.CLIMissing())
		}
		if !a.SharedHere() || !a.SharedRead() || a.SharedName() != "Codex" {
			t.Errorf("%s: Cindy's copy's ~/.codex: SharedHere %v, SharedRead %v, named %q", build, a.SharedHere(), a.SharedRead(), a.SharedName())
		}
	}

	// a version folder Cindy hasn't finished putting its program in
	home, dir := cindyDetectHome(t)
	made(t, home)
	writeAt(t, filepath.Join(dir, "codex-package", "0.130.0", "codex-package.json"), "{}")
	if !codex(home).Detected() {
		t.Error("a codex-package version folder with no codex in it hid Codex")
	}

	// anything else of Codex's here is Codex
	cases := []struct {
		name string
		with func(t *testing.T, home string)
	}{
		{"the codex command in ~/.local/bin", func(t *testing.T, home string) {
			p := filepath.Join(home, ".local", "bin", exe("codex"))
			writeAt(t, p, "#!/bin/sh\n")
			os.Chmod(p, 0o755)
			proc.Forget()
		}},
		{"Codex's own installer's copy", func(t *testing.T, home string) {
			writeAt(t, filepath.Join(home, ".codex", "packages", "standalone", "current", "codex"), "")
		}},
		{"VS Code's extension", func(t *testing.T, home string) {
			mkdirT(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.5.1"))
		}},
		{"a JetBrains IDE", func(t *testing.T, home string) { mkdirT(t, jetBrainsDirFor(home)) }},
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, struct {
			name string
			with func(t *testing.T, home string)
		}{"the Codex app", func(t *testing.T, home string) {
			apps := noApps(t)
			plantApp(t, apps, "Codex.app", "com.openai.codex")
		}})
	}
	for _, tc := range cases {
		home, dir := cindyDetectHome(t)
		made(t, home)
		cindys(t, dir)
		tc.with(t, home)
		if !codex(home).Detected() {
			t.Errorf("Cindy's copy and %s: Codex not detected", tc.name)
		}
	}

	// CODEX_HOME names the user's own folder, which Cindy's copy never
	// makes
	home, dir = cindyDetectHome(t)
	cindys(t, dir)
	own := filepath.Join(home, "codex-own")
	t.Setenv("CODEX_HOME", own)
	writeAt(t, filepath.Join(own, "config.toml"), "")
	if !codex(home).Detected() {
		t.Error("CODEX_HOME's folder beside Cindy's copy: not detected")
	}
}
