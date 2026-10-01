package sessions

import (
	"os"
	"path/filepath"
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
