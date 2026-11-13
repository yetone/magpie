package usage

import (
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
	if readRequestState(now).meta == before || usagePriceCacheKey(now) == price {
		t.Error("a catalog that changed kept the request meta or the price key")
	}
}
