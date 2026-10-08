//go:build !windows

package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestRTKOffPath (#601): an rtk magpie finds where rtk's script puts it,
// ~/.local/bin, which the app adds to its own PATH but the user's shell
// may not have, isn't one the agents' hooks can run — Pi's turns itself
// off with "rtk binary not found in PATH". The page says so instead of
// showing RTK working, and Put RTK on PATH links it into a folder on it.
func TestRTKOffPath(t *testing.T) {
	h := sandbox(t)
	local := filepath.Join(h, ".local", "bin")
	testenv.Program(t, filepath.Join(local, "rtk"), fakeRTK)
	tools := filepath.Join(h, "tools")
	testenv.Program(t, filepath.Join(tools, "curl"), "#!/bin/sh\n")
	t.Setenv("PATH", tools)

	v := ReadRTK()
	if v.Path != filepath.Join(local, "rtk") || !v.OffPath {
		t.Fatalf("rtk at %q, off PATH %v; want it found in ~/.local/bin and said to be off PATH", v.Path, v.OffPath)
	}
	if v.PathDir != "" {
		t.Fatalf("no folder on PATH rtk can go in, yet PathDir is %q", v.PathDir)
	}
	if _, err := PathRTK(); err == nil || !strings.Contains(err.Error(), local) {
		t.Fatalf("PathRTK with nowhere to put rtk: %v; want it to say to add %s to PATH", err, local)
	}
	if _, err := SetRTK("claude", true); err != nil {
		t.Fatal(err)
	}

	// ~/bin is on the shell's PATH: rtk is linked in there
	userBin := filepath.Join(h, "bin")
	t.Setenv("PATH", tools+string(os.PathListSeparator)+userBin)
	if v = ReadRTK(); !v.OffPath || v.PathDir != userBin || !v.PathLink {
		t.Fatalf("off PATH %v, PathDir %q, link %v; want %s linked into", v.OffPath, v.PathDir, v.PathLink, userBin)
	}
	v, err := PathRTK()
	if err != nil {
		t.Fatal(err)
	}
	if v.OffPath || !slices.Contains(v.Restart, "claude") {
		t.Fatalf("after PathRTK: off PATH %v, restart %v", v.OffPath, v.Restart)
	}
	if to, err := os.Readlink(filepath.Join(userBin, "rtk")); err != nil || to != filepath.Join(local, "rtk") {
		t.Fatalf("~/bin/rtk links to %q (%v)", to, err)
	}
	// upgraded with its script into ~/.local/bin, where the link points,
	// not over the link
	c := rtkUpgrader(filepath.Join(userBin, "rtk"))
	real, _ := filepath.EvalSymlinks(local)
	if len(c) != 4 || c[3] != real {
		t.Fatalf("upgrade through the link: %q; want rtk's script into %s", c, real)
	}
}
