//go:build !windows

package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// The Providers page answers at once while an account's vendor is slow to
// list its models (#541: ten seconds and more of placeholders at start-up,
// the page waiting on each account's list in turn). Here Cursor's CLI takes
// 4s to list them: the page is told they are on their way, and has them
// once they are in.
func TestProvidersAnswerWhileListsComeIn(t *testing.T) {
	h := sandboxHome(t)
	t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+"/bin")
	exe := filepath.Join(h, "cursor-agent")
	testenv.Program(t, exe, `#!/bin/sh
case "$1" in
about) echo '{"userEmail":"me@example.com","subscriptionTier":"Pro"}' ;;
models) sleep 4; echo 'slow-1 - Slow One' ;;
esac
`)
	old := provider.CursorExecutable
	provider.CursorExecutable = func() string { return exe }
	provider.ForgetAccounts()
	t.Cleanup(func() {
		for deadline := time.Now().Add(10 * time.Second); provider.FetchingNew() && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
		// ForgetAccounts has Cursor's CLI asked again, but until that ask
		// answers, every look but the one waiting on it (and that one too
		// after firstAsk) is served the last answer: this test's fake
		// account. The FetchNew this test's last GET started isn't counted
		// by FetchingNew until it runs, so it can slip past the wait above
		// and be that waiting look, and a later test's provider.All()
		// listed "cursor" (TestProviderListBeforeSave on macOS CI). So the
		// accounts are forgotten with no CLI, which answers nobody at once,
		// and the cleanup waits until that is what is served.
		provider.CursorExecutable = func() string { return "" }
		provider.ForgetAccounts()
		cursor := func(p provider.Provider) bool { return p.ID == "cursor" }
		for deadline := time.Now().Add(10 * time.Second); slices.ContainsFunc(provider.All(), cursor); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the fake Cursor account was still served after the test")
				break
			}
		}
		provider.CursorExecutable = old
	})

	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	get := func(path string) (providersJSON, time.Duration) {
		t.Helper()
		start := time.Now()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		took := time.Since(start)
		var s providersJSON
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return s, took
	}
	slow := func(s providersJSON) (listed, found bool) {
		for _, p := range s.Providers {
			if p.ID == "cursor" {
				for _, m := range p.Models {
					if m.ID == "slow-1" {
						return true, true
					}
				}
				return false, true
			}
		}
		return false, false
	}

	// who is signed in is known already, as it is once magpie has run
	provider.All()
	s, took := get("/api/providers")
	if took > 2*time.Second {
		t.Fatalf("the page waited %v for Cursor's list", took)
	}
	if listed, found := slow(s); !found || listed {
		t.Fatalf("cursor found %v, its list in %v before it was fetched", found, listed)
	}
	if !s.Fetching {
		t.Fatal("not told the lists are on their way")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, _ = get("/api/providers")
		if listed, _ := slow(s); listed && !s.Fetching {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Cursor's list never came in: fetching %v", s.Fetching)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
