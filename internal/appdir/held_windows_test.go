package appdir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The real refusal: a file in magpie's folder held open, as an agent's
// MCP server holds one, stops the move across disks before it copies
// anything (#7812).
func TestMoveHeldOpenOnWindows(t *testing.T) {
	cfg, cache, disk := moveHome(t)
	old := rename
	rename = func(string, string) error { return errors.New("cross-device link") }
	t.Cleanup(func() { rename = old })
	f, err := os.Open(filepath.Join(cfg, "skills", "a", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := Target(disk)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	err = Move(root)
	var he *HeldError
	if !errors.As(err, &he) {
		f.Close()
		t.Fatalf("Move with a file held open = %v, want a HeldError", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config")); err == nil {
		t.Fatal("copied before the folder was known to move")
	}
	f.Close()
	checkFiles(t, cfg, cache)
	if err := Move(root); err != nil {
		t.Fatalf("let go of, it moves: %v", err)
	}
	checkFiles(t, cfg, cache)
}
