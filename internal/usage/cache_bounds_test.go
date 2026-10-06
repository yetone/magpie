package usage

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestUsageSameSizeAndMtimeRewrite(t *testing.T) {
	for _, appendAfter := range []bool{false, true} {
		t.Run(fmt.Sprint("append=", appendAfter), func(t *testing.T) {
			pageHome(t)
			now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
			historyLog(t, 1034)
			Summarize(All)
			QueryPage(All, Filter{}, 0, 100)
			before, err := os.Stat(Path())
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			edited := bytes.Replace(data, []byte(`"in":10`), []byte(`"in":73`), 1)
			if bytes.Equal(edited, data) || len(edited) != len(data) {
				t.Fatal("fixture must change at the same size")
			}
			if err := os.WriteFile(Path(), edited, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(Path(), before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			if appendAfter {
				Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", Input: 19})
			}
			got, want := Summarize(All), summarize(All, now, Load(time.Time{}))
			if got.Totals != want.Totals {
				t.Fatalf("same-stat summary stale: got %+v want %+v", got.Totals, want.Totals)
			}
			equalPage(t, QueryPage(All, Filter{}, 0, 100), pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
		})
	}
}

func cacheBudget(t *testing.T, n int64) {
	t.Helper()
	old := requestCacheBytes
	requestCacheBytes = n
	t.Cleanup(func() { requestCacheBytes = old })
}

func TestUsageRawCacheBudget(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	cacheBudget(t, 128)
	historyLog(t, 1034)
	assertFresh := func() {
		t.Helper()
		cold := Load(time.Time{})
		for _, p := range []Period{Today, Week, Month, All} {
			got, want := Summarize(p), summarize(p, now, cold)
			if got.Totals != want.Totals {
				t.Fatalf("uncached %s summary stale: %+v / %+v", p, got.Totals, want.Totals)
			}
			equalPage(t, QueryPage(p, Filter{}, 0, 100), pageFromLedger(p, Filter{}, 0, 100, LedgerOf(p, Filter{})))
		}
		logIndex.Lock()
		retained := len(logIndex.snapshot.blocks)
		logIndex.Unlock()
		if retained != 0 {
			t.Fatalf("retained %d over-budget blocks", retained)
		}
		requestCache.Lock()
		chunks, pages := len(requestCache.chunks)+len(requestCache.gateways), len(requestCache.pages)
		requestCache.Unlock()
		if chunks != 0 || pages != 0 {
			t.Fatalf("uncached raw history retained chunks=%d pages=%d", chunks, pages)
		}
	}
	assertFresh()
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", Input: 11})
	assertFresh()
	historyLog(t, 20) // rewrite/truncate after the oversized metadata-only index
	assertFresh()
}

func TestUsageRawAndPricedChunksShareBudget(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	historyLog(t, 1034)
	snapshot := readLogSnapshot()
	cacheBudget(t, snapshot.bytes+1)
	equalPage(t, QueryPage(All, Filter{}, 0, 100), pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
	requestCache.Lock()
	defer requestCache.Unlock()
	retained := snapshot.bytes
	for _, c := range requestCache.gateways {
		retained += c.Bytes
	}
	for _, c := range requestCache.chunks {
		retained += c.Bytes
	}
	if retained > requestCacheBytes {
		t.Fatalf("raw + priced bytes %d exceed %d", retained, requestCacheBytes)
	}
}

func TestUsageLargeSummaryIsCompleteButNotCached(t *testing.T) {
	pageHome(t)
	for i := 0; i < summaryCacheSessions+1; i++ {
		Append(Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Session: fmt.Sprint(i), Input: 1})
	}
	got := Summarize(All)
	if len(got.Sessions) != summaryCacheSessions+1 || got.Calls != summaryCacheSessions+1 {
		t.Fatalf("large summary lost sessions: %d / %d", len(got.Sessions), got.Calls)
	}
	summaries.Lock()
	_, kept := summaries.entries[All]
	summaries.Unlock()
	if kept {
		t.Fatal("oversized Sessions retained in summary cache")
	}
}

// Rebuilding an uncached snapshot with a larger budget keeps its version and
// page key. The preceding query must leave an initialized page map to write.
func TestUsagePageCacheAfterUncachedQuery(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	historyLog(t, 1034)
	normalBudget := requestCacheBytes
	cacheBudget(t, 128)
	QueryPage(All, Filter{}, 0, 100)
	logIndex.Lock()
	uncached := logIndex.snapshot.uncached
	logIndex.Unlock()
	if !uncached {
		t.Fatal("fixture must exceed the tiny budget")
	}
	requestCache.Lock()
	key := requestCache.key
	requestCache.Unlock()

	requestCacheBytes = normalBudget
	got := QueryPage(All, Filter{}, 0, 100)
	equalPage(t, got, pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
	requestCache.Lock()
	defer requestCache.Unlock()
	if requestCache.key != key {
		t.Fatal("page key changed instead of exercising the same cache")
	}
	if _, ok := requestCache.pages[pageKey{All, Filter{}, 0, 100}]; !ok {
		t.Fatal("page not cached after rebuilding within budget")
	}
}
