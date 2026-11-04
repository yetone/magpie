package provider

import (
	"testing"
	"time"
)

// TestLoginsListedOnceARequest: /api/providers asks an agent's accounts
// of every provider, and each list took logins.json's lock; a request
// lists an agent's once, a copy each, and a write in it lists anew.
func TestLoginsListedOnceARequest(t *testing.T) {
	signIn(t)
	loginsSeenAt.Store(0) // another test's look within 30s would skip this HOME's
	defer func(d time.Duration) { heldFor = d }(heldFor)
	heldFor = time.Hour // a loaded machine's pause isn't the bound
	release := Hold()
	defer func() { release() }()
	Logins("codex") // the first list notes the sign-in in logins.json
	first := Logins("codex")
	if len(first) != 1 || first[0].User != "me@example.com" {
		t.Fatalf("codex accounts: %+v", first)
	}
	runs := loginsRuns.Load()
	for range 20 {
		ls := Logins("codex")
		if len(ls) != 1 || ls[0].User != "me@example.com" {
			t.Fatalf("listed again as %+v", ls)
		}
		ls[0].User = "changed"
	}
	if n := loginsRuns.Load() - runs; n != 0 {
		t.Fatalf("20 lists in one request listed %d times, want 0", n)
	}
	if err := writeLogins(readLogins()); err != nil {
		t.Fatal(err)
	}
	Logins("codex")
	if n := loginsRuns.Load() - runs; n != 1 {
		t.Fatalf("after a write: listed %d times, want 1", n)
	}
	release()
	release = func() {}
	Logins("codex")
	Logins("codex")
	if n := loginsRuns.Load() - runs; n != 3 {
		t.Fatalf("outside a request: listed %d times, want 3", n)
	}
}

// TestLoginsMissedTogetherListedOnce: a write drops what a request holds,
// and every request in flight lists again — each taking logins.json's
// lock (and, before the list, the agents' sign-ins) one after another, a
// convoy of requests all waiting on the same lock under parallel load
// (#5104). A list missed together is built once: the rest wait for it.
func TestLoginsMissedTogetherListedOnce(t *testing.T) {
	signIn(t)
	loginsSeenAt.Store(0) // another test's look within 30s would skip this HOME's
	release := Hold()
	defer func() { release() }()
	Logins("codex") // the first list notes the sign-in in logins.json
	if ls := Logins("codex"); len(ls) != 1 || ls[0].User != "me@example.com" {
		t.Fatalf("codex accounts: %+v", ls)
	}
	runs := loginsRuns.Load()
	dropHeld() // a write magpie made: what is held is listed anew
	const n = 20
	start := make(chan struct{})
	done := make(chan []Login, n)
	for range n {
		go func() {
			<-start
			done <- Logins("codex")
		}()
	}
	// the first list builds behind logins.json's lock, held here: who
	// lists before the first is built tells the convoy, whatever the
	// machine's load — every caller builds again where none waits for
	// the one building, and only the one building does otherwise
	loginsMu.Lock()
	close(start)
	var missed int64
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
		if missed = loginsRuns.Load() - runs; missed >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	loginsMu.Unlock()
	for range n {
		if ls := <-done; len(ls) != 1 || ls[0].User != "me@example.com" {
			t.Fatalf("listed again as %+v", ls)
		}
	}
	if got := loginsRuns.Load() - runs; got != 1 {
		t.Fatalf("%d requests missing together listed %d times, want 1 (%d listed before the first was built)", n, got, missed)
	}
}

// TestOwnPausedListedOnceARequest: a provider with another account on
// asks OwnPaused of its own for every request's plan, and each ask took
// logins.json's lock and read it again, even while the request held what
// Logins had listed (#5104). An ask lists once a request, as Logins does,
// and a write lists it anew.
func TestOwnPausedListedOnceARequest(t *testing.T) {
	signIn(t)
	loginsSeenAt.Store(0)
	release := Hold()
	defer func() { release() }()
	Logins("codex") // the first list notes the sign-in in logins.json
	p, ok := find(All(), "codex")
	if !ok {
		t.Fatal("no codex provider")
	}
	if p.OwnPaused() {
		t.Fatal("the only account paused with none to fall back to")
	}
	held.Lock()
	_, cached := held.built["ownPaused\x00codex\x00me@example.com"]
	held.Unlock()
	if !cached {
		t.Fatal("OwnPaused was not listed once a request: it took logins.json's lock again")
	}
	if err := writeLogins(readLogins()); err != nil {
		t.Fatal(err)
	}
	if p.OwnPaused() {
		t.Fatal("paused after a write that paused nothing")
	}
	held.Lock()
	_, cached = held.built["ownPaused\x00codex\x00me@example.com"]
	held.Unlock()
	if !cached {
		t.Fatal("a write did not list OwnPaused anew")
	}
}
