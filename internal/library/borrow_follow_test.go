package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fakeLibraryRemote serves a Library as a Remote magpie's gateway does:
// tagged, with a 304 for the tag it has, or, as a magpie from before
// ETags, the body every time.
type fakeLibraryRemote struct {
	sync.Mutex
	b         *Bundle
	untagged  bool
	asks, not int    // requests, and 304s answered
	sent      string // the If-None-Match of the last request
}

func (f *fakeLibraryRemote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	if r.URL.Path != provider.RemoteLibraryPath {
		http.NotFound(w, r)
		return
	}
	f.asks++
	f.sent = r.Header.Get("If-None-Match")
	body, _ := json.Marshal(f.b)
	if !f.untagged {
		h := sha256.Sum256(body)
		tag := `"` + hex.EncodeToString(h[:16]) + `"`
		w.Header().Set("ETag", tag)
		if f.sent == tag {
			f.not++
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// seen is the reads, the 304s and the last If-None-Match.
func (f *fakeLibraryRemote) seen() (int, int, string) {
	f.Lock()
	defer f.Unlock()
	return f.asks, f.not, f.sent
}

func (f *fakeLibraryRemote) edit(fn func(b *Bundle)) {
	f.Lock()
	defer f.Unlock()
	fn(f.b)
}

// frankittee on Discord: a prompt edited on the Remote magpie whose
// skills and prompts this computer uses never came here by itself. It
// was read every ten minutes, in the app only. Now a minute later a
// change there is here, the prompt and the skill, and a Library that
// didn't change isn't sent or written again.
func TestBorrowedFollowsTheRemote(t *testing.T) {
	remote := remoteLibrary(t)
	fake := &fakeLibraryRemote{b: remote}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	sandbox(t) // this computer
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: srv.URL, Key: "secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetBorrow("office", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setSeenBorrow("office", "") })
	ctx := context.Background()
	// the poll's clock, as if the last read was ago
	readAgo := func(ago time.Duration) {
		lastBorrow.Lock()
		lastBorrow.at = time.Now().Add(-ago)
		lastBorrow.Unlock()
		keepBorrowed(ctx)
	}
	t.Cleanup(func() { lastBorrow.Lock(); lastBorrow.at = time.Time{}; lastBorrow.Unlock() })
	work := setID("office", "work")
	text := func() string { l := mustLoad(t); return setText(l.textPath(work)) }
	stamp := func() time.Time {
		fi, err := os.Stat(path())
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}

	readAgo(time.Hour)
	if got := text(); got != "Reply in the user's language." {
		t.Fatalf("the remote's set: %q", got)
	}
	if !strings.Contains(borrowedText(t, "notes"), "description: Notes") {
		t.Fatal("the remote's skill didn't arrive")
	}
	if asks, _, _ := fake.seen(); asks != 1 {
		t.Fatalf("%d reads", asks)
	}

	// a few seconds on: not read again yet
	readAgo(5 * time.Second)
	if asks, _, _ := fake.seen(); asks != 1 {
		t.Fatalf("read again after 5s: %d reads", asks)
	}

	// a minute on, nothing changed there: a 304, and nothing taken in or
	// written here
	was, taken := stamp(), takenIn.Load()
	readAgo(time.Minute + time.Second)
	if asks, not, sent := fake.seen(); asks != 2 || not != 1 || sent == "" {
		t.Fatalf("an unchanged Library: %d reads, %d 304s, If-None-Match %q", asks, not, sent)
	}
	if !stamp().Equal(was) || takenIn.Load() != taken {
		t.Fatal("an unchanged Library was taken in again")
	}

	// changed there: the prompt and the skill are here a minute later
	fake.edit(func(b *Bundle) {
		b.Texts["work"] = "Reply in French."
		for _, s := range b.Skills {
			if s.Name == "notes" {
				s.Files["SKILL.md"] = []byte("---\nname: notes\ndescription: Notes, kept short\n---\n")
			}
		}
	})
	readAgo(time.Minute + time.Second)
	if got := text(); got != "Reply in French." {
		t.Fatalf("the remote's edited set a minute later: %q", got)
	}
	if !strings.Contains(borrowedText(t, "notes"), "Notes, kept short") {
		t.Fatalf("the remote's edited skill a minute later: %q", borrowedText(t, "notes"))
	}
	if l := mustLoad(t); l.active() == work && setText(l.sharedPath()) != "Reply in French." {
		t.Fatalf("the agents read %q", setText(l.sharedPath()))
	}

	// a magpie from before ETags sends it whole every time: the same
	// Library isn't taken in again, a changed one is
	fake.edit(func(*Bundle) { fake.untagged = true }) // under its lock
	readAgo(time.Minute + time.Second)
	was, taken = stamp(), takenIn.Load()
	readAgo(time.Minute + time.Second)
	if asks, _, _ := fake.seen(); asks != 5 || !stamp().Equal(was) || takenIn.Load() != taken {
		t.Fatalf("the same untagged Library: %d reads, taken in again %v", asks, takenIn.Load() != taken)
	}
	fake.edit(func(b *Bundle) { b.Texts["work"] = "Reply in German." })
	readAgo(time.Minute + time.Second)
	if got := text(); got != "Reply in German." {
		t.Fatalf("an untagged change: %q", got)
	}

	// the switch turned off and on reads it whole again
	if _, err := SetBorrow("office", false); err != nil {
		t.Fatal(err)
	}
	if text() != "" {
		t.Fatal("switched off, the remote's set stayed")
	}
	if _, err := SetBorrow("office", true); err != nil {
		t.Fatal(err)
	}
	if _, err := UseRemoteLibrary(ctx, "office"); err != nil {
		t.Fatal(err)
	}
	if got := text(); got != "Reply in German." {
		t.Fatalf("switched on again: %q", got)
	}
}
