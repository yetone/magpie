package provider

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/testenv"
)

// An account signed in while magpie runs gets its vendor's list the next
// time the providers are listed, not only after Refresh (#204: WorkBuddy's
// models incomplete when first added, all of them after Refresh). One whose
// list couldn't be had isn't asked again on each listing.
func TestFetchNewAccount(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()

	var asked atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			w.WriteHeader(404)
			return
		}
		asked.Add(1)
		if down.Load() {
			w.WriteHeader(502)
			return
		}
		w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["hy4-preview","gpt-6-astra","glm-5.3"]}],
			"models":[{"id":"hy4-preview","name":"Hy4 preview"},{"id":"gpt-6-astra","name":"GPT-6-Astra"},{"id":"glm-5.3","name":"GLM-5.3"}]}}`))
	}))
	defer srv.Close()
	old := wbEndpoint
	wbEndpoint = srv.URL
	defer func() { wbEndpoint = old }()

	ids := func() map[string]bool {
		p, err := Find("workbuddy")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range p.Available() {
			out[m.ID] = true
		}
		return out
	}

	// nothing signed in yet: nothing asked, and nothing missing
	if FetchNew(time.Second) {
		t.Fatal("missing a list with no account")
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("asked %d times with no account", n)
	}

	// WorkBuddy signs itself in while magpie runs
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "旅行者"},
		"auth": map[string]any{"accessToken": "a", "refreshToken": "r",
			"expiresAt": future, "refreshExpiresAt": future, "domain": "www.codebuddy.cn"},
	})
	forgetAccountCaches()
	if ids()["hy4-preview"] {
		t.Fatal("the vendor's model listed before its list was fetched")
	}
	if FetchNew(5 * time.Second) {
		t.Fatal("a list fetched said missing")
	}
	got := ids()
	if !got["hy4-preview"] || !got["gpt-6-astra"] || len(got) != 3 {
		t.Fatalf("after FetchNew: %v", got)
	}
	FetchNew(5 * time.Second)
	if n := asked.Load(); n != 1 {
		t.Fatalf("a fetched list asked for again: %d", n)
	}

	// a list that can't be had is asked for once, and again only later
	if err := catalog.SaveLive("workbuddy", "", nil); err != nil {
		t.Fatal(err)
	}
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	down.Store(true)
	// missing both times: when it failed, and while it waits to ask again,
	// so the terminal and magpie serve try once more later (#1789)
	if !FetchNew(5 * time.Second) {
		t.Fatal("a list that failed not said missing")
	}
	if !FetchNew(5 * time.Second) {
		t.Fatal("a list waiting to be asked again not said missing")
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("a failing vendor asked %d times, want once more", n-1)
	}
	oldRetry := newFetchRetry
	newFetchRetry = 0
	defer func() { newFetchRetry = oldRetry }()
	down.Store(false)
	if FetchNew(5 * time.Second) {
		t.Fatal("missing after the list came")
	}
	if n := asked.Load(); n != 3 || !ids()["hy4-preview"] {
		t.Fatalf("not asked again after the retry time: %d %v", n, ids())
	}
}

// A CLI asked who is signed in for the first time, answering after a look's
// wait, is waited for: `cursor-agent about` asks Cursor's servers, and at a
// start with nothing kept from before it answered after the look, so the
// terminal and magpie serve offered Cursor's fallback Auto alone (#1789).
func TestFetchNewWaitsForASlowSignIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake cursor-agent is a shell script")
	}
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	exe := filepath.Join(home, "cursor-agent")
	testenv.Program(t, exe, `#!/bin/sh
case "$1" in
about) /bin/sleep 1; echo '{"userEmail":"cursor@example.com","subscriptionTier":"pro"}' ;;
models) printf 'Available models\n\nauto - Auto (default)\ncomposer-2.5 - Composer 2.5\n' ;;
*) exit 1 ;;
esac
`)
	wasExe, wasOut, wasFirst := CursorExecutable, cursorSignedOut, firstAsk
	CursorExecutable = func() string { return exe }
	cursorSignedOut = func() bool { return false }
	firstAsk = 10 * time.Millisecond // the look gives up before `about` answers
	t.Cleanup(func() {
		CursorExecutable, cursorSignedOut, firstAsk = wasExe, wasOut, wasFirst
		ForgetAccounts()
	})
	ForgetAccounts()

	if FetchNew(time.Minute) {
		t.Fatal("Cursor's list said missing")
	}
	p, err := Find("cursor")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range p.Available() {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "composer-2.5") {
		t.Fatalf("FetchNew didn't wait for cursor-agent about: %v", ids)
	}
}

// A CLI that answers while the accounts are listed, after its look gave up
// waiting, has its account's list fetched in the same pass: with nothing
// left to wait on, awaitIdentities didn't wait, and the account it said is
// signed in was missing from the list read before (yetone on #1147).
func TestFetchNewListsAgainForAnAnswerWhileListing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake cursor-agent is a shell script")
	}
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	exe := filepath.Join(home, "cursor-agent")
	testenv.Program(t, exe, `#!/bin/sh
case "$1" in
models) printf 'Available models\n\nauto - Auto (default)\ncomposer-2.5 - Composer 2.5\n' ;;
*) exit 1 ;;
esac
`)
	wasExe, wasOut, wasFirst := CursorExecutable, cursorSignedOut, firstAsk
	wasCursor, wasDevin := cursorStatus, devinStatus
	CursorExecutable = func() string { return exe }
	cursorSignedOut = func() bool { return false }
	firstAsk = 5 * time.Second // Devin's look waits for its answer
	t.Cleanup(func() {
		cursorStatus, devinStatus = wasCursor, wasDevin
		CursorExecutable, cursorSignedOut, firstAsk = wasExe, wasOut, wasFirst
		ForgetAccounts()
	})
	ForgetAccounts()

	// Cursor's look gave up waiting already, so the list leaves it out;
	// it answers while Devin's look, read after it, waits
	cursorAnswer := make(chan struct{})
	cursor := &cliIdentity{name: "cursor-test", exe: func() string { return exe }, read: true, waited: true,
		ask: func() (string, string, bool, error) { <-cursorAnswer; return "cursor@example.com", "pro", true, nil }}
	devin := &cliIdentity{name: "devin-test", exe: func() string { return "" }, read: true,
		ask: func() (string, string, bool, error) {
			cursor.Lock()
			done := cursor.done
			cursor.Unlock()
			close(cursorAnswer)
			<-done
			return "", "", false, nil
		}}
	cursorStatus, devinStatus = cursor, devin

	if FetchNew(time.Minute) {
		t.Fatal("Cursor's list said missing")
	}
	p, err := Find("cursor")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range p.Available() {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "composer-2.5") {
		t.Fatalf("Cursor's list not fetched in the pass its CLI answered in: %v", ids)
	}
}
