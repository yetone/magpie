package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

// With Claude Desktop on magpie, magpie writes the window of Desktop's
// models into ~/.claude/settings.json for Desktop's Code tab, and made
// the folder to do it. Those keys (autoCompactWindow, modelSettings) made
// a Claude Code row, "CLI not installed", on a computer where Claude Code
// never was (KorwinTeo on X). What magpie writes there is no sign of
// Claude Code; a key of the user's still is.
func TestClaudeCodeNotListedForMagpiesWindow(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	t.Setenv("PATH", t.TempDir())
	proc.Forget()
	t.Cleanup(proc.Forget)
	os.RemoveAll(filepath.Join(home, ".claude"))
	a := claude(home)
	if a.Detected() {
		t.Fatal("listed with nothing of Claude Code's here")
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "modelSettings"); v == "" {
		t.Fatalf("Desktop's windows not written: %s", readFile(path))
	}
	if a.Detected() {
		t.Errorf("listed for what magpie wrote for Desktop: %s", readFile(path))
	}
	edit.SetJSON(path, edit.KV{Path: "theme", Value: "dark"})
	if !a.Detected() {
		t.Error("a setting of the user's no sign of Claude Code")
	}
}
