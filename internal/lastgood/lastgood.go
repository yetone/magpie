// Package lastgood keeps the last good generation of magpie's own state
// files (accounts, sign-ins, providers, settings) and reads it back when
// the file itself can't be read.
//
// A Windows machine that went down mid-write left logins.json and
// plugin-auth.json at their full length with every byte zero (#1505).
// magpie then read no accounts, and its next write made that final. Here a
// file that is there but not valid is unknown, never empty: before each
// write the file there now is kept as <file>.bak when it is valid, or
// copied aside as <file>.bad-<time> when it isn't, and a read of a file
// that isn't valid gives the .bak's contents instead, noted for the GUI and
// the log.
package lastgood

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/steady"
)

// Valid is whether b is a good copy of a file's contents.
type Valid func(b []byte) bool

// JSON is a file that holds one JSON value: a file of zero bytes, empty or
// whitespace only, or cut short is not.
func JSON(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	return len(bytes.TrimSpace(b)) > 0 && json.Valid(b)
}

// Bak is where path's last good generation is kept.
func Bak(path string) string { return path + ".bak" }

// ErrUnreadable is a file that is there but neither it nor its .bak is
// valid: unknown, never empty.
var ErrUnreadable = errors.New("unreadable")

// UnreadableError says which file, and why it didn't read.
type UnreadableError struct {
	Path string
	Err  error
}

func (e *UnreadableError) Error() string {
	return fmt.Sprintf("%s can't be read (%v) and has no good backup", e.Path, e.Err)
}

func (e *UnreadableError) Unwrap() []error { return []error{ErrUnreadable, e.Err} }

// Read gives path's contents when they are valid. A file there that isn't
// gives its .bak's contents, when that is valid, and the recovery is noted
// (Recovered). A file not there is fs.ErrNotExist, its .bak not looked at:
// a crash leaves a file renamed over as it was or as it became, never
// gone, so one gone was removed on purpose. Anything else is an
// *UnreadableError, or the error that stopped the read.
func Read(path string, valid Valid) ([]byte, error) {
	b, err := steady.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if valid(b) {
		return b, nil
	}
	return Fallback(path, b, valid)
}

// Fallback is Read for a caller that read path itself and found bad (its
// contents, or nil when only its parse said so): the .bak's contents when
// they are valid, noted, else an *UnreadableError.
func Fallback(path string, bad []byte, valid Valid) ([]byte, error) {
	why := describe(bad)
	if b, err := steady.ReadFile(Bak(path)); err == nil && valid(b) {
		note(path, why)
		return b, nil
	}
	return nil, &UnreadableError{Path: path, Err: errors.New(why)}
}

// describe says what is wrong with a bad file, for the log and the GUI.
func describe(b []byte) string {
	switch {
	case b == nil:
		return "it doesn't parse"
	case len(bytes.Trim(b, "\x00")) == 0 && len(b) > 0:
		return strconv.Itoa(len(b)) + " bytes, every one zero"
	case len(bytes.TrimSpace(b)) == 0:
		return "it is empty"
	}
	return "it doesn't parse"
}

// Keep is called before path is replaced, under the lock its writer
// holds: the file there now, when valid, becomes the last good generation,
// path.bak, written to the disk before path is; one that isn't valid is
// copied aside (path.bad-<time>) and the .bak left as it is, so a file
// that can't be read is never written over before a copy of it is kept.
// A file not there needs neither. An error stops the write: the file there
// couldn't be read, or a bad one couldn't be kept.
func Keep(path string, valid Valid, perm fs.FileMode) error {
	b, err := steady.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if valid(b) {
		if old, err := os.ReadFile(Bak(path)); err == nil && bytes.Equal(old, b) {
			return nil
		}
		return steady.WriteFile(Bak(path), b, perm)
	}
	_, err = KeepBad(path, b)
	return err
}

// ErrEdited is a file the user edits by hand that is there, not empty
// and not left all zero by a crash, and doesn't parse: an edit made
// outside magpie, half done, or saved with a slip in it. It is theirs,
// never written over with what magpie had in memory.
var ErrEdited = errors.New("edited outside magpie and doesn't parse")

// EditedError says which file, and why it doesn't parse.
type EditedError struct {
	Path string
	Why  string
}

func (e *EditedError) Error() string {
	return fmt.Sprintf("%s doesn't parse (%s), as edited outside magpie; magpie left it as it is and runs on its last good copy until it is fixed: fix it, or move it aside", e.Path, e.Why)
}

func (e *EditedError) Unwrap() error { return ErrEdited }

// KeepEdited is Keep for a file people edit by hand (providers.json,
// settings.json): one there that isn't valid, and is neither empty nor
// every byte zero, is an *EditedError and nothing is written; magpie's
// own writes go through a renamed temp file, so a bad file it didn't
// leave is someone's edit, and writing the last good generation and the
// change over it lost that edit (sarosaka1023 on X). An empty or zeroed
// one, which a crash leaves (#1505), is kept aside and written over as
// Keep does.
func KeepEdited(path string, valid Valid, perm fs.FileMode) error {
	b, err := steady.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !valid(b) && Edited(b) {
		return &EditedError{Path: path, Why: why(b)}
	}
	return Keep(path, valid, perm)
}

// Edited is whether b, a file's contents that aren't valid, are someone's
// edit rather than what a crash leaves: not empty, not every byte zero,
// nor only an editor's byte-order mark.
func Edited(b []byte) bool {
	b = bytes.TrimPrefix(bytes.Trim(b, " \t\r\n\x00"), []byte("\xef\xbb\xbf"))
	return len(bytes.Trim(b, " \t\r\n\x00")) > 0
}

// why is what doesn't parse in b, as JSON's decoder says it.
func why(b []byte) string {
	var v any
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &v); err != nil {
		return err.Error()
	}
	return describe(b)
}

// Into is a file that unmarshals into a T: what a reader that takes any
// other file as empty can read. `[]` is valid JSON but not a map, so a
// reader of a map would see nothing in it.
func Into[T any](b []byte) bool {
	var v T
	return json.Unmarshal(b, &v) == nil
}

// KeepUnparsed is called before path is written over, by a writer whose
// reader takes a file that doesn't parse (parses false) as empty: such a
// file is copied aside (KeepBad) first, so the write of what was read as
// empty doesn't lose what it held. A file not there, or empty, needs
// nothing. An error stops the write: the file there couldn't be read, or
// couldn't be kept.
func KeepUnparsed(path string, parses Valid) error {
	b, err := steady.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 || parses(b) {
		return nil
	}
	_, err = KeepBad(path, b)
	return err
}

// now is time.Now; a test stands in for it.
var now = time.Now

// KeepBad copies b, path's contents that aren't valid, aside as
// path.bad-<time> and gives that name. Two bad files within one second
// are both kept (.bad-<time>-2, …); the same bad bytes kept already,
// under any time, are not kept again.
func KeepBad(path string, b []byte) (string, error) {
	if name := keptAlready(path, b); name != "" {
		return name, nil
	}
	stamp := path + ".bad-" + now().Format("20060102-150405")
	for i := 1; i < 1000; i++ {
		name := stamp
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(name); err == nil {
			continue
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			log.Printf("%s can't be read and couldn't be kept: %v", filepath.Base(path), err)
			return "", err
		}
		_, err = f.Write(b)
		if err == nil {
			err = steady.Sync(f)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(name)
			log.Printf("%s can't be read and couldn't be kept: %v", filepath.Base(path), err)
			return "", err
		}
		log.Printf("%s can't be read (%s); it is kept as %s", filepath.Base(path), describe(b), filepath.Base(name))
		return name, nil
	}
	return "", fmt.Errorf("%s can't be read and there is no free name to keep it under", filepath.Base(path))
}

// keptAlready gives the name of a copy of b kept aside for path before,
// whatever second it was kept in, or "" when there is none.
func keptAlready(path string, b []byte) string {
	names, _ := filepath.Glob(globEscape(path) + ".bad-*")
	for _, name := range names {
		if fi, err := os.Stat(name); err != nil || fi.Size() != int64(len(b)) {
			continue
		}
		if old, err := os.ReadFile(name); err == nil && bytes.Equal(old, b) {
			return name
		}
	}
	return ""
}

// globEscape quotes the characters filepath.Glob treats as a pattern, so a
// folder named with [ or * is matched as it is.
func globEscape(s string) string {
	if runtime.GOOS == "windows" {
		return strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]").Replace(s)
	}
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "*", `\*`, "?", `\?`).Replace(s)
}

// A Note is one file read back from its last good generation.
type Note struct {
	File string    `json:"file"` // the file's name: logins.json
	Path string    `json:"path"`
	Why  string    `json:"why"` // what was wrong with it
	Bak  time.Time `json:"bak"` // when the .bak was written
	At   time.Time `json:"at"`
}

var (
	notesMu sync.Mutex
	notes   []Note
)

// note records that path was read from its .bak, once per path until
// Forget: a file read hundreds of times over is logged once.
func note(path, why string) {
	notesMu.Lock()
	defer notesMu.Unlock()
	if slices.ContainsFunc(notes, func(n Note) bool { return n.Path == path }) {
		return
	}
	n := Note{File: filepath.Base(path), Path: path, Why: why, At: now()}
	if fi, err := os.Stat(Bak(path)); err == nil {
		n.Bak = fi.ModTime()
	}
	notes = append(notes, n)
	log.Printf("%s can't be read (%s); what it held at %s is used, from %s", n.File, why, n.Bak.Local().Format("2006-01-02 15:04:05"), filepath.Base(Bak(path)))
}

// Noted records a recovery another process made (the plugin host's of
// plugin-auth.json).
func Noted(path, why string) { note(path, why) }

// Recovered are the files read back from their last good generation since
// magpie started, for the GUI to tell the user.
func Recovered() []Note {
	notesMu.Lock()
	defer notesMu.Unlock()
	return slices.Clone(notes)
}

// Reset forgets the notes; for tests.
func Reset() {
	notesMu.Lock()
	notes = nil
	notesMu.Unlock()
}
