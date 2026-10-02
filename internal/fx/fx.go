// Package fx keeps the dollar-to-yuan rate the CNY currency preference (see
// internal/settings) converts list-price costs with. It asks a free,
// no-key endpoint for it, keeps what it got for TTL in a small cache file
// beside magpie's other caches, and falls back to the last rate it ever
// had — or a fixed default, when it has never reached the network — while
// offline. Nothing here changes what usage.Record and its Totals store:
// they stay in USD, and only what's shown is converted.
package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// rateURL is a free, no-key endpoint that gives every currency's rate
// against one US dollar; a var so a test can point it at a fake server.
var rateURL = "https://open.er-api.com/v6/latest/USD"

// SetRateURL points the rate at u until the returned func puts it back; for
// other packages' tests.
func SetRateURL(u string) (restore func()) {
	old := rateURL
	rateURL = u
	return func() { rateURL = old }
}

// TTL is how long a fetched rate is used before another is asked for.
const TTL = 12 * time.Hour

// Fallback is the rate shown when magpie has never reached rateURL and has
// no cache of its own: roughly what a dollar has been worth in yuan,
// fixed so a first run offline still shows something plausible rather
// than nothing.
const Fallback = 7.2

// Rate is a USD→CNY snapshot: how much one dollar is worth right now
// (CNYPerUSD) and when that was learned (At; the zero time for Fallback,
// which was never learned from anywhere).
type Rate struct {
	CNYPerUSD float64   `json:"rate"`
	At        time.Time `json:"at"`
}

// Stale is true once a rate is older than TTL — shown on hover so "using
// yesterday's rate, offline" reads differently from "just fetched".
func (r Rate) Stale() bool { return r.At.IsZero() || time.Since(r.At) >= TTL }

// retryAfter is how long a failed fetch is not tried again: offline, the
// stale rate is shown at once rather than each state() waiting on the
// network again.
const retryAfter = 10 * time.Minute

var (
	mu     sync.Mutex
	mem    *Rate     // this process's own copy, so a redraw never touches disk
	failed time.Time // when a fetch last failed
)

// CachePath is where the rate is kept between runs.
func CachePath() string { return filepath.Join(appdir.Cache(), "fxrate.json") }

// Get is the rate to show right now: what this process (or, failing that,
// the cache file) last learned, if it's under TTL old; else a fresh fetch
// of rateURL, kept in the cache for next time; else — offline, or the
// endpoint's reply didn't parse — the last rate however old, or Fallback
// when there has never been one.
func Get(ctx context.Context) Rate { return getAt(ctx, CachePath()) }

// getAt is Get with the cache file at path: Soon's fetch behind a page
// keeps the one it was started with, and never writes a rate into a home
// it wasn't read from (a test's next one)
func getAt(ctx context.Context, path string) Rate {
	mu.Lock()
	if mem != nil && !mem.Stale() {
		r := *mem
		mu.Unlock()
		return r
	}
	have := mem != nil
	recent := !failed.IsZero() && time.Since(failed) < retryAfter
	mu.Unlock()

	if !have {
		if r, ok := readCache(path); ok {
			mu.Lock()
			mem = &r
			mu.Unlock()
			if !r.Stale() {
				return r
			}
		}
	}

	if !recent {
		r, err := fetchLive(ctx)
		// a fetch begun for another home (a test's, gone) tells this one nothing
		if CachePath() != path {
			return Rate{CNYPerUSD: Fallback}
		}
		if err == nil {
			writeCache(path, r)
			mu.Lock()
			mem, failed = &r, time.Time{}
			mu.Unlock()
			return r
		}
		mu.Lock()
		failed = time.Now()
		mu.Unlock()
	}

	mu.Lock()
	defer mu.Unlock()
	if mem != nil {
		return *mem
	}
	return Rate{CNYPerUSD: Fallback}
}

// refreshing is held while Soon's fetch behind it runs: one at a time.
var refreshing sync.Mutex

// Soon is Get without the wait, for a page that must not stand on the
// network (#541): the rate known now — this process's, the cache file's,
// else Fallback — and, when that is stale, Get behind it (one at a time),
// for the next look to have.
func Soon() Rate {
	path := CachePath()
	mu.Lock()
	if mem == nil {
		if r, ok := readCache(path); ok {
			mem = &r
		}
	}
	r := Rate{CNYPerUSD: Fallback}
	if mem != nil {
		r = *mem
	}
	mu.Unlock()
	if r.Stale() && refreshing.TryLock() {
		go func() {
			defer refreshing.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			getAt(ctx, path)
		}()
	}
	return r
}

// Settle waits for a fetch Soon started to end; for tests, whose home goes
// once they do.
func Settle() {
	refreshing.Lock()
	refreshing.Unlock()
}

// Reset forgets the in-memory rate, so the next Get rereads the cache file
// or asks rateURL again; tests use it between cases.
func Reset() {
	mu.Lock()
	mem, failed = nil, time.Time{}
	mu.Unlock()
}

// erAPI is the shape of open.er-api.com's reply (and exchangerate-api.com's,
// the same service): {"result":"success","rates":{"CNY":7.2,...}}.
type erAPI struct {
	Rates map[string]float64 `json:"rates"`
}

func fetchLive(ctx context.Context) (Rate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rateURL, nil)
	if err != nil {
		return Rate{}, err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Rate{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Rate{}, fmt.Errorf("%s: %s", rateURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Rate{}, err
	}
	var v erAPI
	if err := json.Unmarshal(b, &v); err != nil {
		return Rate{}, err
	}
	cny := v.Rates["CNY"]
	if cny <= 0 {
		return Rate{}, fmt.Errorf("%s: no CNY rate in the reply", rateURL)
	}
	return Rate{CNYPerUSD: cny, At: time.Now()}, nil
}

func readCache(path string) (Rate, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Rate{}, false
	}
	var r Rate
	if err := json.Unmarshal(b, &r); err != nil || r.CNYPerUSD <= 0 {
		return Rate{}, false
	}
	return r, true
}

func writeCache(p string, r Rate) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o644)
}
