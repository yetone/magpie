package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A Claude account's model list fetches models.dev again every 45 minutes
// (catalog.Asked), most often to the same bytes. Keyed on models.json's
// mtime, each fetch dropped the request index and the file of it on disk,
// and the next usage query read every session file again: ~15 s of CPU
// over a long history. Only a catalog that changed may drop them.
func TestRequestStateKeptAcrossACatalogFetchedAgain(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	p := catalog.CachePath()
	write := func(b string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const one = `{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":2,"output":10}}}}}`
	write(one)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	before, price := readRequestState(now).meta, usagePriceCacheKey(now)
	write(one)
	if st, err := os.Stat(p); err != nil || !st.ModTime().After(old.Add(30*time.Minute)) {
		t.Fatalf("not written again: %v %v", st, err)
	}
	if got := readRequestState(now).meta; got != before {
		t.Error("the same catalog fetched again changed the request index's meta")
	}
	if got := usagePriceCacheKey(now); got != price {
		t.Error("the same catalog fetched again changed the price key")
	}
	write(`{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":3,"output":10}}}}}`)
	if usagePriceCacheKey(now) == price {
		t.Error("a catalog that changed kept the price key")
	}
}

// models.dev changes several times a day, mostly in aggregators' lists no
// call of the user's names. The request index is built again only when a
// price or a listed name its rows hold is answered otherwise, across a
// restart too.
func TestRequestStateKeptAcrossACatalogChangeNoRowAsks(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	p := catalog.CachePath()
	at := time.Now().Add(-2 * time.Hour)
	reset := true
	write := func(sol, kilo int) {
		t.Helper()
		b := fmt.Sprintf(`{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":%d,"output":10}}}},`+
			`"kilo":{"id":"kilo","models":{"x/y":{"id":"x/y","cost":{"input":%d,"output":1}}}}}`, sol, kilo)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Minute) // each write a time of its own
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		if reset {
			catalog.Reset() // as Sync does once the file changed
		}
	}
	// a session file's call priced at the catalog's word (#4278)
	row := Record{Provider: UnknownProvider, SessionProvider: "openai", Model: "gpt-6.1-sol", Input: 1}
	priced := func(want float64) {
		t.Helper()
		pr := Pricer()(row)
		if pr == nil || pr.Input != want {
			t.Fatalf("priced %+v, want input %v", pr, want)
		}
	}
	// rows reach a file only after their asks did (saveAsks)
	restart := func() {
		saveAsks()
		asks.Lock()
		asks.path = ""
		asks.Unlock()
	}
	write(2, 1)
	meta := readRequestState(now).meta
	priced(2)
	write(2, 5)
	if readRequestState(now).meta != meta {
		t.Fatal("a change to a model no row names built the index again")
	}
	write(3, 5)
	again := readRequestState(now).meta
	if again == meta {
		t.Fatal("a new price of a model a row names kept the index")
	}
	priced(3)
	restart()
	write(3, 7)
	if readRequestState(now).meta != again {
		t.Fatal("after a restart, a change no row asks of built the index again")
	}
	restart()
	write(4, 7)
	fourth := readRequestState(now).meta
	if fourth == again {
		t.Fatal("after a restart, a new price a row holds kept the index")
	}
	// Sync writes the file before it resets the catalog: a check between
	// the two asks the catalog the file no longer holds, and the reset
	// asks again
	priced(4)
	reset = false
	write(5, 7)
	if readRequestState(now).meta != fourth {
		t.Fatal("the catalog still loaded answered otherwise")
	}
	catalog.Reset()
	if readRequestState(now).meta == fourth {
		t.Fatal("a check before the catalog was read again kept the index past it")
	}
}
