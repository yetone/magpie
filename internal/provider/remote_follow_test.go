package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// followFake is a magpie whose list is model, answered with its ETag, and
// held while a wait names that tag, as the gateway's modelsOnceChanged
// does. waits says whether it waits at all (one from before doesn't),
// and stripped is a magpie that tags every list reached through a proxy
// that drops ListWaitHeader: it answers each ask at once, 304 when the
// tag is the list's, saying no wait.
type followFake struct {
	mu       sync.Mutex
	model    string
	changed  chan struct{}
	waits    bool
	stripped bool
	fetches  atomic.Int32 // asked with no wait: the list fetched
	asks     atomic.Int32 // asked to wait
}

func (f *followFake) set(model string) {
	f.mu.Lock()
	f.model = model
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

func (f *followFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != "secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	waiting := r.Header.Get(ListWaitHeader) != ""
	if f.stripped && waiting {
		f.asks.Add(1)
		f.mu.Lock()
		tag := `"` + f.model + `"`
		f.mu.Unlock()
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, `{"object":"list","data":[]}`)
		return
	}
	if !waiting {
		f.fetches.Add(1)
	} else {
		f.asks.Add(1)
	}
	for {
		f.mu.Lock()
		model, changed := f.model, f.changed
		f.mu.Unlock()
		tag := `"` + model + `"`
		if waiting && f.waits && r.Header.Get("If-None-Match") == tag {
			select {
			case <-changed:
				continue
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
				w.Header().Set("ETag", tag)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		if waiting && f.waits {
			w.Header().Set("ETag", tag)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"`+model+`","object":"model"}]}`)
		return
	}
}

func followFast(t *testing.T) {
	t.Helper()
	gap, fail, old, look, quick := followGap, followFailWait, followOldAgain, followLook, followQuick
	// the fake holds a wait 2s, far from followQuick
	followGap, followFailWait, followOldAgain, followLook, followQuick = 10*time.Millisecond, 50*time.Millisecond, time.Hour, 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() {
		followGap, followFailWait, followOldAgain, followLook, followQuick = gap, fail, old, look, quick
	})
}

func followedCount(id string) int {
	followed.Lock()
	defer followed.Unlock()
	return followed.n[id]
}

func followUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(end) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func followedIDs(t *testing.T, id string) string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatalf("%s gone", id)
	}
	ms, _, _ := p.live()
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ",")
}

func followRemote(t *testing.T, f *followFake, every string) string {
	t.Helper()
	isolate(t)
	isolateFetchHome(t)
	followFast(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	id, err := Add(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "secret", Chat: strings.TrimPrefix(srv.URL, "http://"), RefreshEvery: every})
	if err != nil {
		t.Fatal(err)
	}
	followed.Lock()
	delete(followed.n, id)
	followed.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); KeepRemoteListsFollowed(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return id
}

// A list changed on the Remote magpie is fetched here at once (KaCli), not
// on the next RemoteMagpieEvery: the follower waits at the list and fetches
// it again the moment its tag moves.
func TestRemoteMagpieListFollowed(t *testing.T) {
	f := &followFake{model: "own/m1", changed: make(chan struct{}), waits: true}
	id := followRemote(t, f, "")
	followUntil(t, "the first list fetched", func() bool { return followedCount(id) >= 1 })
	if got := followedIDs(t, id); got != "own/m1" {
		t.Fatalf("listed %q, want own/m1", got)
	}
	before := f.fetches.Load()
	f.set("own/m2")
	followUntil(t, "the changed list fetched", func() bool { return followedIDs(t, id) == "own/m2" })
	if f.fetches.Load() == before {
		t.Error("the list wasn't fetched again")
	}
	// the same list again is waited at, not fetched again and again
	n := f.fetches.Load()
	time.Sleep(300 * time.Millisecond)
	if f.fetches.Load() != n {
		t.Errorf("fetched %d times with no change", f.fetches.Load()-n)
	}
}

// A magpie from before answers its list at once with no ETag: it is left
// to the schedule, asked to wait again only after followOldAgain, so it is
// not asked over and over.
func TestRemoteMagpieOldListNotFollowed(t *testing.T) {
	f := &followFake{model: "own/m1", changed: make(chan struct{})}
	followRemote(t, f, "")
	followUntil(t, "asked once", func() bool { return f.asks.Load() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if n := f.asks.Load(); n != 1 {
		t.Errorf("an old magpie asked %d times, want once", n)
	}
	if n := f.fetches.Load(); n != 0 {
		t.Errorf("an old magpie's list fetched %d times by the follower, want 0: its schedule fetches it", n)
	}
}

// A magpie that tags every list (clyzhicore), reached through a proxy
// that drops ListWaitHeader, answers each wait at once: its first answer,
// tagged, can't be told from a wait's, but its 304 at once that says no
// wait is, and it is left to the schedule rather than asked each
// followGap.
func TestRemoteMagpieStrippedWaitNotAskedOver(t *testing.T) {
	f := &followFake{model: "own/m1", changed: make(chan struct{}), stripped: true}
	followRemote(t, f, "")
	followUntil(t, "asked twice", func() bool { return f.asks.Load() >= 2 })
	time.Sleep(300 * time.Millisecond)
	if n := f.asks.Load(); n != 2 {
		t.Errorf("a magpie that doesn't wait asked %d times in 300ms, want 2", n)
	}
}

// A Remote magpie whose user set its list to be fetched only when asked
// is not followed: magpie's default never overrides the user's "off".
func TestRemoteMagpieOffNotFollowed(t *testing.T) {
	f := &followFake{model: "own/m1", changed: make(chan struct{}), waits: true}
	followRemote(t, f, "off")
	time.Sleep(300 * time.Millisecond)
	if n := f.asks.Load() + f.fetches.Load(); n != 0 {
		t.Errorf("a list set to off was asked %d times", n)
	}
}
