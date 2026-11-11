package dirent

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Every 15 s Sessions poll lists every Codex day folder and Claude Code
// project again, ~18k files in a few hundred folders, and those listings
// were ~70% of the poll's CPU (~115 ms a poll on a real history) while
// nothing in them had changed. A folder under a root someone Watches is
// listed again only when the system said something in it changed since
// its last listing (FSEvents on macOS), and otherwise is its last listing,
// as it was. Where there is no such watch, every Stats lists the folder.
//
// The system's word comes a moment late, so Watch waits until every
// change made before it is heard: it writes a file of its own in a new
// folder the watch covers, and the system tells changes in the order they were
// made, so once that write is heard so is every one before it. Anything
// the watch can't vouch for (it dropped events, a root moved, a volume
// came or went, a wait ran out, it hasn't started) lists again, and so
// does a listing older than listedFor, should an event never come.
var watched struct {
	sync.Mutex
	roots  []watchRoot
	seq    uint64             // counts listings begun and changes heard
	all    uint64             // seq when everything last changed
	dirty  map[string]uint64  // a folder's seq when it last changed
	at     map[uintptr]listed // a listing's seq and time, by its array
	since  uint64             // seq when the watch last started
	active bool
	tried  map[string]bool // roots asked for
	synced time.Time       // when every change before was last heard
	probe  probe
}

type watchRoot struct {
	given, real string
	dev         uint64
}

// probe is the folder Watch writes in to know every change before is
// heard, and the wait for that write's word.
type probe struct {
	dir, real string
	dev       uint64
	n         int
	want      string // the folder whose word the wait is for
	heard     chan struct{}
}

type listed struct {
	seq    uint64
	when   time.Time
	newest int64 // the newest Mod of its files
}

const (
	// listedFor is how long a folder's listing is trusted without listing
	// it again, in case a change was never heard: a write to a file held
	// open is heard only when the file is closed, and Codex holds its
	// rollout open for the whole session.
	listedFor = 2 * time.Minute
	// hotFor is how long after one of its files was written a folder is
	// listed on every read: a session being written may hold its file
	// open, and nothing is heard of it until it closes.
	hotFor = 15 * time.Minute
	// trustFor is how long after a Watch a listing is taken as current: a
	// change made since may not have been heard yet.
	trustFor = 2 * time.Second
	// probeWait is how long Watch waits for its own write to be heard.
	probeWait = time.Second
)

// Watch has dir and everything under it watched for changes, where the
// system can, and waits until every change made before the call is
// heard: a reader calls it before each read of dir's folders, and Stats
// of a folder in it then skips listing one that hasn't changed. A Stats
// with no Watch of its root in the last trustFor reads its folder.
func Watch(dir string) {
	dir = filepath.Clean(dir)
	starting.Lock()
	defer starting.Unlock()
	watched.Lock()
	tried := watched.tried[dir]
	watched.Unlock()
	if !tried && !add(dir) {
		return
	}
	sync1()
}

// starting is held while a watch is made or waited on, outside watched:
// stopping a stream waits for its word, which takes watched.
var starting sync.Mutex

// add has the watch cover dir too, and is whether dir can be watched now.
// starting is held.
func add(dir string) bool {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false // not there yet: tried again next time
	}
	fi, err := os.Stat(real)
	if err != nil {
		return false
	}
	watched.Lock()
	if watched.tried == nil {
		watched.tried = map[string]bool{}
	}
	watched.tried[dir] = true
	for _, r := range watched.roots {
		if within(dir, r.given) || within(real, r.real) {
			watched.Unlock()
			return true
		}
	}
	roots := append(slices.Clip(watched.roots), watchRoot{dir, real, devOf(fi)})
	p := watched.probe
	watched.active = false
	watched.Unlock()
	if p.dir == "" {
		d, err := os.MkdirTemp("", "magpie-watch-")
		if err != nil {
			return false
		}
		r, err := filepath.EvalSymlinks(d)
		pfi, err2 := os.Stat(d)
		if err != nil || err2 != nil {
			os.Remove(d)
			return false
		}
		p = probe{dir: d, real: r, dev: devOf(pfi)}
	}
	paths := []string{p.real}
	for _, r := range roots {
		paths = append(paths, r.real)
	}
	on := startWatch(paths)
	watched.Lock()
	defer watched.Unlock()
	watched.seq++
	watched.since, watched.all = watched.seq, watched.seq
	watched.roots, watched.active, watched.probe = roots, on, p
	return true
}

// sync1 writes in the probe's folder and waits for the write to be heard,
// so every change before it is too. starting is held.
func sync1() {
	at := time.Now()
	watched.Lock()
	if !watched.active {
		watched.Unlock()
		return
	}
	// each wait writes in a folder of its own: the word of an earlier
	// write, come after its wait gave up, must not end this one
	watched.probe.n++
	n := strconv.Itoa(watched.probe.n)
	ch := make(chan struct{})
	watched.probe.heard = ch
	watched.probe.want = filepath.Join(watched.probe.real, n)
	p := watched.probe
	watched.Unlock()
	sub := filepath.Join(p.dir, n)
	ok := os.Mkdir(sub, 0o700) == nil && os.WriteFile(filepath.Join(sub, "probe"), nil, 0o600) == nil
	if ok {
		select {
		case <-ch:
		case <-time.After(probeWait):
			ok = false
		}
	}
	// the file, then its folder: RemoveAll's look, open and listing of the
	// folder were half of each wait's time, two a Sessions poll
	if os.Remove(filepath.Join(sub, "probe")) != nil || os.Remove(sub) != nil {
		os.RemoveAll(sub)
	}
	watched.Lock()
	defer watched.Unlock()
	watched.probe.heard, watched.probe.want = nil, ""
	if ok {
		watched.synced = at
		return
	}
	watched.seq++
	watched.all = watched.seq
}

func within(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, root) && (strings.HasSuffix(root, string(filepath.Separator)) || dir[len(root)] == filepath.Separator)
}

// heard is the system's word that something in each folder of paths
// changed, or, with all, that anything may have.
func heard(paths []string, all bool) {
	watched.Lock()
	defer watched.Unlock()
	watched.seq++
	if ch := watched.probe.heard; ch != nil && slices.Contains(paths, watched.probe.want) {
		close(ch)
		watched.probe.heard = nil
	}
	if all || len(watched.dirty) > 1<<14 {
		watched.all = watched.seq
		watched.dirty = nil
		if all {
			return
		}
	}
	if watched.dirty == nil {
		watched.dirty = map[string]uint64{}
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		watched.dirty[p] = watched.seq
		for _, r := range watched.roots {
			if r.given != r.real && within(p, r.real) {
				watched.dirty[r.given+p[len(r.real):]] = watched.seq
			}
		}
	}
}

// begin is the seq of a listing of dir begun now, or 0 when dir isn't
// watched.
func begin(dir string) uint64 {
	watched.Lock()
	defer watched.Unlock()
	if !watched.active || !watchedDir(dir) {
		return 0
	}
	watched.seq++
	return watched.seq
}

func watchedDir(dir string) bool {
	for _, r := range watched.roots {
		if within(dir, r.given) || within(dir, r.real) {
			// the probe's word vouches for a change made on its own volume
			return r.dev == watched.probe.dev
		}
	}
	return false
}

// mark records l, a listing begun at seq, as current as of then, or, with
// seq 0, as nothing the watch vouches for. Every listing Stats makes is
// marked, so an array used again for another folder's listing, or freed
// and its address given to another, never keeps an earlier one's word:
// the address is kept, not the array.
func mark(l Listing, seq uint64) {
	if cap(l) == 0 {
		return
	}
	key := uintptr(unsafe.Pointer(unsafe.SliceData(l[:cap(l)])))
	watched.Lock()
	defer watched.Unlock()
	if seq == 0 {
		delete(watched.at, key)
		return
	}
	if watched.at == nil || len(watched.at) > 1<<14 {
		watched.at = map[uintptr]listed{}
	}
	var newest int64
	for _, e := range l {
		if e.Mode == 0 {
			newest = max(newest, e.Mod)
		}
	}
	watched.at[key] = listed{seq, time.Now(), newest}
}

// current is whether l, dir's last listing, is still what dir holds: the
// watch has heard of no change in dir since l was listed.
func current(dir string, l Listing) bool {
	if cap(l) == 0 {
		return false
	}
	watched.Lock()
	defer watched.Unlock()
	if !watched.active || !watchedDir(dir) {
		return false
	}
	at, ok := watched.at[uintptr(unsafe.Pointer(unsafe.SliceData(l[:cap(l)])))]
	if !ok || at.seq <= watched.since || at.seq <= watched.all || time.Since(at.when) > listedFor || time.Since(watched.synced) > trustFor {
		return false
	}
	if time.Since(time.Unix(0, at.newest)) < hotFor {
		return false
	}
	dir = filepath.Clean(dir)
	if watched.dirty[dir] >= at.seq {
		return false
	}
	// a change in a root's own folder is word of a change anywhere under
	// it: Codex appends history.jsonl, closed each time, at each prompt,
	// while the rollout it writes stays open and unheard
	for _, r := range watched.roots {
		if (within(dir, r.given) || within(dir, r.real)) && (watched.dirty[r.given] >= at.seq || watched.dirty[r.real] >= at.seq) {
			return false
		}
	}
	return true
}

// forget drops what the watch knows of listings let go.
func forgetListed() {
	watched.Lock()
	watched.at = nil
	watched.Unlock()
}
