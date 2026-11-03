// Package edit provides format-preserving editors for the config files that
// coding agents keep: JSON/JSONC, TOML and YAML. Only the requested key
// changes; comments, ordering and indentation are left as they are.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/steady"
)

// Read returns the file contents, or (nil, nil) when the file does not exist.
// A file unchanged since it was last read is not read again (the Agents
// page looks at each agent's files many times over), and what is returned
// is the caller's own to change. It is looked at even while a request
// holds what was read (filememo.Hold), as what is read here is what a
// write is made from: bytes held from a second ago would write back over
// what another app wrote since. A look that only reads goes through
// readHeld.
//
// A UTF-8 byte order mark at the start is not part of what is returned:
// Windows tools (PowerShell's UTF8 encoding, Notepad, Claude Code channel
// switchers) put one in front of an agent's settings.json or config.toml,
// which the agent reads through while a JSON or TOML parser refuses it
// (#1534). WriteAtomic puts it back, see keepBOM.
func Read(path string) ([]byte, error) {
	return readThrough(path, filememo.ReadNow[[]byte])
}

// readHeld is Read for a look that writes nothing (GetJSON, GetTOMLTop):
// while a request holds what was read, a file found unchanged within the
// second is not looked at again (#1665: one catalog sync looked at an
// agent's settings.json hundreds of times).
func readHeld(path string) ([]byte, error) {
	return readThrough(path, filememo.Read[[]byte])
}

func readThrough(path string, through func(string, string, func([]byte) ([]byte, error)) ([]byte, error)) ([]byte, error) {
	b, err := through("bytes", path, func(b []byte) ([]byte, error) { return b, nil })
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.Clone(TrimBOM(b)), nil
}

// jsonHeld is readHeld of a JSON or JSONC file made plain JSON (comments
// and trailing commas blanked out), made once each time the file is read
// and shared by every look, so read only. GetJSON copied the file and
// converted it again on each look: 80 MB of what twenty window polls
// allocated.
func jsonHeld(path string) ([]byte, error) {
	b, err := filememo.Read("plain json", path, func(b []byte) ([]byte, error) {
		return jsonc.ToJSON(TrimBOM(b)), nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// bom is the UTF-8 byte order mark.
var bom = []byte("\xef\xbb\xbf")

// TrimBOM is b without a UTF-8 byte order mark at its start, for a config
// read some other way than Read.
func TrimBOM(b []byte) []byte { return bytes.TrimPrefix(b, bom) }

// keepBOM is data with the byte order mark the file at path starts with,
// when it starts with one and data doesn't: the user's file keeps the
// encoding it was saved in, for the tools that wrote it that way, and the
// agent read it with the mark before.
func keepBOM(path string, data []byte) []byte {
	if len(data) == 0 || bytes.HasPrefix(data, bom) {
		return data
	}
	f, err := os.Open(path)
	if err != nil {
		return data
	}
	defer f.Close()
	head := make([]byte, len(bom))
	if n, _ := io.ReadFull(f, head); n < len(bom) || !bytes.Equal(head, bom) {
		return data
	}
	return append(append(make([]byte, 0, len(bom)+len(data)), bom...), data...)
}

// filter, while Filtered runs, is given each file WriteAtomic is about to
// write and returns what is written in its place.
var (
	filter    atomic.Pointer[func(path string, data []byte) []byte]
	filtering sync.Mutex
)

// Filtered runs fn with every file written through this package passed
// through f first: f gets the path (a link's target) and the bytes about to
// be written, and returns the bytes to write. Writes from elsewhere while
// fn runs pass through f too, so f changes only what it is sure of, and
// waits on nothing: it runs under whatever lock the writer holds
// (settings.Save's, which settings.Load waits on). One Filtered runs at a
// time.
func Filtered(f func(path string, data []byte) []byte, fn func() error) error {
	filtering.Lock()
	defer filtering.Unlock()
	filter.Store(&f)
	defer filter.Store(nil)
	return fn()
}

// WriteAtomic writes data to path via a temp file + rename so a crash can
// never leave a half-written config behind. File mode and group are
// preserved. When path is a symlink (a config kept in a dotfiles repo) the
// file it points at is written and the link stays; a file with other hard
// links is written in place, see writeInPlace. Once written, temp files
// earlier writes of path left behind go, see steady.RemoveStaleTemps.
func WriteAtomic(path string, data []byte) error {
	path, err := Target(path)
	if err != nil {
		return err
	}
	if f := filter.Load(); f != nil {
		data = (*f)(path, data)
	}
	data = keepBOM(path, data)
	// A file that already holds these bytes is left alone: a new mtime
	// wakes every watcher of it, and a rename swaps the file under an
	// agent reading it, for nothing (#3870).
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	// read again, where a request holds it; a file left alone above keeps
	// what was held of every file (a catalog sync writes each agent's
	// files each round, almost always as they are)
	defer filememo.Forget()
	if hardLinked(path) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			if err := writeInPlace(f, data); err != nil {
				return err
			}
			steady.RemoveStaleTemps(path)
			return nil
		}
		// A file that can't be opened for writing (mode 0444, or read-only
		// on Windows) is renamed over as before hard links were written in
		// place, which splits this name from the others; opened first, so
		// nothing is made beside it on the way.
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}
	}
	mode := fs.FileMode(0o644)
	st, statErr := os.Stat(path)
	if statErr == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	// a folder an agent's variable names is never made again once gone
	// (appdir.Gone): the agent no longer reads it
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := appdir.Gone(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if statErr == nil {
		keepGroup(tmp, st)
	}
	// on the disk before it is renamed in: a machine that goes down
	// between the two can leave the file at its length, all zero (#1505)
	if err := steady.Sync(tmp); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := steady.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	steady.RemoveStaleTemps(path)
	return nil
}

// writeInPlace writes data into f, opened for writing, and closes it: for
// a file that has other names too (a hard link, as Orca keeps omp's
// models.yml in its overlay), where a new file renamed over one name would
// split it from the others, each left with its own text. Every name sees
// the write, so does a copy a backup tool made as a hard link (rsnapshot,
// cp -al, rsync --link-dest): that copy changes with it. It is the trade
// vim makes with backupcopy=auto, which also writes a file with other
// links in place: the link the user made is kept over such a backup.
//
// Writing in place is not atomic, so the text first goes to a temp file
// beside it, synced to disk, and only then into the file: a crash part way
// leaves the whole new text in that temp file, and a write that fails part
// way keeps it there and names it in the error. The file keeps its mode,
// its owner and all its names.
func writeInPlace(f *os.File, data []byte) error {
	path := f.Name()
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		f.Close()
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err != nil {
		f.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	// Written over and then cut to length, so while it is under way a
	// reader can see the new text's head over the old text, or, when the
	// new text is shorter, the whole new text with the old one's tail after
	// it. Emptying the file first would not close that window, only change
	// what shows through it: an empty or half-written file, which a tool
	// may load as a config with nothing set and act on, where a mangled one
	// fails to parse. Written over, a text no longer than the old also
	// needs no more room on the disk.
	_, err = f.Write(data)
	if err == nil {
		err = f.Truncate(int64(len(data)))
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return fmt.Errorf("%w; the new text is kept in %s", err, tmp.Name())
	}
	_ = os.Remove(tmp.Name())
	return nil
}

// Target is the file a write to path should replace: path itself, or,
// when path is a symlink, the file at the end of its links — the path it
// names even when nothing is there yet. Renaming over the link instead
// would turn it into a file of its own and leave its target as it was.
func Target(path string) (string, error) {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p, nil
	}
	p := path
	for range 255 {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return p, nil
		}
		dest, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			// not Join: its cleaning would take a ".." past a linked folder
			dest = filepath.Dir(p) + string(filepath.Separator) + dest
		}
		p = dest
	}
	return "", fmt.Errorf("%s: too many links", path)
}

// IsLink reports whether path is a symlink.
func IsLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// Remove takes away a file magpie has emptied. A symlink stays and the
// file it points at is emptied instead, when there is one: deleting the
// link would leave the old text in its target. A file with other hard
// links is emptied and kept for the same reason, so every name comes out
// empty: with AGENTS.md and CLAUDE.md one file under two names, clearing
// AGENTS.md empties CLAUDE.md too, where deleting the one name would leave
// the text magpie is taking back under the other.
func Remove(path string) error {
	if !IsLink(path) && !hardLinked(path) {
		return os.Remove(path)
	}
	if _, err := os.Stat(path); err != nil {
		return nil // points at nothing: nothing to empty
	}
	return WriteAtomic(path, nil)
}

// Atomically runs fn, which may write the files at paths in several steps,
// and puts each of them back as it was — its bytes and mode, or its absence
// — when fn fails, so an edit that fails part way leaves no file half made.
// A file that can't be read beforehand is left to fn as it is.
func Atomically(fn func() error, paths ...string) error {
	type saved struct {
		path   string
		data   []byte
		mode   fs.FileMode
		exists bool
	}
	var before []saved
	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if t, err := Target(p); err == nil {
				p = t // a link to nothing yet stays, only what fn made goes
			}
			before = append(before, saved{path: p})
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		before = append(before, saved{p, b, st.Mode().Perm(), true})
	}
	err := fn()
	if err == nil {
		return nil
	}
	for _, s := range before {
		now, rerr := os.ReadFile(s.path)
		switch {
		case !s.exists:
			if !errors.Is(rerr, fs.ErrNotExist) {
				if e := os.Remove(s.path); e != nil && !errors.Is(e, fs.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
				}
			}
		case rerr != nil || !bytes.Equal(now, s.data):
			if e := WriteAtomic(s.path, s.data); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			} else if e := os.Chmod(s.path, s.mode); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			}
		}
	}
	return err
}
