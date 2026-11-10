package catalog

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// Keeping the catalog fresh. It is where a model's list price comes from,
// so a model models.dev has just listed (gpt-6.1-sol, #224) has no cost
// until the cache is fetched again: a day old at most, looked at every hour
// by a magpie left running for days, and sooner when a call's model has no
// price, since models.dev may list it by now.

// staleAfter is how old the cached catalog may be before it is fetched
// again.
var staleAfter = 24 * time.Hour

// missingAfter is how old it may be, and how long since the last try,
// before a model with no price has it fetched again.
var missingAfter = 6 * time.Hour

// freshEvery is how often KeepFresh looks.
var freshEvery = time.Hour

var (
	keeping   atomic.Bool  // KeepFresh is running: Missing may fetch
	lastFetch atomic.Int64 // when fetch last started, Unix nanoseconds
	fetching  atomic.Bool
)

// KeepFresh fetches the catalog whenever it is stale, now and every hour
// after, for as long as the process runs (the app's backend, `magpie
// serve`). It never returns.
func KeepFresh() {
	keeping.Store(true)
	for {
		if Stale() {
			fetch()
		}
		time.Sleep(freshEvery)
	}
}

// Missing says a call's model has no price: the catalog is fetched again,
// in the background, when it is older than six hours and wasn't tried in
// that time. Only in a process that keeps it fresh (KeepFresh), so a CLI
// command or a test never fetches on its own.
func Missing() {
	if !keeping.Load() || time.Since(time.Unix(0, lastFetch.Load())) < missingAfter {
		return
	}
	if src := Source(); src != "" {
		if st, err := os.Stat(src); err == nil && time.Since(st.ModTime()) < missingAfter {
			return
		}
	}
	go fetch()
}

// askedAfter is how old the catalog may be when a list made of it alone
// is asked for again (Asked).
var askedAfter = 10 * time.Minute

// Asked says a model list made of the catalog alone is being fetched: a
// Claude subscription's, which is models.dev's anthropic list, as magpie
// asks Anthropic nothing in Claude Code's name. The catalog is fetched
// now, as long as ctx allows, when it is older than askedAfter and wasn't
// tried in that time, so a model launched since the day's fetch is on the
// list the user asked for (hhdaxia on X: Fable 5.2, on models.dev and in
// Claude Code's /model, not after Fetch models again). Only in a process
// that keeps the catalog fresh (KeepFresh), one fetch at a time, and a
// failed one leaves the catalog as it was.
func Asked(ctx context.Context) {
	if !keeping.Load() || time.Since(time.Unix(0, lastFetch.Load())) < askedAfter {
		return
	}
	if src := Source(); src != "" {
		if st, err := os.Stat(src); err == nil && time.Since(st.ModTime()) < askedAfter {
			return
		}
	}
	if !fetching.CompareAndSwap(false, true) {
		return
	}
	defer fetching.Store(false)
	lastFetch.Store(time.Now().UnixNano())
	if err := Sync(ctx); err != nil {
		log.Println("catalog:", err)
	}
}

// KeepFreshForTest has the catalog kept fresh from url, as the app's
// backend keeps it from models.dev, for a test of what asks for it.
func KeepFreshForTest(url string) (restore func()) {
	was := modelsDevURL
	modelsDevURL = url
	keeping.Store(true)
	lastFetch.Store(0)
	return func() { modelsDevURL = was; keeping.Store(false); lastFetch.Store(0) }
}

// fetch is Sync with a bound, one at a time.
func fetch() {
	if !fetching.CompareAndSwap(false, true) {
		return
	}
	defer fetching.Store(false)
	lastFetch.Store(time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Sync(ctx); err != nil {
		log.Println("catalog:", err)
	}
}
