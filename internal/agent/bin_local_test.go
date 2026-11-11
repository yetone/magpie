package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// Xuanwo on X: the official installer puts agy in ~/.local/bin, which a
// magpie started from a desktop launcher has not on its PATH; Antigravity
// CLI was reported as not found on Linux.
func TestDetectedFindsBinInLocalBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	a := &Agent{ID: "agy", Name: "Antigravity CLI", Bin: "magpie-test-agy", Dir: filepath.Join(home, ".gemini", "antigravity-cli"), Path: filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")}
	if a.Detected() {
		t.Fatal("detected with nothing installed")
	}
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	name := a.Bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	testenv.Program(t, filepath.Join(local, name), "#!/bin/sh\n")
	if !a.Detected() {
		t.Fatal("agy in ~/.local/bin, off PATH, not detected")
	}
}
