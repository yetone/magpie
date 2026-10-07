package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A vendor that refuses an account for its quota has its allowance read
// again at once, and what was read of it before the refusal is not trusted
// meanwhile: the share it last said is what the menu bar's "account in use"
// and the routing trace went on showing until the reading came back, and a
// reading asked before the refusal and still out came back with it and was
// trusted for a minute.
func TestStaleAllowanceForgetsTheReading(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)

	// the allowances as last read, and an account more than a minute since
	// its last reading: a reading of them is due, and is held below as the
	// one that was out when the vendor refused the account
	usedCache.Lock()
	usedCache.m, usedCache.at, usedCache.loading, usedCache.renewed =
		map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}, map[string]time.Time{}
	usedCache.m["codex"] = map[string]Allowance{
		"me@example.com":   {{Used: 8, Span: 5 * time.Hour}},
		"work@example.com": {{Used: 12, Span: 5 * time.Hour}},
	}
	usedCache.at["codex"] = time.Now().Add(-2 * time.Minute)
	usedCache.Unlock()

	// the vendor: what it says of an account is taken as the reading began,
	// so the reading held below answers as it did before the refusal and
	// the next one as the refusal left it
	var refused atomic.Bool
	held := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(held) }) }
	before := map[string]float64{"acct-1": 8, "acct-work@example.com": 12}
	var asked atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		used, ok := before[r.Header.Get("chatgpt-account-id")]
		if !ok {
			t.Errorf("usage of %q", r.Header.Get("chatgpt-account-id"))
			w.WriteHeader(400)
			return
		}
		after := refused.Load()
		asked.Add(1)
		select {
		case <-held:
		case <-r.Context().Done():
			return
		}
		if after {
			used = 100 // the account is out of its window now
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	// a reading still out must not outlive the test, and one asked again
	// must land before the caches are cleared, so that it doesn't write over
	// the next test's
	t.Cleanup(func() {
		release()
		for range 50 {
			usedCache.Lock()
			out := len(usedCache.loading)
			usedCache.Unlock()
			if out == 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading, usedCache.renewed =
			map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}, map[string]time.Time{}
		usedCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	})

	now := time.Now()
	if u, _ := Allowances("codex")["work@example.com"].For("gpt-5.5", now); u != 12 {
		t.Fatalf("work while the reading is out: used %v, want the 12 last read", u)
	}
	for range 500 { // both accounts asked, the reading out
		if asked.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if asked.Load() < 2 {
		t.Fatalf("asked %d times, want both accounts", asked.Load())
	}

	StaleAllowance("codex", "Work@Example.com") // the vendor refused it
	refused.Store(true)
	a := Allowances("codex")
	if _, known := a["work@example.com"]; known {
		t.Fatalf("work after the refusal: still read as %v, want it unknown", a["work@example.com"])
	}
	if _, known := a["me@example.com"]; !known {
		t.Fatal("me was forgotten too, want the other account kept")
	}
	release() // the reading that was out comes back, as it was asked before

	deadline := time.Now().Add(5 * time.Second)
	for {
		u, _ := Allowances("codex")["work@example.com"].For("gpt-5.5", time.Now())
		if u >= 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("work after the refusal: used %v, want the vendor's 100", u)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
