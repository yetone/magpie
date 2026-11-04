package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// magpie switched Codex back to B, its first account, and left the Codex
// app running (Restart the Codex app to follow is off), so the app's
// app-server stayed on A, the account it started on. Once A ran out the
// app greyed Send for every model while B, the one Codex is signed in to,
// had room: the app-server is restarted then, once, as a switch off a
// used-up account restarts it (nb5p on Discord).
func TestCodexAppLeftOnASpentAccountIsRestarted(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1: A
	rememberLogins(true)
	codexSignIn(t, home, "b@example.com", "r-b")
	rememberLogins(true)
	for _, u := range []string{"me@example.com", "b@example.com"} {
		if err := SetLoginOn("codex", u, true); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	held := map[string]bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		out := held[r.Header.Get("chatgpt-account-id")]
		mu.Unlock()
		used := 10
		if out {
			used = 100
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"allowed": !out, "limit_reached": out,
			"primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	fresh := func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}

	s := stubCodexApp(t)
	codexRestartsApp = func() bool { return false } // as it ships
	s.running = []int{7}
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	step := func() []int { return codexAppStep(context.Background(), now, idle) }
	t.Cleanup(func() {
		DismissCodexApp()
		codexDaemonMu.Lock()
		codexAppLeftRestarted = leftDaemon{}
		codexDaemonMu.Unlock()
	})
	// the user signs Codex in to A, and the app restarts on it
	if err := SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	step()
	s.ended, s.running = nil, []int{7}
	codexApp.Lock()
	codexApp.stale, codexApp.ended = nil, nil
	codexApp.Unlock()

	// magpie signs Codex back in to B with A not spent: the app is left
	// running, on A
	if err := switchLoginBy("codex", "b@example.com", true); err != nil {
		t.Fatal(err)
	}
	if got := step(); got != nil {
		t.Fatalf("magpie's own switch back ended %v with RestartApp off", got)
	}
	codexDaemonMu.Lock()
	codexAppLeft = leftDaemon{pid: 7, user: "me@example.com"}
	codexDaemonChecked = time.Now().Add(time.Hour) // what the listing would find
	codexDaemonMu.Unlock()
	if was := CodexAppStale(); was != "me@example.com" {
		t.Fatalf("the app is said to be on %q", was)
	}

	// A still has room: the app is left as it is
	fresh()
	restartCodexAppLeftOnSpent(context.Background())
	if got := step(); got != nil {
		t.Fatalf("ended %v with the app's account not spent", got)
	}

	// A runs out: B, the one Codex is signed in to, still has room
	mu.Lock()
	held["acct-1"] = true
	mu.Unlock()
	fresh()
	if CodexUsedUp(context.Background()) {
		t.Fatal("B taken for used up")
	}
	restartCodexAppLeftOnSpent(context.Background())
	if got := step(); !slices.Equal(got, []int{7}) {
		t.Fatalf("ended %v, want the app-server left on the used-up account", got)
	}

	// the same app-server is asked to restart once, not on every look
	s.ended = nil
	s.running = []int{7}
	codexApp.Lock()
	codexApp.stale, codexApp.ended = nil, nil
	codexApp.Unlock()
	fresh()
	restartCodexAppLeftOnSpent(context.Background())
	if got := step(); got != nil {
		t.Fatalf("restarted again: %v", got)
	}
}
