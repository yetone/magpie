package usage

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A provider's list is saved again on every start and each 45 minutes,
// changed only in when it was fetched. Keyed on the file's mtime, each save
// dropped the request index's chunks (and the file of them on disk) and
// the priced summaries, so every start read the whole session history
// again. Only a list that changed may drop them.
func TestRequestStateKeptAcrossAListFetchedAgain(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	save := func(ctx int) {
		t.Helper()
		if err := catalog.SaveLive("antigravity", "https://a/v1", []catalog.Model{{ID: "m", Context: ctx}}); err != nil {
			t.Fatal(err)
		}
	}
	save(1000)
	before, price := readRequestState(now).meta, usagePriceCacheKey(now)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(catalog.LivePath("antigravity"), old, old); err != nil {
		t.Fatal(err)
	}
	save(1000)
	if st, err := os.Stat(catalog.LivePath("antigravity")); err != nil || !st.ModTime().After(old.Add(30*time.Minute)) {
		t.Fatalf("not saved again: %v %v", st, err)
	}
	if got := readRequestState(now).meta; got != before {
		t.Error("the same list fetched again changed the request index's meta")
	}
	if got := usagePriceCacheKey(now); got != price {
		t.Error("the same list fetched again changed the price key")
	}
	save(2000)
	if readRequestState(now).meta == before || usagePriceCacheKey(now) == price {
		t.Error("a list that changed kept the request meta or the price key")
	}
}
