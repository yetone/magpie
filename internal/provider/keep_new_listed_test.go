package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func newListedHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	newFetches.Lock()
	was := newFetches.m
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	t.Cleanup(func() {
		newFetches.Lock()
		newFetches.m = was
		newFetches.Unlock()
		forgetAccountCaches()
	})
	return home
}

func waitNewListed(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// Ported from kkkzbh's #1805 regression, using the current Codex fixture
// and aging the retry record instead of relying on a short retry deadline
func TestKeepNewListedAfterAnEmptyStartup(t *testing.T) {
	home := newListedHome(t)
	var asked atomic.Int32
	var down atomic.Bool
	down.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asked.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write(codexListOf("gpt-6-luna"))
	}))
	defer up.Close()
	codexListedAt(t, up.URL)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); keepNewListed(ctx, 5*time.Millisecond) }()
	defer func() { cancel(); <-done }()
	if FetchNew(time.Second) {
		t.Fatal("an empty startup was missing a list")
	}
	codexSignedIn(t, home)
	waitNewListed(t, "the account signed in later was never asked", func() bool { return asked.Load() > 0 })
	newFetches.Lock() // the failed round has returned
	if n := asked.Load(); n != 1 {
		t.Errorf("failed account asked %d times before its retry is due", n)
	}
	newFetches.Unlock()
	down.Store(false)
	FetchNew(time.Second)
	if n := asked.Load(); n != 1 {
		t.Fatalf("a failed account retried before its delay: %d", n)
	}
	newFetches.Lock()
	newFetches.m["codex"] = time.Now().Add(-2 * time.Minute)
	newFetches.Unlock()
	waitNewListed(t, "the account's models did not arrive after retry", func() bool {
		p, err := Find("codex")
		if err != nil {
			return false
		}
		_, listed := p.Listed()
		return listed
	})
	for range 3 {
		if FetchNew(time.Second) {
			t.Fatal("a listed account was missing its list")
		}
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("a listed account was fetched again: %d requests", n)
	}
}

func TestKeepNewListedCancelsItsFetch(t *testing.T) {
	home := newListedHome(t)
	codexSignedIn(t, home)
	started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-release:
		}
	}))
	defer up.Close()
	codexListedAt(t, up.URL)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); keepNewListed(ctx, time.Hour) }()
	defer func() {
		cancel()
		close(release)
		<-done
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first list request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop kept fetching after the gateway stopped")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the vendor request was not canceled")
	}
}

func TestKeepNewListedStopsWhileAnotherFetchHoldsTheLock(t *testing.T) {
	newListedHome(t)
	newFetches.Lock()
	defer newFetches.Unlock()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); keepNewListed(ctx, time.Hour) }()
		// Wait until the first timer has fired and the loop is blocked on
		// its next round, then cancel while another fetch still owns the lock
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("stopping waited for another fetch's mutex")
		}
	})
}
