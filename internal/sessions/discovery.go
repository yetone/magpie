package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
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

// readDirectory is path's entries. A folder that isn't there lists as none.
// One that couldn't be read (a stat or listing refused: too many files open
// under a long history's parse, a sharing violation while an antivirus or
// a sync client holds it, a network home that stalled) is unknown, not
// empty: its last good listing stands. Taken for none, every session in it
// left the Sessions page, and its parses were let go with them, until a
// later poll read the folder again and parsed them anew.
func readDirectory(path string) []os.DirEntry {
	mod, size, dir, err := appdir.DirStat(path)
	if err != nil {
		return unreadDirectory(path, err)
	}
	if !dir {
		return nil
	}
	directoryCache.Lock()
	old, ok := directoryCache.entries[path]
	directoryCache.Unlock()
	if ok && old.mod.Equal(mod) && old.size == size && old.at.Sub(old.mod) > racyDirectory {
		return old.entries
	}
	at := time.Now()
	entries, err := os.ReadDir(path)
	if err != nil {
		if kept := unreadDirectory(path, err); kept != nil {
			return kept
		}
		return nil
	}
	entries = compactEntries(path, entries)
	directoryCache.Lock()
	if len(directoryCache.entries) >= 16384 {
		directoryCache.entries = map[string]directoryEntry{}
	}
	directoryCache.entries[path] = directoryEntry{mod, size, at, entries}
	directoryCache.Unlock()
	return entries
}

// keptEntry is a kept listing's entry. A listing is kept as long as its
// folder doesn't change, ~16k of them across a long Claude Code history, so
// they sit in one array its entries point into: os.ReadDir gives each its
// own allocation, and the slice it returns room to grow (#2278).
type keptEntry struct {
	dir, name string
	typ       fs.FileMode
}

func (e *keptEntry) Name() string               { return e.name }
func (e *keptEntry) IsDir() bool                { return e.typ.IsDir() }
func (e *keptEntry) Type() fs.FileMode          { return e.typ }
func (e *keptEntry) Info() (fs.FileInfo, error) { return os.Lstat(filepath.Join(e.dir, e.name)) }
func (e *keptEntry) String() string             { return fs.FormatDirEntry(e) }

// compactEntries is entries, read from dir, held as keptEntry. Not on
// Windows, whose entries carry what Info answers and keptEntry would stat for.
func compactEntries(dir string, entries []os.DirEntry) []os.DirEntry {
	if runtime.GOOS == "windows" {
		return entries
	}
	all := make([]keptEntry, len(entries))
	out := make([]os.DirEntry, len(entries))
	for i, e := range entries {
		all[i] = keptEntry{dir, e.Name(), e.Type()}
		out[i] = &all[i]
	}
	return out
}

// unreadDirectory is what a folder lists as when reading it failed with
// err: nothing when it is gone, else the last good listing, if there is
// one.
func unreadDirectory(path string, err error) []os.DirEntry {
	if errors.Is(err, fs.ErrNotExist) {
		directoryCache.Lock()
		delete(directoryCache.entries, path)
		directoryCache.Unlock()
		return nil
	}
	directoryCache.Lock()
	defer directoryCache.Unlock()
	return directoryCache.entries[path].entries
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
