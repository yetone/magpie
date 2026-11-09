package dirent

import (
	"cmp"
	"io/fs"
	"slices"
	"sync"
	"time"
)

// Stat is what a folder's listing says of one entry: its kind (Mode's type
// bits, 0 for a regular file), and a regular file's size and modification
// time, as os.Lstat gives them. Mod is unix nanoseconds, not a time.Time:
// what is kept of a history's listings is ~130k entries, and its 24 bytes
// made an entry 56 bytes where it is now 40 (#2278).
type Stat struct {
	Size int64
	Mod  int64
	Mode fs.FileMode
}

// ModTime is Mod as a time, as os.Lstat's ModTime has it.
func (s Stat) ModTime() time.Time { return time.Unix(0, s.Mod) }

// Entry is one entry of a Listing.
type Entry struct {
	Name string
	Stat
}

// Listing is a folder's entries in name order. It is a slice, not a map:
// every 15 s Sessions poll lists every Codex day folder and Claude Code
// project again, and a map of each folder's entries was ~90 MB of the 2.3 GB
// eight polls allocated.
type Listing []Entry

// Get is name's entry.
func (l Listing) Get(name string) (Stat, bool) {
	if i, ok := slices.BinarySearchFunc(l, name, func(e Entry, name string) int { return cmp.Compare(e.Name, name) }); ok {
		return l[i].Stat, true
	}
	return Stat{}, false
}

// Stats is each entry of dir in name order, as os.Lstat gives it, read in
// a few calls for the whole folder where the system has them
// (getattrlistbulk on macOS), rather than one per entry. ok is false where
// the system has none or the folder couldn't be read, and the caller asks
// after each entry itself.
//
// A caller done reading the listing gives it back (Done) for the next.
func Stats(dir string) (Listing, bool) {
	old, room := taken(dir)
	l, ok := stats(dir, room, old)
	if old != nil {
		listings.Lock()
		keepRoom(old)
		listings.Unlock()
	}
	if !ok {
		return nil, false
	}
	slices.SortFunc(l, func(a, b Entry) int { return cmp.Compare(a.Name, b.Name) })
	return l, true
}

// listings are Stats' answers given back: a listing made new for each
// folder, grown an entry at a time, and each entry's name copied again,
// was ~95 MB of a minute of the page's polls (#2278). A folder's last
// listing is kept, so the next read of it takes its names where they are
// the same, and the one before it is room for the next folder's. They are
// kept here, not in a sync.Pool: a poll collects garbage about twice a
// second, and each collection emptied the pool before the next folder was
// read. A Sessions read holds a listing of each Claude Code project at
// once, a few hundred, so what is kept is bounded by its entries,
// keptEntries in all (~7 MB, and the names). Each folder's last listing
// and the room of the one before are kept: 1<<16 kept little of a history
// of ~23k session files, and the listings were made new again.
var listings struct {
	sync.Mutex
	last    map[string]Listing // a folder's last listing
	room    []Listing          // cleared
	entries int                // the room of those kept
}

const keptEntries = 1 << 17

// taken is dir's last listing and room for its next, each taken from
// what is kept, or none.
func taken(dir string) (old, room Listing) {
	listings.Lock()
	defer listings.Unlock()
	if old = listings.last[dir]; old != nil {
		delete(listings.last, dir)
		listings.entries -= cap(old)
	}
	n := len(listings.room)
	if n == 0 {
		return old, nil
	}
	at := n - 1 // room enough for dir as last listed, or the last given back
	for i, r := range listings.room {
		if cap(r) >= len(old) {
			at = i
			break
		}
	}
	room = listings.room[at]
	listings.room[at] = listings.room[n-1]
	listings.room[n-1] = nil
	listings.room = listings.room[:n-1]
	listings.entries -= cap(room)
	return old, room
}

// keepRoom keeps l, cleared, as room for a next listing, while the kept
// have room. listings is held.
func keepRoom(l Listing) {
	clear(l[:cap(l)])
	if listings.entries+cap(l) <= keptEntries {
		listings.room = append(listings.room, l[:0])
		listings.entries += cap(l)
	}
}

// Done gives l, dir's listing, back for the next Stats of dir: whoever had
// it reads it no more. A Get of it before is only a copy of its entry.
func (l Listing) Done(dir string) {
	if cap(l) == 0 || cap(l) > 1<<14 {
		return // none, or a folder of more files than are kept
	}
	listings.Lock()
	defer listings.Unlock()
	if prev := listings.last[dir]; prev != nil { // read twice at once
		delete(listings.last, dir)
		listings.entries -= cap(prev)
		keepRoom(prev)
	}
	if listings.entries+cap(l) > keptEntries {
		keepRoom(l)
		return
	}
	if listings.last == nil {
		listings.last = map[string]Listing{}
	}
	listings.last[dir] = l
	listings.entries += cap(l)
}
