package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Looking a command up on PATH stats every candidate name in every PATH
// folder: on Windows 47 folders × PATHEXT's 11 extensions took 111ms for
// one that isn't there, and the pages asked once per known agent (59) and
// once per Pi session (297) on every request, so every page's first load
// took seconds and Sessions half a minute (#1771). LookPath and FindTool
// keep their answers while the folders they look in are unchanged: a tool
// installed or removed changes its folder's modification time, a changed
// PATH is another key, and Forget (the Agents page's refresh) looks again
// at once.

// lookFresh is how long the folders' stamp is trusted before it is taken
// again; within it an answer kept is given without a stat.
const lookFresh = 2 * time.Second

// lookSettle is how long after a folder changed its modification time is
// trusted to show the next change. The clocks that stamp a folder are
// coarse: Linux's ticks every few milliseconds and Windows's every 15.6ms, so
// a tool installed in the same tick as the change before it leaves the
// folder's time as it was, and a stamp taken between the two would keep the
// first answer until something else changed the folder. A stamp naming a
// folder changed within lookSettle is taken again at the next lookup, and
// nothing looked up under it is kept (git treats its "racy" index entries
// the same way).
// shortcut: filesystems with timestamp ticks over 100ms need a longer window.
const lookSettle = 100 * time.Millisecond

// lookNow lets the same-tick regression hold the clock still.
var lookNow = time.Now

// lookFreshInTests is lookFresh in a test binary testenv isolated: none,
// so a test that installs a tool finds it at once, unless a test says.
var lookFreshInTests time.Duration

var looks struct {
	sync.Mutex
	gen     int
	env     string // PATH and PATHEXT the stamp was taken with
	stamp   string
	checked time.Time
	m       map[string]lookAnswer
}

type lookAnswer struct {
	path string
	err  error
}

// LookPath is exec.LookPath, with a version manager's wrapper script found
// there given as the program it runs (Unwrap). Where it is on PATH is kept
// while the PATH folders are unchanged (Look); Unwrap keeps its own answer
// by the script's size and time.
func LookPath(name string) (string, error) {
	p, err := Look(name)
	if err != nil {
		return p, err
	}
	return Unwrap(name, p), nil
}

// Look is exec.LookPath as it is, a wrapper script given as itself, the
// answer kept while the PATH folders are unchanged.
func Look(name string) (string, error) {
	a := looked("look\x00"+name, func() lookAnswer {
		p, err := lookPath(name)
		return lookAnswer{p, err}
	})
	return a.path, a.err
}

// Forget drops every answer kept by LookPath and FindTool, so the next
// asks again: the reader asked to look for agents again.
func Forget() {
	looks.Lock()
	looks.m, looks.env, looks.stamp, looks.checked = nil, "", "", time.Time{}
	looks.gen++
	looks.Unlock()
	forgetListings()
}

// looked is the answer kept for key, or ask's, kept.
func looked(key string, ask func() lookAnswer) lookAnswer {
	env := os.Getenv("PATH") + "\x00" + os.Getenv("PATHEXT")
	looks.Lock()
	if looks.env != env || looks.checked.IsZero() || time.Since(looks.checked) >= lookWindow() {
		looks.Unlock()
		stamp, settled := lookStamp() // a few stats, not under the lock
		looks.Lock()
		if !settled {
			stamp = "" // keeps nothing; the zero checked takes it again next time
		}
		if stamp != looks.stamp || stamp == "" {
			looks.m, looks.stamp = nil, stamp
		}
		looks.env, looks.checked = env, time.Now()
		if !settled {
			looks.checked = time.Time{}
		}
	}
	if a, ok := looks.m[key]; ok {
		looks.Unlock()
		return a
	}
	gen, stamp := looks.gen, looks.stamp
	looks.Unlock()
	a := ask()
	looks.Lock()
	// an answer asked across a Forget or a change of the folders, or under
	// a stamp not settled, isn't kept
	if looks.gen == gen && looks.stamp == stamp && stamp != "" {
		if looks.m == nil {
			looks.m = map[string]lookAnswer{}
		}
		looks.m[key] = a
	}
	looks.Unlock()
	return a
}

// lookWindow is lookFresh, or lookFreshInTests in a test binary.
func lookWindow() time.Duration {
	if Isolated() {
		return lookFreshInTests
	}
	return lookFresh
}

// lookStamp names what the answers depend on: PATH, PATHEXT, and each
// folder looked in with its modification time. It isn't settled while a
// folder changed within lookSettle. A time ahead of the clock is settled:
// the next change sets it to now, which the stamp tells apart.
func lookStamp() (string, bool) {
	now, settled := lookNow(), true
	var b strings.Builder
	b.WriteString(os.Getenv("PATH"))
	b.WriteByte(0)
	b.WriteString(os.Getenv("PATHEXT"))
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, findDirs()...)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		b.WriteByte(0)
		b.WriteString(d)
		if st, err := os.Stat(d); err == nil {
			b.WriteByte('@')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 36))
			if d := now.Sub(st.ModTime()); d >= 0 && d < lookSettle {
				settled = false
			}
		}
	}
	return b.String(), settled
}

// findDirs are the folders FindTool looks in after PATH.
func findDirs() []string {
	dirs := UserBinDirs()
	if runtime.GOOS == "windows" {
		dirs = append(dirs, SystemDirs(LoginPath()...)...)
	}
	return dirs
}
