package sessions

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

// Writing the whole index to disk takes a second or more for a long
// history; a read of a few changed files neither waits on it nor counts it
// as indexing, or the page shows its indexing show for every catch-up read
// (Image #15: the show came back again and again at 3 of 3 files read).
func TestSaveBehindTheRead(t *testing.T) {
	setup(t)
	release := make(chan struct{})
	writing := make(chan struct{}, 8)
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	saveHook = func() { writing <- struct{}{}; <-release }
	t.Cleanup(func() { let(); saveHook = nil })

	done := make(chan []Session)
	go func() { done <- List(0) }()
	select {
	case <-writing:
	case <-time.After(5 * time.Second):
		t.Fatal("no save started")
	}
	var list []Session
	select {
	case list = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the read waited on the save")
	}
	if len(list) == 0 {
		t.Fatal("no sessions read")
	}
	if Indexing().Indexing {
		t.Fatal("still indexing while the index is saved")
	}
	// the save lands once let go, and a new load waits for it
	let()
	Saved()
	b, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	var kept cacheFile
	if json.Unmarshal(b, &kept) != nil || len(kept.Files) == 0 {
		t.Fatalf("cache file: %.200s", b)
	}
}
