package steady

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A file is flushed to the disk before it is renamed over the old one: a
// Windows machine that went down between the two left logins.json at its
// full length, every byte zero (#1505). The flush is of the new contents,
// before the old file is replaced; one that fails leaves the old file and
// no temp file beside it.
func TestWriteFileSyncsBeforeRename(t *testing.T) {
	defer func() { Sync = (*os.File).Sync }()
	dir := t.TempDir()
	p := filepath.Join(dir, "logins.json")
	if err := os.WriteFile(p, []byte(`["old"]`), 0o600); err != nil {
		t.Fatal(err)
	}

	synced := 0
	Sync = func(f *os.File) error {
		synced++
		if b, _ := os.ReadFile(p); string(b) != `["old"]` {
			t.Errorf("flushed after the rename: the file was already %q", b)
		}
		if b, _ := os.ReadFile(f.Name()); string(b) != `["new"]` {
			t.Errorf("flushed %q, want the new contents", b)
		}
		return f.Sync()
	}
	if err := WriteFile(p, []byte(`["new"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if synced != 1 {
		t.Fatalf("flushed %d times, want once", synced)
	}
	if b, _ := os.ReadFile(p); string(b) != `["new"]` {
		t.Fatalf("file = %q", b)
	}

	Sync = func(*os.File) error { return errors.New("the disk went away") }
	if err := WriteFile(p, []byte(`["newer"]`), 0o600); err == nil {
		t.Fatal("a flush that failed reported as written")
	}
	if b, _ := os.ReadFile(p); string(b) != `["new"]` {
		t.Fatalf("a failed flush changed the file to %q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("a failed write left %v", left)
	}
}

// A write that died between making its temp file and renaming it leaves the
// temp beside the file: a multi-instance start-up exiting mid-write made
// hundreds of them beside magpie's own files over a run, and nothing took
// them — edit.WriteAtomic reaps its temps beside the file it wrote, but
// steady.WriteFile never did (#4938). A served write reaps the ones old
// enough to belong to no write still going on, its own shape and edit's
// alike, and leaves a younger one alone and another file's temp alone.
func TestWriteFileReapsStaleTemps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "quotas.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	temps := []struct {
		name         string
		stale, stays bool
	}{
		{".quotas.json.123", true, false},     // this package's shape, a crash left it
		{".quotas.json.456", false, true},     // another write may still be on it
		{".quotas.json.789.tmp", true, false}, // edit.WriteAtomic's shape, the same file's
		{".quotas.json.x.012", true, true},    // .quotas.json.x's, not quotas.json's
	}
	for _, tt := range temps {
		f := filepath.Join(dir, tt.name)
		if err := os.WriteFile(f, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if tt.stale {
			if err := os.Chtimes(f, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("file = %q, %v", b, err)
	}
	for _, tt := range temps {
		_, err := os.Stat(filepath.Join(dir, tt.name))
		if stays := err == nil; stays != tt.stays {
			t.Errorf("%s: stays %v, want %v", tt.name, stays, tt.stays)
		}
	}
}
