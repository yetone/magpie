package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

// cindyDetectHome is a home with no Pi and no Claude Code of the user's:
// nothing on PATH, no app, no editor, no Claude Desktop. It returns the
// home and Cindy's data folder in it (os.UserConfigDir's Cindy), not made.
func cindyDetectHome(t *testing.T) (home, cindyDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PATH", t.TempDir())
	noApps(t)
	was := windowsStorePackages
	windowsStorePackages = func() []string { return nil }
	t.Cleanup(func() { windowsStorePackages = was })
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(cfg, "Cindy")
}

// T_t__biu on X (#7872, Windows, magpie 0.1.4862, Cindy 0.1.146): with
// no Pi installed, the Library wrote Pi's ~/.pi/agent/AGENTS.md for Cindy,
// whose Pi reads it (#7854), and the Agents page then listed Pi, "CLI not
// found". A folder holding only Pi's instruction files is Cindy's; Pi's
// own run leaves its settings, sign-in or sessions beside them.
func TestPiFolderOfInstructionsAloneIsNotPi(t *testing.T) {
	for _, only := range [][]string{
		{"AGENTS.md"},
		{"AGENTS.md", "skills/a/SKILL.md"},
		{"CLAUDE.md"},
		{"AGENTS.override.md", "AGENTS.md"},
	} {
		home, _ := cindyDetectHome(t)
		d := filepath.Join(home, ".pi", "agent")
		for _, f := range only {
			writeAt(t, filepath.Join(d, f), "x")
		}
		if a := pi(home); a.Detected() || a.CLIMissing() {
			t.Errorf("%v alone: Pi detected %v, CLI missing %v", only, a.Detected(), a.CLIMissing())
		}
		for _, own := range []string{"settings.json", "auth.json", "models.json", "sessions/--work--/s.jsonl"} {
			writeAt(t, filepath.Join(d, own), "{}")
			if !pi(home).Detected() {
				t.Errorf("%v with Pi's %s: not detected", only, own)
			}
			os.RemoveAll(filepath.Join(d, strings.Split(own, "/")[0]))
		}
		// the pi command is Pi, whatever its folder holds
		writeAt(t, filepath.Join(home, ".local", "bin", exe("pi")), "#!/bin/sh\n")
		os.Chmod(filepath.Join(home, ".local", "bin", exe("pi")), 0o755)
		proc.Forget()
		if !pi(home).Detected() {
			t.Errorf("%v with pi in ~/.local/bin: not detected", only)
		}
	}
}

// T_t__biu on X (#7872): Cindy's bundled Claude Code
// (Cindy\claude-code\2.1.321\claude.exe) made ~/.claude and ~/.claude.json
// on its first run, with no Claude Code of the user's here, and the Agents
// page listed Claude Code. With only Cindy's copy here they are Cindy's:
// Claude Code isn't listed, and the Library still gives the folder what
// Cindy's copy reads there (SharedHere).
func TestClaudeFolderOfCindysCopyIsNotClaudeCode(t *testing.T) {
	made := func(t *testing.T, home string) {
		writeAt(t, filepath.Join(home, ".claude", "projects", "-work", "s.jsonl"), "{}")
		writeAt(t, filepath.Join(home, ".claude", "settings.json"), "{}")
		writeAt(t, filepath.Join(home, ".claude.json"), `{"numStartups":1}`)
	}
	cindys := func(t *testing.T, dir string) {
		writeAt(t, filepath.Join(dir, "claude-code", "2.1.321", exe("claude")), "")
		writeAt(t, filepath.Join(dir, "claude-code", "2.1.321", ".verified"), "")
	}

	home, dir := cindyDetectHome(t)
	made(t, home)
	if !claude(home).Detected() {
		t.Fatal("~/.claude with no Cindy here: Claude Code not detected")
	}
	cindys(t, dir)
	a := claude(home)
	if a.Detected() || a.CLIMissing() {
		t.Fatalf("~/.claude Cindy's copy made: detected %v, CLI missing %v", a.Detected(), a.CLIMissing())
	}
	if !a.SharedHere() || !a.SharedRead() || a.SharedName() != "Claude Code" {
		t.Errorf("Cindy's copy's ~/.claude: SharedHere %v, SharedRead %v, named %q", a.SharedHere(), a.SharedRead(), a.SharedName())
	}

	// a version folder Cindy hasn't finished putting its program in
	home, dir = cindyDetectHome(t)
	made(t, home)
	writeAt(t, filepath.Join(dir, "claude-code", "2.1.321", ".verified"), "")
	if !claude(home).Detected() {
		t.Error("an empty claude-code version folder of Cindy's hid Claude Code")
	}

	// anything else of Claude Code's here is Claude Code
	for _, tc := range []struct {
		name string
		with func(t *testing.T, home string)
	}{
		{"the claude command in ~/.local/bin", func(t *testing.T, home string) {
			p := filepath.Join(home, ".local", "bin", exe("claude"))
			writeAt(t, p, "#!/bin/sh\n")
			os.Chmod(p, 0o755)
			proc.Forget()
		}},
		{"~/.claude/local's", func(t *testing.T, home string) {
			writeAt(t, filepath.Join(home, ".claude", "local", exe("claude")), "")
		}},
		{"VS Code's extension", func(t *testing.T, home string) {
			mkdirT(t, filepath.Join(home, ".vscode", "extensions", "anthropic.claude-code-2.1.3"))
		}},
		{"a JetBrains IDE", func(t *testing.T, home string) { mkdirT(t, jetBrainsDirFor(home)) }},
	} {
		home, dir := cindyDetectHome(t)
		made(t, home)
		cindys(t, dir)
		tc.with(t, home)
		if !claude(home).Detected() {
			t.Errorf("Cindy's copy and %s: Claude Code not detected", tc.name)
		}
	}

	// CLAUDE_CONFIG_DIR names the user's own folder, which Cindy's copy
	// never makes
	home, dir = cindyDetectHome(t)
	cindys(t, dir)
	own := filepath.Join(home, "claude-own")
	t.Setenv("CLAUDE_CONFIG_DIR", own)
	writeAt(t, filepath.Join(own, "settings.json"), "{}")
	if !claude(home).Detected() {
		t.Error("CLAUDE_CONFIG_DIR's folder beside Cindy's copy: not detected")
	}
}
