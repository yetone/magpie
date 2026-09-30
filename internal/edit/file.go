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
// never leave a half-written config behind. File mode is preserved. A path
// that is a symlink keeps being one: the file it points at is the one
// replaced, so a config kept in a dotfiles repo and linked in stays linked.
func WriteAtomic(path string, data []byte) error {
	path, err := resolve(path)
	if err != nil {
		return err
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
	return nil
}

// Remove takes the file at path away. A symlink is the user's, not magpie's:
// it stays, and the file it points at is emptied instead.
func Remove(path string) error {
	real, err := resolve(path)
	if err != nil {
		return err
	}
	if real == path {
		return os.Remove(path)
	}
	if _, err := os.Stat(real); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return WriteAtomic(real, nil)
}

// resolve follows the symlinks at path to the file they end at, which need
// not exist yet. A path that isn't a symlink is itself.
func resolve(path string) (string, error) {
	for range 255 {
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		to, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(path), to)
		}
		path = to
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", path)
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
