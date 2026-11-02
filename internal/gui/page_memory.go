package gui

import (
	"runtime/debug"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// A Sessions or Usage page's poll reads every session file's listing and
// the request ledger again. That was ~330 MB allocated every 15 s to keep
// ~105 MB; over 15k sessions and 400k calls (11-02) an unchanged poll is
// ~12 MB of Sessions' and ~0.4 MB of Usage's, and one while a session
// grows, which re-sums every session's stats, ~110 MB. Go keeps the pages
// that freed, and its scavenger hands them back only
// slowly, never while the next collection's goal covers them, so an open
// window sat at ~340 MB in Activity Monitor between polls. The gateway
// hands its requests' pages back once it rests (#3472), but the window's
// reads aren't its requests. So once those reads have rested for
// pageRest, what they left goes back at once, as the warm-up's does.

// pageRest is how long the page reads rest before that. It outlasts the
// pages' polls (Usage every 10 s, Sessions every 15 s), so the pages go
// back once a window stops polling, not between an open window's polls:
// at 3 s every poll took back the pages the last one handed back, and
// madvise and the collections it drove were ~40% of the CPU (#2278).
const pageRest = 30 * time.Second

var pageReads struct {
	sync.Mutex
	n    int         // reads in flight
	last time.Time   // when the latest ended
	due  *time.Timer // the release to come, or nil
	rest time.Duration
	// debug.FreeOSMemory, or a test's count
	release func()
}

// readingPage counts a page read until done is called, and has what it
// left handed back once no read has been in flight for pageRest.
func readingPage() (done func()) {
	pageReads.Lock()
	pageReads.n++
	pageReads.Unlock()
	return sync.OnceFunc(func() {
		pageReads.Lock()
		defer pageReads.Unlock()
		pageReads.last = time.Now()
		if pageReads.n--; pageReads.n == 0 && pageReads.due == nil {
			pageReads.due = time.AfterFunc(pageRestFor(), releaseIfPagesRest)
		}
	})
}

// pageRestFor is pageRest, or a test's. Called under pageReads' lock.
func pageRestFor() time.Duration {
	if pageReads.rest > 0 {
		return pageReads.rest
	}
	return pageRest
}

// releaseIfPagesRest hands memory back when no page read has been in
// flight for pageRest. A read in flight arms it again when it ends, and a
// rest not long enough yet waits out the rest of it.
func releaseIfPagesRest() {
	pageReads.Lock()
	pageReads.due = nil
	if pageReads.n != 0 {
		pageReads.Unlock()
		return
	}
	if wait := pageRestFor() - time.Since(pageReads.last); wait > 0 {
		pageReads.due = time.AfterFunc(wait, releaseIfPagesRest)
		pageReads.Unlock()
		return
	}
	release := pageReads.release
	pageReads.Unlock()
	// what the request matching keeps for a poll's next read goes too
	usage.ReleaseScratch()
	if release != nil {
		release()
		return
	}
	debug.FreeOSMemory()
}
