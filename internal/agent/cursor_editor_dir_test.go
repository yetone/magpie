package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The Cursor editor makes ~/.cursor (argv.json, extensions/, mcp.json,
// projects/, skills-cursor/) on a machine with no cursor-agent, and the
// Agents page showed a Cursor row there that wasn't the CLI (@G_Z_Y on X).
// That folder is no sign of Cursor CLI; its cli-config.json or its command
// is. The folder is still SharedHere, for what the editor reads in it.
func TestCursorFolderFromTheEditorIsNotTheCLI(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	home := t.TempDir()
	a := cursor(home)
	for _, f := range []string{"argv.json", "extensions/extensions.json", "mcp.json", "projects/work/repo.json", "skills-cursor/a/SKILL.md", "ide_state.json"} {
		writeAt(t, filepath.Join(a.Dir, f), "{}")
	}
	if cursor(home).Detected() {
		t.Errorf("a ~/.cursor holding only the editor's files is taken for Cursor CLI")
	}
	if !cursor(home).SharedHere() {
		t.Errorf("the editor's ~/.cursor isn't SharedHere: the Library would stop giving the editor its servers")
	}
	for _, name := range []string{"cursor-agent", "cursor-agent.exe"} {
		writeAt(t, filepath.Join(bin, name), "#!/bin/sh\n")
		os.Chmod(filepath.Join(bin, name), 0o755)
	}
	if !cursor(home).Detected() {
		t.Error("the cursor-agent command on PATH no longer finds Cursor CLI")
	}
	os.Remove(filepath.Join(bin, "cursor-agent"))
	os.Remove(filepath.Join(bin, "cursor-agent.exe"))
	writeAt(t, a.Path, `{"version":1}`)
	if !cursor(home).Detected() {
		t.Error("~/.cursor/cli-config.json no longer finds Cursor CLI")
	}

	// no folder at all: nothing shared is here
	if cursor(t.TempDir()).SharedHere() {
		t.Error("SharedHere with no ~/.cursor")
	}
	// only Cursor CLI and Copilot CLI (~/.copilot, which VS Code's and the
	// JetBrains plugin's Copilot read) share their folders
	for _, b := range All() {
		if b.ID != "cursor" && b.ID != "copilot" && b.shared != "" {
			t.Errorf("%s shares its folder too: %q", b.ID, b.shared)
		}
	}
}
