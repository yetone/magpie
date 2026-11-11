package sessions

import (
	"slices"
	"sync"

	"github.com/yetone/magpie/internal/dirent"
)

// held is the session folders' listings kept while a request that reads
// them more than once is answered. A Sessions poll lists every Claude
// Code and Codex file for its list (allFiles), then again for the Codex
// threads' effort (the Requests state's CallSources): ~21k files, read
// twice a poll.
var held struct {
	sync.Mutex
	n int
	// gen counts the holds begun: a listing is taken after the latest
	// hold began when it carries that hold's gen. A count, not a time:
	// on Windows two time.Now() a moment apart can be the same, and a
	// hold begun in the tick a listing was taken in counted that older
	// listing as its own (#4114)
	gen   uint64
	lists map[string]*heldList
	// mark is dirent's as the latest hold began: its folders are watched
	// from it, so a poll's first probe vouches for the rest (#2278)
	mark int
}

type heldList struct {
	gen   uint64 // held.gen when it was taken
	files []file
	// closed once files is in: a read that comes while the folder is
	// being listed waits for that listing rather than taking its own
	ready chan struct{}
}

// OneListing has the session folders listed once until done is called:
// the reads between share each folder's listing, taken after the latest
// hold began, so none sees the files as they were before its own request
// came. done lets the listings go once no hold is left.
func OneListing() (done func()) {
	held.Lock()
	held.n++
	held.gen++
	held.mark = dirent.Mark()
	held.Unlock()
	return letGo()
}

// ShareListing is OneListing for a read that may see the listings the
// holds there are now share, taken before it came: it begins a hold only
// when there is none. The Usage page's Sessions tab asks for the list,
// the stats and the overview at once, and the stats, read again behind
// (statsFor), listed every session file a second time beside the list's
// own listing.
func ShareListing() (done func()) {
	held.Lock()
	defer held.Unlock()
	if held.n++; held.n == 1 {
		held.gen++
		held.mark = dirent.Mark()
	}
	return letGo()
}

func letGo() func() {
	return sync.OnceFunc(func() {
		held.Lock()
		if held.n--; held.n == 0 {
			held.lists = nil
		}
		held.Unlock()
	})
}

// watch is dirent.Watch of dir, from the latest hold's mark under one.
func watch(dir string) {
	held.Lock()
	n, mark := held.n, held.mark
	held.Unlock()
	if n == 0 {
		dirent.Watch(dir)
		return
	}
	dirent.WatchSince(dir, mark)
}

// heldWaiting is a test's: called as a read waits for another's listing.
var heldWaiting func()

// heldFiles is list(), or, under a hold, the listing of key taken since
// the latest hold began. The folders are listed outside the lock, once
// for the reads that ask meanwhile. The listing is theirs together: a
// reader never writes to it, and sorts or filters a copy of its own.
func heldFiles(key string, list func() []file) []file {
	held.Lock()
	if held.n == 0 {
		held.Unlock()
		return list()
	}
	if l, ok := held.lists[key]; ok && l.gen == held.gen {
		held.Unlock()
		if heldWaiting != nil {
			heldWaiting()
		}
		<-l.ready
		return l.files
	}
	mine := &heldList{gen: held.gen, ready: make(chan struct{})}
	if held.lists == nil {
		held.lists = map[string]*heldList{}
	}
	held.lists[key] = mine
	held.Unlock()
	defer close(mine.ready)
	// one listing for every read under the hold, not a copy each: ~95 MB
	// of a minute of polls (#2278). They only read it, and clipped, an
	// append to it is made elsewhere. Before ready is closed, so whoever
	// waited reads it.
	mine.files = slices.Clip(list())
	return mine.files
}
