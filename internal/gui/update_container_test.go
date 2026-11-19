package gui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// The Docker image runs /magpie as nonroot, in a folder it may not write
// to and with no password prompt, so the page said "move it to a folder you
// can write to" (/), which a container can't act on. In a container it says
// to pull the new image, by the version's own tag: on the Daily channel
// :latest held another release than the one out (fishmagic80 on X).
// Outside one the folder is still named (#1277).
func TestUpdateInContainerSaysPullTheImage(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user may not write to")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "magpie")
	if err := os.WriteFile(exe, []byte("magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	wasE, wasC := canElevate, inContainer
	t.Cleanup(func() { canElevate, inContainer = wasE, wasC })
	canElevate = func() bool { return false }

	inContainer = func() bool { return true }
	u := &updater{state: "available", latest: &update.Release{Version: "0.1.4479"}, channel: "daily"}
	u.placeExe(exe)
	if j := u.json(); u.exe != "" || j.Stuck != "container" || j.StuckDir != "" || j.Image != "ghcr.io/yetone/magpie:0.1.4479" {
		t.Fatalf("in a container: exe %q, %+v", u.exe, j)
	}

	inContainer = func() bool { return false }
	u = &updater{state: "available", latest: &update.Release{Version: "0.1.4479"}}
	u.placeExe(exe)
	if j := u.json(); j.Stuck != "not-writable" || j.StuckDir != dir || j.Image != "" {
		t.Fatalf("outside one: %+v", j)
	}

	// a container whose binary may be replaced updates itself as before
	inContainer = func() bool { return true }
	os.Chmod(dir, 0o755)
	u = &updater{}
	u.placeExe(exe)
	if u.exe != exe || u.stuck != "" {
		t.Fatalf("a writable folder in a container: exe %q, stuck %q", u.exe, u.stuck)
	}
}
