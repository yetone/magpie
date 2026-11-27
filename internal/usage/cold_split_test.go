package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// pairAtOnce has a first build pair every chunk at once, for the tests
// of what a pairing of all of them is told apart from.
func pairAtOnce(t *testing.T) {
	span := coldSpan
	coldSpan = 0
	t.Cleanup(func() { coldSpan = span })
}

// A first start pairs the history a few weeks at a time, not at once
// (#2278), and builds the page, the heatmap and Codex's effort a pairing
// of every chunk does: a call whose gateway row is after a span's end is
// paired, and a span pairs only the chunks it reaches.
func TestFirstBuildPairsSpans(t *testing.T) {
	yesterday, _ := advanceHistory(t)
	// a call just before midnight, its gateway row just after
	at := time.Date(2026, 9, 23, 23, 59, 30, 0, time.Local)
	path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-z.jsonl")
	writeCodexCall(t, path, "z", "sz", `"vscode"`, "resp-z", "m", "high", at, 300, 30)
	Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 300, Output: 30, Status: 200, ResponseID: "resp-z", Session: "sz"})
	// a Codex thread written over three weeks, one file: a call 20 days
	// back and one 2 days back, each through magpie too
	writeLongCodexThread(t, yesterday.AddDate(0, 0, -20), yesterday.AddDate(0, 0, -1))
	// a call through magpie alone, 30 days back
	Append(Record{Time: yesterday.AddDate(0, 0, -30), Agent: "claude-code", Provider: "anthropic", Model: "c", Input: 50, Output: 5, Status: 200})
	restart := func() {
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pairs, requestCache.pages = nil, nil, nil, nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
		codexEffort.Lock()
		codexEffort.periods = map[Period]*effortEntry{}
		codexEffort.Unlock()
		codexEffortBuilding.Lock()
		effortOlds = map[Period]*effortOld{}
		codexEffortBuilding.Unlock()
		for _, path := range []string{pageDiskPath(), heatDiskPath(), effortDiskPath()} {
			os.Remove(path)
		}
	}
	restart()
	t.Cleanup(restart)
	type built struct {
		page    RequestPage
		heat    Heatmap
		effort  map[string][]sessions.ModelEffort
		written bool
	}
	build := func() built {
		var b built
		b.page = QueryPage(All, Filter{}, 0, 50)
		b.heat = HeatmapOf(Filter{})
		b.effort = CodexModelEffort(All)
		_, page := os.Stat(pageDiskPath())
		_, heat := os.Stat(heatDiskPath())
		_, effort := os.Stat(effortDiskPath())
		b.written = page == nil && heat == nil && effort == nil
		return b
	}
	paired := []int{}
	pageSplitting = func(n int) { paired = append(paired, n) }
	whole := build()
	pageSplitting = nil
	if !whole.written || len(paired) == 0 {
		t.Fatal("a pairing of every chunk wrote no sums")
	}
	all := paired[0]
	for _, days := range []int{1, 4, 14} {
		coldSpan = days
		restart()
		spans := []int{}
		coldSpanning = func(n int) { spans = append(spans, n) }
		got := build()
		coldSpanning = nil
		if !got.written {
			t.Fatalf("%d days a span: no sums written", days)
		}
		if len(spans) < 2 {
			t.Fatalf("%d days a span: %d spans", days, len(spans))
		}
		pairedBelow(t, "a span", spans, 0, all)
		if !reflect.DeepEqual(got.page, whole.page) {
			t.Fatalf("%d days a span: page %+v, paired at once %+v", days, got.page, whole.page)
		}
		if !reflect.DeepEqual(got.heat, whole.heat) {
			t.Fatalf("%d days a span: heatmap %+v, paired at once %+v", days, got.heat, whole.heat)
		}
		if !reflect.DeepEqual(got.effort, whole.effort) {
			t.Fatalf("%d days a span: effort %+v, paired at once %+v", days, got.effort, whole.effort)
		}
	}
	if whole.page.Total != 11 {
		t.Fatalf("page: %d rows", whole.page.Total)
	}
}

// A first start pairs the history once for the warm-up's page and
// Codex's effort, whichever is asked first, and tells what each pairing
// of its own does (#2278).
func TestFirstBuildPairsOnceForPageAndEffort(t *testing.T) {
	yesterday, _ := advanceHistory(t)
	writeLongCodexThread(t, yesterday.AddDate(0, 0, -20), yesterday.AddDate(0, 0, -1))
	coldSpan = 4
	restart := func() {
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pairs, requestCache.pages = nil, nil, nil, nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
		codexEffort.Lock()
		codexEffort.periods = map[Period]*effortEntry{}
		codexEffort.Unlock()
		codexEffortBuilding.Lock()
		effortOlds = map[Period]*effortOld{}
		codexEffortBuilding.Unlock()
		for _, path := range []string{pageDiskPath(), heatDiskPath(), effortDiskPath()} {
			os.Remove(path)
		}
	}
	restart()
	t.Cleanup(restart)
	spans := 0
	coldSpanning = func(int) { spans++ }
	t.Cleanup(func() { coldSpanning = nil })
	page := QueryPage(All, Filter{}, 0, 50)
	restart()
	effort := CodexModelEffort(All)
	restart()
	spans = 0
	if got := QueryPage(All, Filter{}, 0, 50); !reflect.DeepEqual(got, page) {
		t.Fatalf("page first: %+v, alone %+v", got, page)
	}
	first := spans
	if got := CodexModelEffort(All); !reflect.DeepEqual(got, effort) || spans != first || first == 0 {
		t.Fatalf("page first: effort %+v after %d spans of %d, alone %+v", got, spans, first, effort)
	}
	restart()
	spans = 0
	if got := CodexModelEffort(All); !reflect.DeepEqual(got, effort) {
		t.Fatalf("effort first: %+v, alone %+v", got, effort)
	}
	first = spans
	if got := QueryPage(All, Filter{}, 0, 50); !reflect.DeepEqual(got, page) || spans != first || first == 0 {
		t.Fatalf("effort first: page %+v after %d spans of %d, alone %+v", got, spans, first, page)
	}
}

// writeLongCodexThread writes one Codex rollout with a call at each of
// ats, and the gateway's row of each.
func writeLongCodexThread(t *testing.T, ats ...time.Time) {
	t.Helper()
	path := filepath.Join(sessions.CodexDir(), "sessions", ats[0].Format("2006/01/02"), "rollout-"+ats[0].Format("2006-01-02T15-04-05")+"-long.jsonl")
	lines := []any{map[string]any{"timestamp": ats[0].Add(-time.Minute).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "long", "session_id": "slong", "source": "vscode"}}}
	for i, at := range ats {
		response := fmt.Sprintf("resp-long-%d", i)
		lines = append(lines,
			map[string]any{"timestamp": at.Add(-time.Second).Format(time.RFC3339Nano), "type": "turn_context", "payload": map[string]any{"turn_id": "turn-" + response, "model": "m", "effort": "low"}},
			map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "token_usage_record", "payload": map[string]any{
				"response_id": response, "session_id": "slong", "thread_id": "long", "turn_id": "turn-" + response,
				"usage": map[string]int{"input_tokens": 70 + i, "cached_input_tokens": 0, "cache_write_input_tokens": 0, "output_tokens": 7, "reasoning_output_tokens": 0, "total_tokens": 77 + i},
			}})
		Append(Record{Time: at.Add(time.Second), Agent: "codex", Provider: "openai", Model: "m", Input: 70 + i, Output: 7, Status: 200, ResponseID: response, Session: "slong"})
	}
	var data []byte
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, b...), '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// A first start's spans begin on the oldest row's day in the reader's
// zone, the zone the cut is in. A call stamped in UTC early on a day that
// is still the day before west of UTC (a reported call's "t", a session
// file's timestamp) began before the first span and was counted in no
// total of All nor in the heatmap's first day, while the page still
// listed it (#8000).
func TestColdSpansBeginOnTheOldestRowsLocalDay(t *testing.T) {
	zone := time.Local
	time.Local = time.FixedZone("west", -8*3600)
	t.Cleanup(func() { time.Local = zone })
	pageHome(t)
	now := holdClock(t, time.Date(2026, 11, 27, 12, 0, 0, 0, time.Local))
	// 00:24 UTC on 10-17, 16:24 on 10-16 here
	Append(Record{Time: time.Date(2026, 10, 17, 0, 24, 29, 0, time.UTC), Agent: "claude-code", Provider: "openai", Model: "m", Input: 1000, Output: 100, Status: 200})
	for i := range 3 {
		Append(Record{Time: now.Add(-time.Duration(i+1) * time.Hour).UTC(), Agent: "claude-code", Provider: "openai", Model: "m", Input: 10, Output: 1, Status: 200})
	}
	restart := func() {
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pairs, requestCache.pages = nil, nil, nil, nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
		for _, path := range []string{pageDiskPath(), heatDiskPath(), effortDiskPath()} {
			os.Remove(path)
		}
	}
	restart()
	t.Cleanup(restart)
	// a page of one row: its newest are since the cut, and its totals
	// are the kept cells' of the rows before
	build := func() (RequestPage, Heatmap) { return QueryPage(All, Filter{}, 0, 1), HeatmapOf(Filter{}) }
	spans := 0
	coldSpanning = func(int) { spans++ }
	t.Cleanup(func() { coldSpanning = nil })
	page, heat := build()
	if spans == 0 {
		t.Fatal("no span paired")
	}
	if page.Total != 4 || page.Sum.Input != 1030 {
		t.Fatalf("page: %d rows, %d in, want 4 rows, 1030 in", page.Total, page.Sum.Input)
	}
	if len(heat.Days) == 0 || heat.Days[0].Date != "2026-10-16" || heat.Days[0].Calls != 1 {
		t.Fatalf("heatmap: %+v, want 2026-10-16 first, 1 call", heat.Days)
	}
	restart()
	span := coldSpan
	coldSpan = 0
	t.Cleanup(func() { coldSpan = span })
	whole, wholeHeat := build()
	if page.Total != whole.Total || !reflect.DeepEqual(page.Sum, whole.Sum) || !reflect.DeepEqual(heat, wholeHeat) {
		t.Fatalf("spans: %d rows %+v, %+v; paired at once: %d rows %+v, %+v", page.Total, page.Sum, heat, whole.Total, whole.Sum, wholeHeat)
	}
}
