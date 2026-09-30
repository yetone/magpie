package edit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// hardLink makes link another name of target, skipping where the file
// system has no hard links.
func hardLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Link(target, link); err != nil {
		t.Skip("no hard links here:", err)
	}
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fa, fb) {
		t.Fatalf("%s and %s are no longer one file", a, b)
	}
}

// A config another tool keeps as a hard link (Orca links omp's models.yml
// into its overlay): a write goes into the file itself, so every name has
// the new text, the mode stays, and nothing is left beside it.
func TestWriteAtomicKeepsHardLink(t *testing.T) {
	d := t.TempDir()
	overlay := filepath.Join(d, "overlay.yml")
	if err := os.WriteFile(overlay, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(overlay)
	p := filepath.Join(d, "models.yml")
	hardLink(t, overlay, p)
	for _, text := range []string{"providers:\n  magpie:\n    api: openai-completions\n", "x: 1\n"} {
		if err := WriteAtomic(p, []byte(text)); err != nil {
			t.Fatal(err)
		}
		sameFile(t, p, overlay)
		if got := read(t, overlay); got != text {
			t.Fatalf("other name: %q, want %q", got, text)
		}
	}
	if now, _ := os.Stat(p); now.Mode().Perm() != st.Mode().Perm() {
		t.Errorf("mode %v, was %v", now.Mode().Perm(), st.Mode().Perm())
	}
	if es, _ := os.ReadDir(d); len(es) != 2 {
		t.Errorf("left beside it: %v", es)
	}
}

// Emptying a file with another name empties both and keeps it, as with a
// symlink: deleting one name would leave the old text under the other.
func TestRemoveKeepsHardLink(t *testing.T) {
	d := t.TempDir()
	other := filepath.Join(d, "other.md")
	if err := os.WriteFile(other, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "AGENTS.md")
	hardLink(t, other, p)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	sameFile(t, p, other)
	if got := read(t, other); got != "" {
		t.Fatalf("other name kept %q", got)
	}
}

// A failed edit puts a hard-linked file back in place, still one file.
func TestAtomicallyKeepsHardLink(t *testing.T) {
	d := t.TempDir()
	other := filepath.Join(d, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "settings.json")
	hardLink(t, other, p)
	err := Atomically(func() error {
		if err := WriteAtomic(p, []byte("half")); err != nil {
			return err
		}
		return errors.New("fail")
	}, p)
	if err == nil || err.Error() != "fail" {
		t.Fatalf("err %v", err)
	}
	sameFile(t, p, other)
	if got := read(t, other); got != "{}" {
		t.Errorf("other name %q", got)
	}
}
