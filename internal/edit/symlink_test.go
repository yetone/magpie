package edit

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// linkTo makes link a symlink to target, skipping where symlinks can't be
// made (Windows without the privilege).
func linkTo(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
}

func isLink(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink (%v)", p, err)
	}
}

// A config kept in a dotfiles repo and linked into place (#323): a write
// goes to the file it points at, the link stays, and the mode is the
// target's.
func TestWriteAtomicFollowsSymlink(t *testing.T) {
	d := t.TempDir()
	repo := filepath.Join(d, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo, []byte("{\n  \"model\": \"old\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(d, "home", ".claude")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(conf, "settings.json")
	linkTo(t, "../../dotfiles/settings.json", p) // relative, as stow makes them
	if err := SetJSON(p, KV{Path: "model", Value: "new"}); err != nil {
		t.Fatal(err)
	}
	isLink(t, p)
	if got := read(t, repo); got != "{\n  \"model\": \"new\"\n}\n" {
		t.Fatalf("target: %q", got)
	}
	if st, _ := os.Stat(repo); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if es, _ := os.ReadDir(conf); len(es) != 1 {
		t.Errorf("left beside the link: %v", es)
	}

	// a link through a link, to a file not made yet: the file is made
	// where the last one points
	next := filepath.Join(d, "dotfiles", "new.md")
	mid := filepath.Join(d, "mid.md")
	linkTo(t, next, mid)
	q := filepath.Join(conf, "CLAUDE.md")
	linkTo(t, mid, q)
	if err := WriteAtomic(q, []byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	isLink(t, q)
	isLink(t, mid)
	if got := read(t, next); got != "hi\n" {
		t.Fatalf("dangling target: %q", got)
	}

	// links in a loop: an error, and the link left as it is
	a, b := filepath.Join(d, "a"), filepath.Join(d, "b")
	linkTo(t, b, a)
	linkTo(t, a, b)
	if err := WriteAtomic(a, []byte("x")); err == nil {
		t.Error("a loop of links was written")
	}
	isLink(t, a)
}

// Emptying a linked file empties its target and keeps the link; a plain
// file still goes, and a link to nothing is left be.
func TestRemoveKeepsSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "instructions.md")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "link.md")
	linkTo(t, target, p)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	isLink(t, p)
	if got := read(t, target); got != "" {
		t.Fatalf("target kept %q", got)
	}

	gone := filepath.Join(d, "gone.md")
	dangling := filepath.Join(d, "dangling.md")
	linkTo(t, gone, dangling)
	if err := Remove(dangling); err != nil {
		t.Fatal(err)
	}
	isLink(t, dangling)
	if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
		t.Error("emptying a link to nothing made its target")
	}

	plain := tmpFile(t, "plain.md", "x")
	if err := Remove(plain); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(plain); !errors.Is(err, os.ErrNotExist) {
		t.Error("a plain file stayed")
	}
}

// A failed edit puts a linked file back through its link, and takes away
// only the file it made behind a link to nothing, not the link.
func TestAtomicallyKeepsSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "real.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "link.json")
	linkTo(t, target, p)
	missing := filepath.Join(d, "missing.json")
	q := filepath.Join(d, "dangling.json")
	linkTo(t, missing, q)
	err := Atomically(func() error {
		if err := WriteAtomic(p, []byte("half")); err != nil {
			return err
		}
		if err := WriteAtomic(q, []byte("half")); err != nil {
			return err
		}
		return errors.New("fail")
	}, p, q)
	if err == nil || err.Error() != "fail" {
		t.Fatalf("err %v", err)
	}
	isLink(t, p)
	isLink(t, q)
	if got := read(t, target); got != "{}" {
		t.Errorf("target %q", got)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("the file made behind the link stayed")
	}
}
