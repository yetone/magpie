package testenv

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// Program writes script, a `#!` script, at path as a program a test runs,
// where os.WriteFile(path, script, 0o755) would. On macOS the file is one
// the system has already let run: macOS checks a newly written executable
// before its first exec, a quarter of a second alone and several seconds
// while other new programs start (a go test ./... starting its test
// binaries), which ate the wait of every test that times a fake's answer
// (TestDevinSignedInOnlyInMagpie, TestClaudeRunEndsWhenTheClientLeaves).
// The check is made once a file: one already run keeps its pass when it is
// renamed or written over in place. So Program takes a file that has run
// from a pool, renames it to path and writes the script into it; when the
// test ends, the file goes back to the pool for the next test.
func Program[S ~string | ~[]byte](t testing.TB, path string, script S) {
	t.Helper()
	_, err := os.Lstat(path)
	existed := err == nil
	if err := WriteProgram(path, script); err != nil {
		t.Fatal(err)
	}
	// a file the test only wrote over (a stand-in TestMain made) stays,
	// as it would have
	if !existed {
		t.Cleanup(func() { reclaim(path) })
	}
}

// WriteProgram is Program for a fake that lasts the whole test binary, made
// from TestMain, or written by a stand-in for the code under test: it is
// not taken back when a test ends.
func WriteProgram[S ~string | ~[]byte](path string, script S) error {
	b := []byte(script)
	if !bytes.HasPrefix(b, []byte("#!")) {
		// a binary written into a file that passed as a script is no test's
		return fmt.Errorf("testenv.Program: %s is no #! script", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return os.WriteFile(path, b, 0o755)
	}
	if !ran(path) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := warmAt(path); err != nil {
			return err
		}
	}
	// written over in place: the file keeps the pass of its first run
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// RemovePrograms deletes the pool, after the tests: Run does it, and a
// TestMain of its own calls it.
func RemovePrograms() {
	warming.Wait()
	pool.Lock()
	defer pool.Unlock()
	if pool.dir != "" {
		os.RemoveAll(pool.dir)
	}
	pool.dir, pool.free, pool.ran = "", nil, nil
}

// pool holds the files that have run once: the free ones in dir, and in ran
// each one at a program's path.
var pool struct {
	sync.Mutex
	dir  string
	n    int
	free []string
	ran  map[string]os.FileInfo
}

// ran says whether path is a file that has run once, put there by
// WriteProgram and still in place.
func ran(path string) bool {
	pool.Lock()
	fi, ok := pool.ran[path]
	pool.Unlock()
	now, err := os.Stat(path)
	return ok && err == nil && os.SameFile(fi, now)
}

// warmAt puts a file that has run once at path: a free one from the pool,
// or a new one run there now.
func warmAt(path string) error {
	pool.Lock()
	for len(pool.free) > 0 {
		f := pool.free[0]
		pool.free = pool.free[1:]
		if os.Rename(f, path) == nil {
			pool.Unlock()
			return record(path)
		}
		os.Remove(f) // another file system: the pool is no help here
	}
	pool.Unlock()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		return err
	}
	// run as a test's code runs it (proc.Command is out of reach: proc's
	// own tests use this package), on macOS only, so no console to hide
	p, err := os.StartProcess(path, []string{path}, &os.ProcAttr{})
	if err != nil {
		return fmt.Errorf("testenv.Program: a first run of %s: %v", path, err)
	}
	if st, err := p.Wait(); err != nil || !st.Success() {
		return fmt.Errorf("testenv.Program: a first run of %s: %v %v", path, st, err)
	}
	return record(path)
}

// record notes path as a file that has run once.
func record(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	pool.Lock()
	defer pool.Unlock()
	if pool.ran == nil {
		pool.ran = map[string]os.FileInfo{}
	}
	pool.ran[path] = fi
	return nil
}

// reclaim takes path back into the pool when it is still the file Program
// put there: one the code under test replaced, or that is gone, is not.
func reclaim(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	pool.Lock()
	defer pool.Unlock()
	fi, ok := pool.ran[path]
	delete(pool.ran, path)
	if now, err := os.Stat(path); !ok || err != nil || !os.SameFile(fi, now) {
		return
	}
	if pool.dir == "" {
		d, err := os.MkdirTemp("", "magpie-test-programs-")
		if err != nil {
			return
		}
		pool.dir = d
	}
	pool.n++
	f := filepath.Join(pool.dir, strconv.Itoa(pool.n))
	if os.Rename(path, f) == nil {
		pool.free = append(pool.free, f)
	}
}
