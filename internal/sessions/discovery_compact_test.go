package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// A kept listing's entries sit in one array and answer as os.ReadDir's do.
func TestKeptListingIsCompact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps os.ReadDir's entries, which carry their info")
	}
	dir := t.TempDir()
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	got, want := readDirectory(dir), mustReadDir(t, dir)
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d", len(got), len(want))
	}
	for i, e := range got {
		w := want[i]
		if e.Name() != w.Name() || e.IsDir() != w.IsDir() || e.Type() != w.Type() {
			t.Errorf("entry %d = %s %v %v, want %s %v %v", i, e.Name(), e.IsDir(), e.Type(), w.Name(), w.IsDir(), w.Type())
		}
		gi, err1 := e.Info()
		wi, err2 := w.Info()
		if err1 != nil || err2 != nil || gi.Size() != wi.Size() || gi.Mode() != wi.Mode() || !gi.ModTime().Equal(wi.ModTime()) {
			t.Errorf("entry %s info = %v %v, want %v %v", e.Name(), gi, err1, wi, err2)
		}
	}
	first, ok := got[0].(*keptEntry)
	if !ok {
		t.Fatalf("kept entry is %T, not listed", got[0])
	}
	for i, e := range got {
		p, _ := e.(*keptEntry)
		if p == nil || uintptr(unsafe.Pointer(p))-uintptr(unsafe.Pointer(first)) != uintptr(i)*unsafe.Sizeof(keptEntry{}) {
			t.Fatalf("entry %d has an allocation of its own", i)
		}
	}
	if again := readDirectory(dir); &again[0] != &got[0] {
		t.Error("an unchanged folder was listed again")
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return es
}
