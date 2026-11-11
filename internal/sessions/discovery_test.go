package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A listing made in the same mtime tick as the directory's last change is
// read again: on Linux an entry added within that tick left the mtime (and
// the directory's size) as they were, and the new Cowork session stayed
// hidden until the folder changed again.
func TestRacyDirectoryListedAgain(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), nil, 0o644)
	st, _ := os.Stat(dir)
	seed := func(at time.Time) {
		directoryCache.Lock()
		directoryCache.entries[dir] = directoryEntry{st.ModTime(), st.Size(), at, nil}
		directoryCache.Unlock()
	}
	t.Cleanup(func() {
		directoryCache.Lock()
		delete(directoryCache.entries, dir)
		directoryCache.Unlock()
	})
	// listed in the tick of its change: not trusted
	seed(st.ModTime())
	if n := len(readDirectory(dir)); n != 1 {
		t.Fatalf("a racy listing was served: %d entries", n)
	}
	// listed well after it: served as cached
	seed(st.ModTime().Add(time.Minute))
	if n := len(readDirectory(dir)); n != 0 {
		t.Fatalf("a settled listing was read again: %d entries", n)
	}
}

// A folder that can't be read this time is unknown, not empty: a Claude
// Code project folder refused for a moment (too many files open, a sharing
// violation) took every session in it off the Sessions page and let go
// their parses, and they came back at a later poll (@simonzyzy on X). Its
// last good listing stands; a folder that is gone lists as none.
func TestUnreadableDirectoryKeepsItsLastListing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder is refused by its mode where there is a mode to refuse and no root")
	}
	dir := t.TempDir()
	for _, n := range []string{"a.jsonl", "b.jsonl"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	t.Cleanup(func() {
		os.Chmod(dir, 0o755)
		directoryCache.Lock()
		delete(directoryCache.entries, dir)
		directoryCache.Unlock()
	})
	if n := len(readDirectory(dir)); n != 2 {
		t.Fatalf("listed %d entries, want 2", n)
	}
	// the folder changes (a new mtime: the kept listing is not taken as
	// current) and is then refused
	os.WriteFile(filepath.Join(dir, "c.jsonl"), nil, 0o644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(dir, future, future)
	os.Chmod(dir, 0)
	if n := len(readDirectory(dir)); n != 2 {
		t.Fatalf("a folder refused for a moment listed %d entries, want its last 2", n)
	}
	os.Chmod(dir, 0o755)
	if n := len(readDirectory(dir)); n != 3 {
		t.Fatalf("read again it listed %d entries, want 3", n)
	}
	os.RemoveAll(dir)
	if n := len(readDirectory(dir)); n != 0 {
		t.Fatalf("a folder that is gone listed %d entries", n)
	}
}
