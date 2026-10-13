package provider

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
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
// the last one's still under way and the page asked for ever. A CLI still
// asked who is signed in is waited for behind the page, counted meanwhile.
// One P keeps a started goroutine from running before FetchingNew is read.
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

	// the panel's FetchNewSoon lists nothing before it returns, so
	// /api/state?last=1 answers at once (#1519) while the accounts' read
	// is held, and is counted still
	newSoonAt.Store(0)
	held.Lock()
	soon := make(chan bool)
	go func() {
		FetchNewSoon(5 * time.Second)
		soon <- FetchingNew()
	}()
	select {
	case busy = <-soon:
		held.Unlock()
	case <-time.After(time.Second):
		held.Unlock()
		<-soon
		t.Fatal("FetchNewSoon waited on the accounts' read")
	}
	if !busy {
		t.Fatal("FetchNewSoon started a fetch, and FetchingNew says none is on its way")
	}
	waitFetchingNew(t)

	// a CLI never asked who is signed in, as after a sign-in forgot it, or
	// one still asked: which accounts are due can't be known without
	// waiting on it, which the page doesn't (#541); the fetch lists them
	// and waits behind the page, which is told lists are on their way until
	// it has (yetone on #1147, #1789)
	wasDevin, wasFirst := devinStatus, firstAsk
	defer func() { devinStatus, firstAsk = wasDevin, wasFirst }()
	firstAsk = 5 * time.Second // long, to tell a look's wait from none
	for _, asked := range []bool{false, true} {
		answer := make(chan struct{})
		devinStatus = &cliIdentity{name: "devin-test", exe: func() string { return "" }, read: true,
			ask: func() (string, string, bool, error) { <-answer; return "", "", false, nil }}
		if asked {
			devinStatus.Lock()
			devinStatus.waited = true // its look gave up already
			devinStatus.refresh()
			devinStatus.Unlock()
		}
		start = time.Now()
		FetchNewBehind(5 * time.Second)
		took = time.Since(start)
		busy = FetchingNew()
		if took > time.Second || !busy {
			close(answer)
			waitFetchingNew(t)
			t.Fatalf("with a CLI asked %v who is signed in: took %v, fetching %v", asked, took, busy)
		}
		close(answer)
		waitFetchingNew(t)
	}
}

// A CLI forgotten while the Providers page's FetchNewBehind lists the
// accounts (a sign-in finishing then) left its account out of that list:
// the fetch is asked behind the page, which is told lists are on their
// way, not that none is due.
func TestFetchingNewForgottenWhileListed(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	newSoonAt.Store(0)
	ForgetAccounts()
	wasCursor, wasDevin, wasGrok := cursorStatus, devinStatus, GrokExecutable
	t.Cleanup(func() {
		waitFetchingNew(t)
		cursorStatus, devinStatus, GrokExecutable = wasCursor, wasDevin, wasGrok
		ForgetAccounts()
	})
	nobody := func() (string, string, bool, error) { return "", "", false, nil }
	now := time.Now()
	cursor := &cliIdentity{name: "cursor-test", exe: func() string { return "" }, read: true, at: now, ask: nobody}
	cursorStatus = cursor
	devinStatus = &cliIdentity{name: "devin-test", exe: func() string { return "" }, read: true, at: now, ask: nobody}
	// Grok's account is read after Cursor's: the sign-in lands there
	var once sync.Once
	GrokExecutable = func() string {
		once.Do(cursor.forget)
		return ""
	}

	FetchNewBehind(5 * time.Second)
	busy := FetchingNew()
	waitFetchingNew(t)
	if !busy {
		t.Fatal("a CLI forgotten while the accounts were listed, and FetchingNew says none is on its way")
	}
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
