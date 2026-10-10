package provider

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// FetchingNew says a fetch is on its way as soon as FetchNewBehind or
// FetchNewSoon has started one, not once its goroutine reaches FetchNew: the
// Providers page reads it straight after FetchNewBehind and stops asking on
// false, so it kept the list it had while the fetch was about to run, and a
// test's cleanup that waited on it let its fetch run into the next test
// (TestProvidersAnswerWhileListsComeIn failed on CI). And a page's poll with
// no account due starts nothing it then counts: counted, each poll found
// the last one's still under way and the page asked for ever. One P keeps a
// started goroutine from running before FetchingNew is read.
func TestFetchingNewOnceStarted(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	reset := func() {
		newFetches.Lock()
		newFetches.m = map[string]time.Time{}
		newFetches.Unlock()
		newSoonAt.Store(0)
		wbTokens.Lock()
		wbTokens.m = map[string]wbCreds{}
		wbTokens.Unlock()
		forgetAccountCaches()
	}
	reset()

	// the vendor answers once the test lets it
	release := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			w.WriteHeader(404)
			return
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["hy4-preview"]}],
			"models":[{"id":"hy4-preview","name":"Hy4 preview"}]}}`))
	}))
	old := wbEndpoint
	wbEndpoint = srv.URL
	t.Cleanup(func() {
		close(release)
		waitFetchingNew(t)
		srv.Close()
		wbEndpoint = old
		reset()
	})
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "旅行者"},
		"auth": map[string]any{"accessToken": "a", "refreshToken": "r",
			"expiresAt": future, "refreshExpiresAt": future, "domain": "www.codebuddy.cn"},
	})
	forgetAccountCaches()
	All()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	// the Providers page: the account's list is due
	FetchNewBehind(5 * time.Second)
	if !FetchingNew() {
		t.Fatal("FetchNewBehind started a fetch, and FetchingNew says none is on its way")
	}
	release <- struct{}{}
	waitFetchingNew(t)

	// the page asks again with the list in: nothing is due, nothing runs
	FetchNewBehind(5 * time.Second)
	if FetchingNew() {
		t.Fatal("a poll with nothing due keeps FetchingNew up")
	}
	waitFetchingNew(t)

	// the panel, with the list due again
	if err := catalog.SaveLive("workbuddy", "", nil); err != nil {
		t.Fatal(err)
	}
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	newSoonAt.Store(0)
	FetchNewSoon(5 * time.Second)
	if !FetchingNew() {
		t.Fatal("FetchNewSoon started a fetch, and FetchingNew says none is on its way")
	}
	release <- struct{}{}
	waitFetchingNew(t)

	// a FetchNew under way (start-up's) holds the accounts: the page
	// doesn't wait on it, and is told lists are on their way
	newFetches.Lock()
	start := time.Now()
	FetchNewBehind(5 * time.Second)
	took := time.Since(start)
	busy := FetchingNew()
	newFetches.Unlock()
	if took > time.Second || !busy {
		t.Fatalf("behind a FetchNew under way: took %v, fetching %v", took, busy)
	}
	waitFetchingNew(t)

	// a second page asks while the first's fetch still picks the accounts
	// due (held on reading them): it starts nothing, and is told lists are
	// on their way (tsuixl on #1147)
	if err := catalog.SaveLive("workbuddy", "", nil); err != nil {
		t.Fatal(err)
	}
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	held.Lock()
	first := make(chan struct{})
	go func() {
		defer close(first)
		FetchNewBehind(5 * time.Second)
	}()
	for deadline := time.Now().Add(5 * time.Second); !fetchingNew.Load(); {
		if time.Now().After(deadline) {
			held.Unlock()
			t.Fatal("the first FetchNewBehind didn't start")
		}
		runtime.Gosched()
	}
	FetchNewBehind(5 * time.Second)
	busy = FetchingNew()
	held.Unlock()
	<-first
	if !busy {
		t.Fatal("a FetchNewBehind behind one still picking the accounts due is told none is on its way")
	}
	release <- struct{}{}
	waitFetchingNew(t)
}

// waitFetchingNew waits for the fetches under way to end, the ones started
// in the background included.
func waitFetchingNew(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); FetchingNew() || fetchingNew.Load(); {
		if time.Now().After(deadline) {
			t.Fatal("a fetch still runs after 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
