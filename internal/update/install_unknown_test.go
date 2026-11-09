package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// denyStat has statPath answer "permission denied" for the paths given,
// as Windows does for a folder the user may not look into, and a share
// that doesn't answer does in its own words: they are there, but Stat
// can't say so.
func denyStat(t *testing.T, paths ...string) {
	t.Helper()
	was := statPath
	t.Cleanup(func() { statPath = was })
	statPath = func(p string) (fs.FileInfo, error) {
		for _, d := range paths {
			if samePath(p, d) {
				return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrPermission}
			}
		}
		return os.Stat(p)
	}
}

// keeps fails the test unless file is still there holding want.
func keeps(t *testing.T, file, want string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%s is gone: %v", file, err)
	}
	if string(b) != want {
		t.Fatalf("%s holds %q, want %q", file, b, want)
	}
}

// A folder on the way to the one checked that can't be looked at is
// unknown, not missing: the check refuses, and the folder and what is in
// it stay. Taken for missing, it was the folder the check had "made",
// and was removed with everything in it.
func TestInstallableKeepsAnAncestorItCantLookAt(t *testing.T) {
	newInstallFixture(t)
	tmp := t.TempDir()
	theirs := filepath.Join(tmp, "Tools")
	file := filepath.Join(theirs, "notes.txt")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	denyStat(t, theirs)
	err := Installable(filepath.Join(theirs, "new", "magpie"))
	keeps(t, file, "mine")
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("a folder that can't be looked at: %v", err)
	}
	if _, err := os.Stat(filepath.Join(theirs, "new")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the check made a folder under one it couldn't look at: %v", err)
	}
}

// The folder checked itself, there with the user's files, that can't be
// looked at: refused, and nothing in it removed.
func TestInstallableKeepsTheFolderItCantLookAt(t *testing.T) {
	newInstallFixture(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "magpie")
	file := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	denyStat(t, dir)
	err := Installable(dir)
	keeps(t, file, "{}")
	if err == nil {
		t.Fatal("a folder that can't be looked at accepted")
	}
}

// Only the folders the check made are removed: every folder that was
// there before stays, with what is in it.
func TestInstallableRemovesOnlyWhatItMade(t *testing.T) {
	newInstallFixture(t)
	tmp := t.TempDir()
	keep := filepath.Join(tmp, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Installable(filepath.Join(tmp, "a", "b", "magpie")); err != nil {
		t.Fatal(err)
	}
	keeps(t, keep, "x")
	if _, err := os.Stat(filepath.Join(tmp, "a")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the folders made were left: %v", err)
	}
	b := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "user.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Installable(filepath.Join(b, "magpie")); err != nil {
		t.Fatal(err)
	}
	keeps(t, filepath.Join(b, "user.txt"), "u")
	if _, err := os.Stat(filepath.Join(b, "magpie")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the folder made was left: %v", err)
	}
}

// An installed magpie.exe that can't be looked at isn't moved over by a
// fresh install: settle says it can't tell, and the download runs on.
func TestSettleLeavesAnExeItCantLookAt(t *testing.T) {
	f := newInstallFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.to, []byte("MZ installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	denyStat(t, f.to)
	to, err := settle(f.exe)
	keeps(t, f.to, "MZ installed")
	if err == nil || to != "" {
		t.Fatalf("settle = %q, %v; want an error", to, err)
	}
}

func TestPresent(t *testing.T) {
	tmp := t.TempDir()
	if ok, err := present(tmp); !ok || err != nil {
		t.Fatalf("present(there) = %v, %v", ok, err)
	}
	if ok, err := present(filepath.Join(tmp, "no")); ok || err != nil {
		t.Fatalf("present(missing) = %v, %v", ok, err)
	}
	denyStat(t, tmp)
	if ok, err := present(tmp); ok || err == nil {
		t.Fatalf("present(unknown) = %v, %v", ok, err)
	}
}
