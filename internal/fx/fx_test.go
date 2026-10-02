package fx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetFetchesAndCaches(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"success","rates":{"USD":1,"CNY":7.25,"EUR":0.9}}`))
	}))
	defer server.Close()
	old := rateURL
	rateURL = server.URL
	defer func() { rateURL = old }()

	r := Get(context.Background())
	if r.CNYPerUSD != 7.25 {
		t.Fatalf("rate = %v, want 7.25", r.CNYPerUSD)
	}
	if r.At.IsZero() || r.Stale() {
		t.Fatalf("a rate just fetched should be fresh, not %+v", r)
	}
	if calls != 1 {
		t.Fatalf("fetched %d times", calls)
	}
	if _, err := os.Stat(CachePath()); err != nil {
		t.Fatalf("no cache file written: %v", err)
	}

	// a second Get, still fresh: the in-memory copy answers, the server
	// isn't asked again
	if r2 := Get(context.Background()); r2.CNYPerUSD != 7.25 {
		t.Fatalf("second rate = %v", r2.CNYPerUSD)
	}
	if calls != 1 {
		t.Fatalf("fetched %d times, want 1 (in-memory cache should have answered)", calls)
	}

	// forgetting the in-memory copy but keeping the cache file: still no
	// network, the file answers
	Reset()
	if r3 := Get(context.Background()); r3.CNYPerUSD != 7.25 {
		t.Fatalf("rate from cache file = %v", r3.CNYPerUSD)
	}
	if calls != 1 {
		t.Fatalf("fetched %d times after Reset, want 1 (the cache file should have answered)", calls)
	}
}

func TestGetFallsBackToStaleCacheWhenOffline(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	// a cache file from well over TTL ago
	old := Rate{CNYPerUSD: 6.9, At: time.Now().Add(-48 * time.Hour)}
	writeCache(CachePath(), old)

	badURL := rateURL
	rateURL = "http://127.0.0.1:1/no-such-server"
	defer func() { rateURL = badURL }()

	r := Get(context.Background())
	if r.CNYPerUSD != 6.9 {
		t.Fatalf("rate = %v, want the stale cached 6.9", r.CNYPerUSD)
	}
	if !r.Stale() {
		t.Fatal("a rate 48h old should read as stale")
	}
}

func TestGetFallsBackToFixedDefaultWithNoCacheAndOffline(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	badURL := rateURL
	rateURL = "http://127.0.0.1:1/no-such-server"
	defer func() { rateURL = badURL }()

	r := Get(context.Background())
	if r.CNYPerUSD != Fallback {
		t.Fatalf("rate = %v, want the fixed Fallback %v", r.CNYPerUSD, Fallback)
	}
	if !r.At.IsZero() || !r.Stale() {
		t.Fatalf("the fixed default should carry no time and read as stale: %+v", r)
	}
}

func TestGetRefetchesOnceCacheIsStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	Reset()
	// a stale cache file on disk already
	stalePath := filepath.Join(dir, "magpie", "fxrate.json")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Rate{CNYPerUSD: 6.5, At: time.Now().Add(-13 * time.Hour)})
	if err := os.WriteFile(stalePath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"result":"success","rates":{"CNY":7.4}}`))
	}))
	defer server.Close()
	old := rateURL
	rateURL = server.URL
	defer func() { rateURL = old }()

	r := Get(context.Background())
	if r.CNYPerUSD != 7.4 {
		t.Fatalf("rate = %v, want the freshly fetched 7.4", r.CNYPerUSD)
	}
	if calls != 1 {
		t.Fatalf("fetched %d times, want 1", calls)
	}
}

func TestGetIgnoresAReplyWithNoCNYRate(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"success","rates":{"EUR":0.9}}`))
	}))
	defer server.Close()
	old := rateURL
	rateURL = server.URL
	defer func() { rateURL = old }()

	r := Get(context.Background())
	if r.CNYPerUSD != Fallback {
		t.Fatalf("rate = %v, want Fallback %v when the reply has no CNY", r.CNYPerUSD, Fallback)
	}
}

func TestGetDoesNotRetryAFailedFetchAtOnce(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	writeCache(CachePath(), Rate{CNYPerUSD: 6.9, At: time.Now().Add(-48 * time.Hour)})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	old := rateURL
	rateURL = server.URL
	defer func() { rateURL = old }()

	// offline, each state() asks: the stale rate comes back without the
	// network being tried again until retryAfter has passed
	for i := 0; i < 3; i++ {
		if r := Get(context.Background()); r.CNYPerUSD != 6.9 {
			t.Fatalf("rate = %v, want the stale 6.9", r.CNYPerUSD)
		}
	}
	if calls != 1 {
		t.Fatalf("fetched %d times, want 1", calls)
	}
	mu.Lock()
	failed = time.Now().Add(-retryAfter - time.Second)
	mu.Unlock()
	Get(context.Background())
	if calls != 2 {
		t.Fatalf("fetched %d times after retryAfter, want 2", calls)
	}
}
