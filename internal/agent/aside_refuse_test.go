package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// refuseWrites makes the file at path refuse a write to it, on either
// platform, and what refuses one differs with how the write is made.
// WriteAtomic writes a temp file beside the target and renames it over, so
// on Unix it is the folder that has to refuse a write: 0500 takes away the
// permission a create and a rename both need, where 0400 on the file alone
// is stepped over, since a rename answers to the folder and not to the
// file it lands on. Windows has no permission a folder carries that a
// write to a file in it is refused for, so the file is marked read-only
// there, which is what a write to it answers a permission error for.
// Either way a platform the write still goes through on is no answer for
// these checks, so it says so rather than asserting nothing.
func refuseWrites(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		dir := filepath.Dir(path)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		if refusesNoWrite(dir) {
			t.Skipf("%s made read-only still takes a write", dir)
		}
		return
	}
	refuseWritesWindows(t, path)
}

// refusesNoWrite is whether a file may still be created in dir, which is
// what WriteAtomic's temp file needs and what a folder carrying the
// read-only permission refuses.
func refusesNoWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return true
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return false
}

// refused is whether err is the answer a write to a file that refuses one
// gets, each platform naming it its own way.
func refused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "permission denied") || strings.Contains(s, "Access is denied")
}
