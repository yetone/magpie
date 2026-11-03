package library

// Reading the Library page follows the links on the way to a few hundred
// paths: each agent's instructions, MCP file and skills folder, each skill
// and where it came from, CC Switch's folder, the shared skills folder,
// most of them many times over (once for each skill, or each pair of
// agents). resolve looks at a path a part at a time, so one read of the
// page looked at the same few hundred folders thousands of times, two or
// three system calls each; on Windows that alone was over a second on
// every visit (oxyuan, #1771). While a read holds the library's lock,
// what each part was found to be is kept for the rest of that read, so
// each folder is looked at once. Nothing is kept past the read: the next
// visit looks again, so a link made or changed on disk in between shows.

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/agent"
)

// part is what one entry on the way to a path is: there or not, and
// where it points when it is a link (a symlink, or on Windows a junction).
type part struct {
	there, link bool
	to          string
}

type partMemo struct {
	sync.Mutex
	m map[string]part
}

// reading is the memo of the read in progress, nil when there is none.
var reading atomic.Pointer[partMemo]

// readParts keeps what lookPart finds until the returned func is called.
// Only a read of the page that holds mu calls it: no change can make or
// take away a link while it does, since every change holds mu too. A
// caller without mu meanwhile (a probe of a folder to add) is given what
// the read found, at most as old as the read.
func readParts() (done func()) {
	reading.Store(&partMemo{m: map[string]part{}})
	return func() { reading.Store(nil) }
}

// lookPart is what the entry at p is, as found once in this read.
func lookPart(p string) part {
	pm := reading.Load()
	if pm == nil {
		e, _ := lookAt(p)
		return e
	}
	pm.Lock()
	e, ok := pm.m[p]
	pm.Unlock()
	if ok {
		return e
	}
	e, sure := lookAt(p)
	if sure {
		pm.Lock()
		pm.m[p] = e
		pm.Unlock()
	}
	return e
}

// wslReadlink is agent.WSLReadlink; a var for tests.
var wslReadlink = agent.WSLReadlink

// lookAt is statPart; a test counts the looks
var lookAt = statPart

// statPart looks at the entry at p: one Lstat, and a Readlink for a link.
// sure is false when it couldn't be looked at for another reason than
// its not being there: unknown, looked at again next time.
func statPart(p string) (e part, sure bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return part{}, errors.Is(err, fs.ErrNotExist)
	}
	if !linkEntry(fi) {
		return part{there: true}, true
	}
	to, err := os.Readlink(p)
	if err != nil || to == "" {
		// a link a WSL distro has, which Windows can't read through
		// \\wsl.localhost: the distro says where it leads
		// (agent.WSLReadlink)
		if w, ok := wslReadlink(p); ok {
			return part{there: true, link: true, to: w}, true
		}
		// a link that can't be read is taken as an entry of its own, as
		// before; but it isn't kept
		return part{there: true}, false
	}
	return part{there: true, link: true, to: to}, true
}
