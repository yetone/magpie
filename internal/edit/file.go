// Package edit provides format-preserving editors for the config files that
// coding agents keep: JSON/JSONC, TOML and YAML. Only the requested key
// changes; comments, ordering and indentation are left as they are.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Read returns the file contents, or (nil, nil) when the file does not exist.
func Read(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// WriteAtomic writes data to path via a temp file + rename so a crash can
// never leave a half-written config behind. File mode is preserved. When
// path is a symlink (a config kept in a dotfiles repo) the file it points
// at is written and the link stays; a file with other hard links is
// written in place, see writeInPlace. Once written, temp files earlier
// writes of path left behind go, see removeStaleTemps.
func WriteAtomic(path string, data []byte) error {
	path, err := Target(path)
	if err != nil {
		return err
	}
	if hardLinked(path) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			if err := writeInPlace(f, data); err != nil {
				return err
			}
			removeStaleTemps(path)
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
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
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
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	removeStaleTemps(path)
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

// staleTemp is how long a temp file beside a config goes untouched before
// the next write of that config takes it for one a crash left. A write
// takes milliseconds, so a temp file that old belongs to no write still
// going on.
const staleTemp = 10 * time.Minute

// removeStaleTemps removes the temp files that earlier writes of path left
// beside it when they crashed or failed part way, once a later write has
// gone through: the file then holds newer text than any of them. One
// younger than staleTemp stays, as another write may still be using it.
// Should a write stall longer than that (a laptop asleep mid-write) and
// lose its temp file here, its rename fails and leaves the file as it was,
// or, writing in place, it only loses the spare copy of its text.
func removeStaleTemps(path string) {
	dir, prefix := filepath.Dir(path), "."+filepath.Base(path)+"."
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		mid, ok := strings.CutPrefix(e.Name(), prefix)
		if ok {
			mid, ok = strings.CutSuffix(mid, ".tmp")
		}
		// os.CreateTemp puts digits for the *; anything else is another
		// file's, say .models.yml.bak.1.tmp is models.yml.bak's
		if !ok || mid == "" || strings.Trim(mid, "0123456789") != "" {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTemp {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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
