package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// A page's cells before the last days are kept in page-v1.json: a start
// pairs only the recent chunks for a page of the newest rows, and builds
// the page a pairing of every chunk does. A page of older rows, a
// settled file changed, or no file, pairs them all.
func TestPageSettledFromDisk(t *testing.T) {
	now, settled, paired, restart := pageHistory(t)
	first := QueryPage(All, Filter{}, 0, 1)
	if len(*paired) != 1 || len(first.Rows) != 1 || first.Total != 5 {
		t.Fatalf("first page: pairings %v, %d rows of %d", *paired, len(first.Rows), first.Total)
	}
	all := (*paired)[0]
	if _, err := os.Stat(pageDiskPath()); err != nil {
		t.Fatal(err)
	}
	restart()
	if again := QueryPage(All, Filter{}, 0, 1); (*paired)[len(*paired)-1] >= all || !reflect.DeepEqual(again, first) {
		t.Fatalf("started again, paired %d of %d: %+v, was %+v", (*paired)[len(*paired)-1], all, again, first)
	}
	// the fourth newest is older than cut: none of the recent chunks has it
	restart()
	n := len(*paired)
	deep := QueryPage(All, Filter{}, 3, 1)
	if len(*paired) != n+1 || len(deep.Rows) != 1 || deep.Rows[0].Input != 101 {
		t.Fatalf("a page of older rows: pairings %v, rows %+v", (*paired)[n:], deep.Rows)
	}
	restart()
	os.Remove(pageDiskPath())
	pageSplitting = nil
	if cold := QueryPage(All, Filter{}, 3, 1); !reflect.DeepEqual(cold, deep) {
		t.Fatalf("a page of older rows %+v, cold %+v", deep, cold)
	}
	pageSplitting = func(n int) { *paired = append(*paired, n) }
	restart()
	os.Remove(pageDiskPath())
	QueryPage(All, Filter{}, 0, 1)
	writeCodexCall(t, settled, "b", "sb", `"vscode"`, "resp-b", "m", "low", now.AddDate(0, 0, -20).Add(-time.Hour), 1001, 10)
	restart()
	changed := QueryPage(All, Filter{}, 0, 1)
	if (*paired)[len(*paired)-1] < all {
		t.Fatalf("a settled file changed, paired %d of %d", (*paired)[len(*paired)-1], all)
	}
	restart()
	if again := QueryPage(All, Filter{}, 0, 1); (*paired)[len(*paired)-1] >= all || !reflect.DeepEqual(again, changed) {
		t.Fatalf("started again after the change, paired %d of %d: %+v, was %+v", (*paired)[len(*paired)-1], all, again, changed)
	}
	restart()
	os.Remove(pageDiskPath())
	if cold := QueryPage(All, Filter{}, 0, 1); !reflect.DeepEqual(cold, changed) {
		t.Fatalf("after a settled file changed %+v, cold %+v", changed, cold)
	}
}

// pageHistory writes five Codex calls 40, 20, 10, 5 and 0 days back, each
// with its gateway call, and counts the chunks each page's pairing pairs.
// settled is the 20-day call's file; restart drops what a start doesn't
// have.
func pageHistory(t *testing.T) (now time.Time, settled string, paired *[]int, restart func()) {
	pairAtOnce(t)
	pageHome(t)
	now = holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	for i, days := range []int{40, 20, 10, 5, 0} {
		id := string(rune('a' + i))
		at := now.AddDate(0, 0, -days).Add(-time.Hour)
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, 100+i, 10)
		if days == 20 {
			settled = path
		}
		Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	paired = &[]int{}
	pageSplitting = func(n int) { *paired = append(*paired, n) }
	t.Cleanup(func() { pageSplitting = nil })
	restart = func() {
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.pairs, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
	}
	restart()
	t.Cleanup(restart)
	return now, settled, paired, restart
}

// The Usage page names this computer's zone as time.Local and the warm-up
// names none: one page, which the warm-up's pairing built for the page.
func TestLocalZoneIsTheWarmUpsPage(t *testing.T) {
	_, _, paired, _ := pageHistory(t)
	warm := QueryPage(All, Filter{}, 0, 50)
	if len(*paired) != 1 {
		t.Fatalf("the warm-up paired %v", *paired)
	}
	if page := QueryPage(All, Filter{Zone: time.Local}, 0, 50); len(*paired) != 1 || !reflect.DeepEqual(page, warm) {
		t.Fatalf("the page in time.Local paired %v: %+v, the warm-up's %+v", *paired, page, warm)
	}
}

// A page read from near the end of page-v1.json is moved first: pages
// written after it don't drop it, and the next start reads it again.
func TestPageReadAgainStaysOnDisk(t *testing.T) {
	_, _, paired, restart := pageHistory(t)
	QueryPage(All, Filter{}, 0, 1)
	all := (*paired)[0]
	for i := range pageDiskPages - 1 {
		if err := pageDiskWrite(fmt.Sprint("other", i), time.Time{}, time.Time{}, &chunkPart{}); err != nil {
			t.Fatal(err)
		}
	}
	restart()
	if QueryPage(All, Filter{}, 0, 1); (*paired)[len(*paired)-1] >= all {
		t.Fatalf("the last page in the file, paired %d of %d", (*paired)[len(*paired)-1], all)
	}
	if err := pageDiskWrite("one more", time.Time{}, time.Time{}, &chunkPart{}); err != nil {
		t.Fatal(err)
	}
	restart()
	if QueryPage(All, Filter{}, 0, 1); (*paired)[len(*paired)-1] >= all {
		t.Fatalf("read again, then another page written: paired %d of %d", (*paired)[len(*paired)-1], all)
	}
}

// A page whose settled days are kept prices none of the gateway's blocks
// before them: a start held every block of the history priced, ~53 MB of
// its heap, for a page that reads the last days' rows only (#2278). A page
// of older rows prices them, and is what a cold build shows.
func TestKeptPageLeavesSettledBlocksUnpriced(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	for i := range logBlockRows {
		Append(Record{Time: now.AddDate(0, 0, -30).Add(time.Duration(i) * time.Second), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 1, Status: 200})
	}
	for i, days := range []int{10, 1, 0} {
		Append(Record{Time: now.AddDate(0, 0, -days).Add(-time.Hour), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10, Status: 200})
	}
	priced := 0
	blockPricing = func() { priced++ }
	t.Cleanup(func() { blockPricing = nil })
	restart := func() {
		releasePriced()
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.pairs, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
		priced = 0
	}
	restart()
	t.Cleanup(restart)
	first := QueryPage(All, Filter{}, 0, 1)
	if priced != 2 || first.Total != logBlockRows+3 {
		t.Fatalf("first page: %d blocks priced, %d rows", priced, first.Total)
	}
	restart()
	if again := QueryPage(All, Filter{}, 0, 1); priced != 1 || !reflect.DeepEqual(again, first) {
		t.Fatalf("started again, %d blocks priced: %+v, was %+v", priced, again, first)
	}
	restart()
	deep := QueryPage(All, Filter{}, 3, 1)
	if priced != 2 || len(deep.Rows) != 1 || deep.Rows[0].Agent != "claude-code" {
		t.Fatalf("a page of older rows: %d blocks priced, rows %+v", priced, deep.Rows)
	}
	restart()
	os.Remove(pageDiskPath())
	if cold := QueryPage(All, Filter{}, 3, 1); !reflect.DeepEqual(cold, deep) {
		t.Fatalf("a page of older rows %+v, cold %+v", deep, cold)
	}
}

// A page of rows older than cut keeps its cells and pairs only the chunks
// reaching back as far as its rows, each reach further only when the one
// before has too few: the newest page after a few days away paired every
// chunk of the history. It is the page a pairing of every chunk builds.
func TestOlderPageReachesBackForItsRows(t *testing.T) {
	_, _, paired, restart := pageHistory(t)
	QueryPage(All, Filter{}, 0, 1)
	all := (*paired)[0]
	var reached []int
	pageReaching = func(n int) { reached = append(reached, n) }
	t.Cleanup(func() { pageReaching = nil })
	for _, c := range []struct {
		offset, reaches int
		input           int
	}{{3, 2, 101}, {4, 3, 100}} {
		restart()
		reached = nil
		page := QueryPage(All, Filter{}, c.offset, 1)
		if len(reached) != c.reaches || len(page.Rows) != 1 || page.Rows[0].Input != c.input {
			t.Fatalf("offset %d: reached %v (all %d), rows %+v", c.offset, reached, all, page.Rows)
		}
		if reached[0] >= all {
			t.Fatalf("offset %d: the first reach paired %d of %d chunks", c.offset, reached[0], all)
		}
		restart()
		os.Remove(pageDiskPath())
		pageSplitting, pageReaching = nil, nil
		if cold := QueryPage(All, Filter{}, c.offset, 1); !reflect.DeepEqual(cold, page) {
			t.Fatalf("offset %d: reached back %+v, cold %+v", c.offset, page, cold)
		}
		pageSplitting = func(n int) { *paired = append(*paired, n) }
		pageReaching = func(n int) { reached = append(reached, n) }
		restart()
		QueryPage(All, Filter{}, 0, 1) // the settled cells on disk again
	}
}
