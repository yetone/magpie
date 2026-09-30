package edit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func symlink(t *testing.T, to, at string) {
	t.Helper()
	if err := os.Symlink(to, at); err != nil {
		t.Skipf("can't make symlinks here: %v", err)
	}
}

func isLink(t *testing.T, p string) bool {
	t.Helper()
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// A config linked in from a dotfiles repo stays linked: writing and removing
// it go to the file the link points at, in another folder, through a chain.
func TestWriteAtomicThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "dotfiles"), 0o755)
	os.MkdirAll(filepath.Join(dir, "home"), 0o755)
	real := filepath.Join(dir, "dotfiles", "AGENTS.md")
	os.WriteFile(real, []byte("mine\n"), 0o600)
	mid := filepath.Join(dir, "home", "mid.md")
	symlink(t, filepath.Join("..", "dotfiles", "AGENTS.md"), mid) // relative
	link := filepath.Join(dir, "home", "AGENTS.md")
	symlink(t, mid, link)

	if err := WriteAtomic(link, []byte("mine\nmagpie's\n")); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, link) || !isLink(t, mid) {
		t.Fatal("a link was replaced by a file")
	}
	if got := read(t, real); got != "mine\nmagpie's\n" {
		t.Fatalf("real file: %q", got)
	}
	if st, _ := os.Stat(real); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}

	if err := Remove(link); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, link) {
		t.Fatal("Remove took the user's link")
	}
	if got := read(t, real); got != "" {
		t.Fatalf("real file after Remove: %q", got)
	}
}

// A link to a file not made yet gets the file made where it points.
func TestWriteAtomicDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.md")
	link := filepath.Join(dir, "link.md")
	symlink(t, real, link)
	if err := WriteAtomic(link, []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, link) || read(t, real) != "x\n" {
		t.Fatal("dangling link not written through")
	}
}

func TestRemovePlainFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "AGENTS.md")
	os.WriteFile(p, []byte("x\n"), 0o644)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}
