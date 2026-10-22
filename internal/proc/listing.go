package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// A command that isn't installed was looked for with a stat of every
// name it could have (11 PATHEXT extensions on Windows) in every folder
// of PATH and of findDirs: hundreds of stats a command, and the Library
// and Agents pages ask for about a hundred agents' commands. The answers
// are kept (looked), but every one of them is asked again when any of
// those folders changes, and at the app's start, so the first change on
// the Library page after a start, or after some folder on PATH changed
// while it sat idle, took 5–8s on a Windows machine with 11 agents and an
// antivirus that slows each stat (#1624, Hipple). Each folder's listing is
// read once instead and kept while its modification time is unchanged: a
// command none of them has a name for isn't there, without a stat of its
// own; one that may be is looked for as before.

// listingRead is how many folders were read, and execLookPath what looks
// for a command that may be there; the tests count both.
var (
	listingRead  int
	execLookPath = exec.LookPath
)

var listings struct {
	sync.Mutex
	m map[string]*listing
}

type listing struct {
	mod     time.Time
	checked time.Time
	// names are the folder's entries, lower-cased, each also as it is up
	// to each dot in it: claude.exe is there as "claude" and "claude.exe"
	names map[string]bool
}

// mayBeIn is whether name could be a program in one of dirs: false only
// when every folder was read and none has an entry name could be (name,
// or name and an extension, in any case). A folder that can't be read
// may have it.
func mayBeIn(name string, dirs []string) bool {
	if name == "" || strings.ContainsAny(name, `/\:`) {
		return true
	}
	key := strings.ToLower(name)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			abs, err := filepath.Abs(d)
			if err != nil {
				return true
			}
			d = abs
		}
		names, ok := listed(d)
		if !ok || names[key] {
			return true
		}
	}
	return false
}

// listed is the names in dir, read again when its modification time
// changed (checked once a lookWindow); false when it can't be read. A
// folder changed within lookSettle is read and not kept: what is
// installed in the same clock tick leaves its time as it was (lookStamp).
func listed(dir string) (map[string]bool, bool) {
	now := lookNow()
	listings.Lock()
	l := listings.m[dir]
	if l != nil && now.Sub(l.checked) < lookWindow() {
		listings.Unlock()
		return l.names, true
	}
	listings.Unlock()
	st, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		// nothing is in a folder that isn't there
		listings.Lock()
		if listings.m == nil {
			listings.m = map[string]*listing{}
		}
		listings.m[dir] = &listing{checked: now}
		listings.Unlock()
		return nil, true
	}
	if err != nil || !st.IsDir() {
		return nil, false
	}
	if l != nil && l.names != nil && l.mod.Equal(st.ModTime()) {
		listings.Lock()
		l.checked = now
		listings.Unlock()
		return l.names, true
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	names := make(map[string]bool, len(es))
	for _, e := range es {
		n := strings.ToLower(e.Name())
		names[n] = true
		for i := 1; i < len(n); i++ {
			if n[i] == '.' {
				names[n[:i]] = true
			}
		}
	}
	listings.Lock()
	listingRead++
	if d := now.Sub(st.ModTime()); d >= 0 && d < lookSettle {
		delete(listings.m, dir)
	} else {
		if listings.m == nil {
			listings.m = map[string]*listing{}
		}
		listings.m[dir] = &listing{mod: st.ModTime(), checked: now, names: names}
	}
	listings.Unlock()
	return names, true
}

// pathDirs are the folders exec.LookPath looks in: PATH's (an empty
// entry the current folder, but on Windows), and on Windows the current
// folder first unless NoDefaultCurrentDirectoryInExePath is set.
func pathDirs() []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	if runtime.GOOS != "windows" {
		// an empty entry is the current folder there
		for i, d := range dirs {
			if d == "" {
				dirs[i] = "."
			}
		}
	} else {
		if _, off := os.LookupEnv("NoDefaultCurrentDirectoryInExePath"); !off {
			dirs = append([]string{"."}, dirs...)
		}
	}
	return dirs
}

// lookPath is exec.LookPath, with a command no folder of PATH has a name
// for answered as not found without looking.
func lookPath(name string) (string, error) {
	if !mayBeIn(name, pathDirs()) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return execLookPath(name)
}

// forgetListings drops the folders read.
func forgetListings() {
	listings.Lock()
	listings.m = nil
	listings.Unlock()
}
