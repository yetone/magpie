// Package steady reads and replaces files that another process may be
// replacing at that moment. On Windows a file being renamed over refuses
// to open, and one held open refuses to be renamed over, both for a
// moment ("Access is denied"): read as none, a file of accounts is no
// accounts, and the next write leaves them out. A file being renamed over
// is also, for a moment (two milliseconds seen), not there at all, to
// another process or thread: Stat and ReadThere wait that out for a file
// that was there. Elsewhere a rename is atomic and these are os.Stat,
// os.ReadFile and os.Rename.
package steady

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// tries and pause bound the wait to half a second: what holds the file is
// another read or rename, done in milliseconds.
const (
	tries = 50
	pause = 10 * time.Millisecond
)

// goneTries and gonePause bound the wait on a file not there to 15ms or
// so: the moment a rename over it leaves is a couple of milliseconds, and
// a file that is really not there is waited on that long.
const (
	goneTries = 15
	gonePause = time.Millisecond
)

// errPathNotFound is Windows' ERROR_PATH_NOT_FOUND: a folder on the way to
// the file isn't there (ERROR_FILE_NOT_FOUND, 2, is the file alone).
const errPathNotFound = syscall.Errno(3)

// windows is whether files behave as Windows' do; a test sets it.
var windows = runtime.GOOS == "windows"

// stat and readFile are os.Stat and os.ReadFile; a test stands in for them.
var (
	stat     = os.Stat
	readFile = os.ReadFile
)

func retry(f func() error) error {
	err := f()
	for i := 0; err != nil && windows && !errors.Is(err, fs.ErrNotExist) && i < tries; i++ {
		time.Sleep(pause)
		err = f()
	}
	return err
}

// retryGone is retry, waiting out a moment of the file not there too,
// unless its folder isn't there either: nothing is being renamed into it.
// Most agents aren't installed, and the first look at each of their files
// waited the whole 15ms (#1665: ~22ms an agent each catalog sync, 2.6s a
// round of 116 on Windows).
func retryGone(f func() error) error {
	err := retry(f)
	for i := 0; err != nil && windows && errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errPathNotFound) && i < goneTries; i++ {
		time.Sleep(gonePause)
		err = retry(f)
	}
	return err
}

// Stat is os.Stat of a file that was there: one being replaced is waited
// for, so it isn't taken for gone. A file not there costs the wait, so a
// caller asking again remembers it gone (filememo does).
func Stat(p string) (fs.FileInfo, error) {
	var fi fs.FileInfo
	err := retryGone(func() (err error) {
		fi, err = stat(p)
		return err
	})
	return fi, err
}

// ReadThere is ReadFile of a file just found there (Stat): not there now is
// it being replaced, waited out.
func ReadThere(p string) ([]byte, error) {
	var b []byte
	err := retryGone(func() (err error) {
		b, err = readFile(p)
		return err
	})
	return b, err
}

// ReadThereInto is ReadThere into buf's room, grown when the file needs
// more: for a caller that reads one file again and again only to compare
// it, which a new buffer each time made the bulk of what magpie allocated
// as it started (a 1.5 MB config.toml read 150 times over).
func ReadThereInto(p string, buf []byte) ([]byte, error) {
	var b []byte
	err := retryGone(func() (err error) {
		b, err = readInto(p, buf[:0])
		return err
	})
	return b, err
}

// readInto is os.ReadFile into buf; a test stands in for it.
var readInto = func(p string, buf []byte) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// ReadFile is os.ReadFile, waiting out a file being replaced.
func ReadFile(p string) ([]byte, error) {
	var b []byte
	err := retry(func() (err error) {
		b, err = readFile(p)
		return err
	})
	return b, err
}

// Rename is os.Rename, waiting out a reader holding newpath open.
func Rename(oldpath, newpath string) error {
	return retry(func() error { return os.Rename(oldpath, newpath) })
}

// Sync is (*os.File).Sync; a test stands in for it to make a flush fail.
var Sync = (*os.File).Sync

// WriteFile replaces p with b, mode perm, through a temp file beside it
// that is flushed to the disk before it is renamed over p. A rename reaches
// the disk on its own schedule and the data behind it later: a Windows
// machine that went down between the two left logins.json and
// plugin-auth.json at their full length with every byte zero (#1505). A
// write or flush that fails removes the temp file and leaves p as it was.
// A write that is served takes the stale temps earlier writes left beside
// p when they died (RemoveStaleTemps) along with it.
func WriteFile(p string, b []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = Sync(tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = Rename(tmp.Name(), p)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	RemoveStaleTemps(p)
	return nil
}

// staleTemps is how long a temp file beside a file goes untouched before
// the next write of that file takes it for one a crash left. A write takes
// milliseconds, so a temp file that old belongs to no write still going on.
const staleTemps = 10 * time.Minute

// RemoveStaleTemps removes the temp files that earlier writes of path left
// beside it when they died or failed part way, once a later write has gone
// through: the file then holds newer text than any of them (#4938 — a
// start-up that exits mid-write left hundreds beside magpie's own files,
// and every file descriptor a directory watcher holds on them with it).
// One younger than staleTemps stays, as another write may still be using
// it. Should a write stall longer than that (a laptop asleep mid-write)
// and lose its temp file here, its rename fails and leaves the file as it
// was, or, writing in place, it only loses the spare copy of its text.
func RemoveStaleTemps(path string) {
	dir, prefix := filepath.Dir(path), "."+filepath.Base(path)+"."
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		mid, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok {
			continue
		}
		// os.CreateTemp puts digits for the *: this package's temps are
		// digits alone, edit.WriteAtomic's digits and ".tmp" (which the
		// reaping of one file's temps takes with the other's, the two
		// writers sharing the folder); anything else is another file's,
		// say .models.yml.bak.1.tmp is models.yml.bak's
		if t, hasTmp := strings.CutSuffix(mid, ".tmp"); hasTmp {
			mid = t
		}
		if mid == "" || strings.Trim(mid, "0123456789") != "" {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTemps {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
