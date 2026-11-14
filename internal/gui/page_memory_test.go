package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The pages a poll's reads left go back once no read is in flight: not
// while one is, once when the last ends, and a rest a new read cut short
// is waited out again.
func TestPageReadsReleasedAtRest(t *testing.T) {
	released := make(chan struct{}, 4)
	pageReads.Lock()
	pageReads.rest, pageReads.release = 20*time.Millisecond, func() { released <- struct{}{} }
	pageReads.Unlock()
	t.Cleanup(func() {
		pageReads.Lock()
		if pageReads.due != nil {
			pageReads.due.Stop()
			pageReads.due = nil
		}
		pageReads.rest, pageReads.release = 0, nil
		pageReads.Unlock()
	})

	a, b := readingPage(), readingPage()
	a()
	// one is still in flight
	releaseIfPagesRest()
	select {
	case <-released:
		t.Fatal("released while a read was in flight")
	default:
	}
	b()
	b() // done twice ends it once
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("not released once every read ended")
	}

	// a rest not long enough yet: the release waits out the rest of it
	stopDue := func() {
		if pageReads.due != nil {
			pageReads.due.Stop()
			pageReads.due = nil
		}
	}
	pageReads.Lock()
	pageReads.rest = time.Hour
	pageReads.Unlock()
	readingPage()()
	pageReads.Lock()
	stopDue()
	pageReads.Unlock()
	releaseIfPagesRest()
	pageReads.Lock()
	waiting := pageReads.due != nil
	stopDue()
	pageReads.last = time.Time{}
	pageReads.Unlock()
	if !waiting {
		t.Error("a rest cut short isn't waited out again")
	}
	select {
	case <-released:
		t.Fatal("released before the reads rested")
	default:
	}
	releaseIfPagesRest()
	select {
	case <-released:
	default:
		t.Fatal("not released once the reads rested")
	}
}

// held counts the Sessions and Usage pages' reads, and no other request.
func TestHeldCountsPageReads(t *testing.T) {
	in := -1
	h := held(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		pageReads.Lock()
		in = pageReads.n
		pageReads.Unlock()
	}))
	t.Cleanup(func() {
		pageReads.Lock()
		if pageReads.due != nil {
			pageReads.due.Stop()
			pageReads.due = nil
		}
		pageReads.Unlock()
	})
	for path, want := range map[string]int{"/api/sessions": 1, "/api/usage/requests": 1, "/api/state": 0} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if in != want {
			t.Errorf("%s: %d page reads in flight while it was answered, want %d", path, in, want)
		}
	}
}

// The app's page gone away (closed or hidden) has the Sessions page's
// stats let go pageAwayFor later, not the minutes the idle timers take;
// a page read since it went away keeps them.
func TestPageAwayLetsStatsGo(t *testing.T) {
	forgetStats()
	t.Cleanup(forgetStats)
	old := pageAwayFor
	pageAwayFor = 20 * time.Millisecond
	t.Cleanup(func() {
		pageAway.Lock()
		if pageAway.t != nil {
			pageAway.t.Stop()
			pageAway.t = nil
		}
		pageAway.Unlock()
		pageAwayFor = old
		pageReads.Lock()
		if pageReads.due != nil {
			pageReads.due.Stop()
			pageReads.due = nil
		}
		pageReads.Unlock()
	})
	kept := func() bool {
		statsMemo.Lock()
		defer statsMemo.Unlock()
		return statsMemo.m[0] != nil
	}

	// read again after it went away: kept
	statsFor(0)
	awayPage()
	readingPage()()
	time.Sleep(100 * time.Millisecond)
	if !kept() {
		t.Fatal("stats let go though a page read them after it went away")
	}

	awayPage()
	for deadline := time.Now().Add(10 * time.Second); kept(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("stats still kept after the page went away")
		}
	}
}
