package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Directory membership changes with the directory's mtime; file contents don't.
// Keep names only, and always stat candidate files for current size/mtime. This
// avoids rereading every Cowork project directory at each five-second refresh.
var directoryCache = struct {
	sync.Mutex
	entries map[string]directoryEntry
}{entries: map[string]directoryEntry{}}

type directoryEntry struct {
	mod     time.Time
	size    int64
	at      time.Time // when it was listed
	entries []os.DirEntry
}

// racyDirectory is how close to a directory's mtime a listing can't be
// trusted: mtimes are coarse (a Linux kernel tick, a second on some
// filesystems) and a directory's size often doesn't change, so an entry
// added in the same tick as a listing left the mtime as it was and stayed
// hidden. Such a listing is read again until it's older than this.
const racyDirectory = 2 * time.Second

func readDirectory(path string) []os.DirEntry {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return nil
	}
	directoryCache.Lock()
	old, ok := directoryCache.entries[path]
	directoryCache.Unlock()
	if ok && old.mod.Equal(st.ModTime()) && old.size == st.Size() && old.at.Sub(old.mod) > racyDirectory {
		return old.entries
	}
	at := time.Now()
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	directoryCache.Lock()
	if len(directoryCache.entries) >= 16384 {
		directoryCache.entries = map[string]directoryEntry{}
	}
	directoryCache.entries[path] = directoryEntry{st.ModTime(), st.Size(), at, entries}
	directoryCache.Unlock()
	return entries
}

// SessionGlob is filepath.Glob for session discovery, with directory listings
// cached by metadata. It never caches file contents or suppresses new entries.
func SessionGlob(pattern string) ([]string, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if !strings.ContainsAny(pattern, "*?[") {
		if _, err := os.Lstat(pattern); err != nil {
			return nil, nil
		}
		return []string{pattern}, nil
	}
	dir, name := filepath.Split(pattern)
	dir = filepath.Clean(dir)
	parents, err := SessionGlob(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range parents {
		for _, e := range readDirectory(p) {
			if yes, _ := filepath.Match(name, e.Name()); yes {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	return out, nil
}
