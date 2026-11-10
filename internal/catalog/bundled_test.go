package catalog

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// models.dev unreachable (creeksuen on X: a network in China). Each fake
// models.dev here fails the way such a network does: the connection is
// cut before an answer, the answer never comes, or a page that isn't the
// catalog comes instead (a captive portal, a cut-off body).
func unreachable(t *testing.T) map[string]string {
	t.Helper()
	hold := make(chan struct{})
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			c.Close()
		}
	}))
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body>Sign in to the network</body></html>")
	}))
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"anthropic":{"id":"anthropic","models":{"claude-haiku-4-5":{"id":"claude-ha`)
	}))
	t.Cleanup(func() {
		close(hold)
		for _, s := range []*httptest.Server{cut, hang, portal, short} {
			s.Close()
		}
	})
	return map[string]string{"cut": cut.URL, "hang": hang.URL, "portal": portal.URL, "short": short.URL}
}

func pointAt(t *testing.T, url string) {
	t.Helper()
	was := modelsDevURL
	modelsDevURL = url
	t.Cleanup(func() { modelsDevURL = was })
}

func freshCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	t.Cleanup(Reset)
}

func shipped(t *testing.T, on bool) {
	t.Helper()
	was := bundledOn
	bundledOn = on
	Reset()
	t.Cleanup(func() { bundledOn = was; Reset() })
}

const lastGood = `{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":2,"output":10}}}}}`

// A fetch that fails, however it fails, keeps the catalog fetched before,
// byte for byte, and what it answers.
func TestFailedSyncKeepsLastGood(t *testing.T) {
	for name, url := range unreachable(t) {
		t.Run(name, func(t *testing.T) {
			freshCache(t)
			pointAt(t, url)
			p := CachePath()
			if err := writeWhole(p, []byte(lastGood), time.Time{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := Sync(ctx); err == nil {
				t.Fatal("Sync from an unreachable models.dev succeeded")
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != lastGood {
				t.Fatalf("cache after a failed Sync = %q, %v; want the last good copy", b, err)
			}
			Reset()
			if pr, ok := PriceOf("openai", "gpt-6.1-sol"); !ok || pr.Output != 10 {
				t.Fatalf("price after a failed Sync = %+v, %v", pr, ok)
			}
		})
	}
}

// A first run with no network answers from the copy magpie ships with:
// prices, context windows, a provider's models. A test binary has it
// off, so it is turned on here, and off it answers nothing.
func TestFirstRunOfflineAnswersFromShippedCopy(t *testing.T) {
	urls := unreachable(t)
	freshCache(t)
	pointAt(t, urls["hang"])
	shipped(t, false)
	if _, ok := PriceOf("anthropic", "claude-haiku-4-5"); ok {
		t.Fatal("a test binary read the shipped copy with it off")
	}
	shipped(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Sync(ctx); err == nil {
		t.Fatal("Sync from a models.dev that hangs succeeded")
	}
	if Source() != "" {
		t.Fatalf("a failed Sync left a catalog file: %s", Source())
	}
	pr, ok := PriceOf("anthropic", "claude-haiku-4-5")
	if !ok || pr.Input <= 0 {
		t.Fatalf("claude-haiku-4-5 unpriced on a first run offline: %+v, %v", pr, ok)
	}
	if n := ContextOf("claude-haiku-4-5"); n <= 0 {
		t.Fatalf("claude-haiku-4-5 has no context window offline: %d", n)
	}
	if len(Provider("anthropic")) == 0 {
		t.Fatal("anthropic lists no models offline")
	}
}

// A cache that can't be parsed (cut short on disk) is passed over for the
// shipped copy, not read as no catalog.
func TestUnreadableCacheFallsBackToShippedCopy(t *testing.T) {
	freshCache(t)
	shipped(t, true)
	if err := writeWhole(CachePath(), []byte(`{"anthropic":{"id":"anthr`), time.Time{}); err != nil {
		t.Fatal(err)
	}
	Reset()
	if _, ok := PriceOf("anthropic", "claude-haiku-4-5"); !ok {
		t.Fatal("a cache cut short left no catalog")
	}
}

// Seed writes the shipped copy where Sync keeps the catalog, dated when it
// was taken, so the plugin host reads it and Stale has the real one
// fetched; it replaces an older catalog and never a newer one.
func TestSeed(t *testing.T) {
	freshCache(t)
	shipped(t, true)
	at := bundledAt
	t.Cleanup(func() { bundledAt = at })
	bundledAt = time.Now().Add(-30 * 24 * time.Hour)

	Seed()
	p := CachePath()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Seed wrote nothing: %v", err)
	}
	if !st.ModTime().Equal(bundledAt) {
		t.Fatalf("seeded copy dated %v, want %v", st.ModTime(), bundledAt)
	}
	if !Stale() {
		t.Fatal("a month-old shipped copy isn't stale: it would never be fetched again")
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, bundled()) {
		t.Fatal("seeded copy isn't the shipped one")
	}

	// a newer catalog (fetched since) is kept
	if err := writeWhole(p, []byte(lastGood), time.Time{}); err != nil {
		t.Fatal(err)
	}
	Seed()
	if b, _ := os.ReadFile(p); string(b) != lastGood {
		t.Fatal("Seed wrote over a newer catalog")
	}

	// an older one is replaced, and what is read with it
	old := bundledAt.Add(-time.Hour)
	os.Chtimes(p, old, old)
	Reset()
	if _, ok := PriceOf("openai", "gpt-6.1-sol"); !ok {
		t.Fatal("the older catalog wasn't read")
	}
	Seed()
	if b, _ := os.ReadFile(p); !bytes.Equal(b, bundled()) {
		t.Fatal("Seed kept a catalog older than the shipped copy")
	}
	if _, ok := PriceOf("anthropic", "claude-haiku-4-5"); !ok {
		t.Fatal("the shipped copy isn't read after Seed replaced an older one")
	}

	// off (a test binary), Seed writes nothing
	freshCache(t)
	shipped(t, false)
	Seed()
	if _, err := os.Stat(CachePath()); err == nil {
		t.Fatal("Seed wrote with the shipped copy off")
	}
}

// Sync writes the catalog whole through a rename: one being read while it
// is written is read as it was, never cut short (the plugin host's mdev
// and another magpie read it while a sync writes it).
func TestSyncWritesThroughRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses a rename over a file held open; this holds one open to tell the two writes apart")
	}
	freshCache(t)
	next := `{"openai":{"id":"openai","models":{"gpt-6.2":{"id":"gpt-6.2","cost":{"input":3,"output":12}}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api.json" {
			io.WriteString(w, next)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	pointAt(t, srv.URL+"/api.json")
	p := CachePath()
	if err := writeWhole(p, []byte(lastGood), time.Time{}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	held, _ := io.ReadAll(f)
	if string(held) != lastGood {
		t.Fatalf("the file a reader held open became %q: Sync wrote it in place", held)
	}
	if b, _ := os.ReadFile(p); string(b) != next {
		t.Fatalf("cache after Sync = %q", b)
	}
	if es, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".models.json-*")); len(es) > 0 {
		t.Fatalf("temp files left beside the cache: %v", es)
	}
}

// A failed fetch is tried again within minutes, not the hour, doubling up
// to it; fetch says it failed.
func TestFailedFetchRetriedSoon(t *testing.T) {
	if d := nextRetry(0); d != retryFirst || d >= freshEvery {
		t.Fatalf("first retry after %v", d)
	}
	d, n := time.Duration(0), 0
	for d < freshEvery {
		was := d
		d = nextRetry(d)
		if d <= was || d > freshEvery {
			t.Fatalf("retry after %v follows %v", d, was)
		}
		n++
	}
	if n > 8 {
		t.Fatalf("%d retries before the hour", n)
	}
	if nextRetry(freshEvery) != freshEvery {
		t.Fatal("retries grow past the hour")
	}

	urls := unreachable(t)
	freshCache(t)
	pointAt(t, urls["hang"])
	b := fetchBound
	fetchBound = 200 * time.Millisecond
	t.Cleanup(func() { fetchBound = b; lastFetch.Store(0) })
	start := time.Now()
	if err := fetch(); err == nil {
		t.Fatal("fetch from a models.dev that hangs said it succeeded")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("fetch waited %v past its bound", el)
	}
}
