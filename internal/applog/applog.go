// Package applog writes the desktop app's log to a file, as well as to
// stderr. The app opened from the Finder, the Dock, the Start menu or a
// desktop launcher has no terminal: what it logged went nowhere, so a user
// asked for magpie's log had none to send (rayem1w on X, a Codex "hit a
// snag" through the gateway, 0.1.2330). Only the launch agent
// (`magpie daemon install`, serve.log) and a `magpie tui` that left the
// gateway running (serve.log in magpie's cache) kept one.
//
// The file is app.log in appdir.Logs. It is the app's alone: the launch
// agent's serve.log, in the same folder on a Mac, is written by launchd for
// `magpie serve`, a process of its own. At 5 MiB app.log becomes
// app.old.log, over the one before, so two files at most are kept. A
// panic or fatal error's trace goes to the file too (debug.SetCrashOutput),
// so a crash is in it.
//
// What is logged names no key or token (the gateway logs header names,
// never values, and paths without their query); scrub masks what looks
// like one anyway, in case a vendor's error carries it.
package applog

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

const (
	// Name is the log's file name in appdir.Logs.
	Name = "app.log"
	// OldName is the log before the last rotation, kept beside it.
	OldName = "app.old.log"
	// Limit is the size at which the log is rotated.
	Limit = 5 << 20
)

// Path is where the desktop app writes its log.
func Path() string { return filepath.Join(appdir.Logs(), Name) }

// current is the log this process writes, "" when it writes none, and
// active its file.
var (
	current atomic.Pointer[string]
	active  atomic.Pointer[file]
)

// Current is the log this process writes, or "" when it writes none (the
// TUI, `magpie serve`, a command).
func Current() string {
	if p := current.Load(); p != nil {
		return *p
	}
	return ""
}

// Start sends the standard logger's output, and a crash's trace, to the
// log as well as to stderr, and logs that this magpie started. It answers
// the log's path; when the file can't be opened, the error, and the log
// goes to stderr alone as before.
func Start(version, how string) (string, error) {
	path := Path()
	w, err := open(path, Limit)
	if err != nil {
		return "", err
	}
	log.SetOutput(io.MultiWriter(quiet{os.Stderr}, w))
	if was := active.Swap(w); was != nil {
		was.Close()
	}
	current.Store(&path)
	log.Printf("magpie %s started (%s, pid %d, %s/%s); this log: %s", version, how, os.Getpid(), runtime.GOOS, runtime.GOARCH, path)
	return path, nil
}

// Stop sends the log to stderr alone again and closes the file: for
// tests, whose home is removed after them (Windows removes no folder with
// a file open in it).
func Stop() {
	log.SetOutput(os.Stderr)
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	current.Store(nil)
	if w := active.Swap(nil); w != nil {
		w.Close()
	}
}

// file is a log file rotated at limit bytes.
type file struct {
	mu    sync.Mutex
	path  string
	old   string
	limit int64
	f     *os.File
	size  int64
}

// open opens the log at path to append to, making its folder, readable by
// the user alone.
func open(path string, limit int64) (*file, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &file{path: path, old: filepath.Join(filepath.Dir(path), OldName), limit: limit}
	if err := w.reopen(false); err != nil {
		return nil, err
	}
	return w, nil
}

// reopen opens the file at w.path, emptied when truncate is set, and
// hands it to the runtime for a crash's trace.
func (w *file) reopen(truncate bool) error {
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(w.path, flag, 0o600)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	if fi, err := f.Stat(); err == nil {
		w.size = fi.Size()
	}
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	return nil
}

// Write writes p to the log, rotating it first when p would take it past
// its limit. It answers len(p) for what it wrote, after masking, so an
// io.MultiWriter goes on to stderr.
func (w *file) Write(p []byte) (int, error) {
	n := len(p)
	p = scrub(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, errors.New("applog: the log is closed")
	}
	if w.size > 0 && w.size+int64(len(p)) > w.limit {
		w.rotate()
	}
	m, err := w.f.Write(p)
	w.size += int64(m)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// rotate makes the log app.old.log and starts a new one. When another
// magpie rotated it already (an update's new process beside the old one),
// the log at w.path is already new and is only opened. When it can't be
// moved (Windows, while another process has it open), it is emptied, so
// it stays bounded either way.
func (w *file) rotate() {
	if at, err := os.Stat(w.path); err == nil {
		if mine, err := w.f.Stat(); err == nil && !os.SameFile(at, mine) {
			w.f.Close()
			if w.reopen(false) == nil {
				return
			}
		}
	}
	// the runtime holds a copy of the file for a crash's trace, and
	// Windows moves no file with a handle open
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	w.f.Close()
	moved := os.Rename(w.path, w.old)
	if err := w.reopen(moved != nil); err != nil {
		w.f = nil
		return
	}
	if moved != nil {
		n, _ := fmt.Fprintf(w.f, "applog: %s couldn't be kept as %s (%v): emptied instead\n", Name, OldName, moved)
		w.size += int64(n)
	}
}

// Close closes the log; for tests.
func (w *file) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// quiet is stderr that never fails a write: the app opened from Windows
// Explorer has no stderr at all, and io.MultiWriter stops at the first
// writer that fails.
type quiet struct{ w io.Writer }

func (q quiet) Write(p []byte) (int, error) {
	_, _ = q.w.Write(p)
	return len(p), nil
}

// secrets are what a key or token looks like in a line of text: a bearer
// or basic credential, a query parameter naming one, and the shapes
// vendors' keys have (sk-…, Google's AIza…, GitHub's gh?_…, a JWT). Each
// keeps its first characters, for telling which it was, and masks the
// rest.
var secrets = []struct {
	re   *lazyre.RE
	keep string
}{
	{lazyre.New(`(?i)\b((?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}…"},
	{lazyre.New(`(?i)([?&](?:key|api[_-]?key|apikey|access_token|refresh_token|id_token|token|auth|sig|signature|code)=)[^&\s"'<>]+`), "${1}…"},
	{lazyre.New(`\b(sk-(?:ant-|proj-)?)[A-Za-z0-9_-]{12,}`), "${1}…"},
	{lazyre.New(`\b(AIza)[0-9A-Za-z_-]{20,}`), "${1}…"},
	{lazyre.New(`\b(gh[opsur]_)[A-Za-z0-9]{20,}`), "${1}…"},
	{lazyre.New(`\b(eyJ)[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "${1}…"},
}

// scrub is p with whatever looks like a key or token masked.
func scrub(p []byte) []byte {
	for _, s := range secrets {
		p = s.re.ReplaceAll(p, []byte(s.keep))
	}
	return p
}
