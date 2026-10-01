package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// While the first reading of the accounts' allowances since magpie started
// is still out, routing goes by what each said last, kept on disk — not by
// none, which counts every account unused and put the first in order
// first, until the reading came back and put the other first again
// (vincentzhang on Discord: a restart moved a session to the first
// account, the next back to the second, and its prompt cache was lost).
func TestAllowancesBeforeFirstReading(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	reset := func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	reset()
	t.Cleanup(reset)

	used := map[string]float64{"acct-1": 12, "acct-work@example.com": 97}
	hold := make(chan struct{})
	var slow atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			select {
			case <-hold:
			case <-r.Context().Done():
			}
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(func() { close(hold) })
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	oldWait := firstWait
	firstWait = 50 * time.Millisecond
	t.Cleanup(func() { firstWait = oldWait })

	// read once, as before the restart: kept on disk
	if u := LoginUsage(context.Background(), "codex"); len(u) != 2 {
		t.Fatalf("usage %+v", u)
	}
	// a restart, and the vendor slow to say this time
	reset()
	slow.Store(true)
	a := Allowances("codex")
	now := time.Now()
	for user, want := range map[string]float64{"me@example.com": 12, "work@example.com": 97} {
		if got, _ := a[user].For("gpt-5.5", now); got != want {
			t.Fatalf("%s before the first reading: used %v, want the last %v (%+v)", user, got, want, a)
		}
	}
}
