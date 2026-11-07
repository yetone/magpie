package usage

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The heatmap (#1369) is the last 53 weeks from a Monday, a day each by the
// local calendar: the rows the chart's filter keeps, the day picked set
// aside, without the gateway's own rejections, the days before its first
// Monday or after today. The streaming page and the ledger's agree.
func TestHeatmapDays(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local)) // a Friday
	from := time.Date(2025, 10, 6, 0, 0, 0, 0, time.Local)               // the Monday 52 weeks before this week's
	if got := heatmapSince(now); !got.Equal(from) || got.Weekday() != time.Monday {
		t.Fatalf("heatmap starts %v, want %v", got, from)
	}
	today := time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local)
	gateway := &rowChunk{}
	all := Ledgered{Agents: []string{"codex"}, Providers: []string{"a", "b"}}
	for i, r := range []Record{
		{Time: from.Add(-time.Nanosecond), Provider: "a", Input: 1000},             // the Sunday before: not shown
		{Time: from, Provider: "a", Input: 10, Output: 5, CacheRead: 100},          // the first day
		{Time: from.Add(23 * time.Hour).UTC(), Provider: "b", Input: 20},           // the same local day, stamped in UTC
		{Time: today.Add(-time.Nanosecond), Provider: "a", Input: 30, Status: 500}, // yesterday, failed: still a call
		{Time: today.Add(time.Hour), Provider: "a", Input: 40},
		{Time: today.Add(2 * time.Hour), Provider: "a", Input: 999, Rejected: true},
		{Time: today.Add(time.Hour), Provider: "a", Input: 7, Output: 1}, // no known price
	} {
		r.Agent, r.Model = "codex", "m"
		row := Row{Record: r, Cost: float64(r.Input) / 100, Priced: i != 6}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
		if !r.IsRejected() {
			all.Sum.addRow(row)
		}
	}
	for name, query := range map[string]func(Filter) RequestPage{
		"compact": func(f Filter) RequestPage { return buildRequestPage(heatmapPeriod, f, 0, 1, gateway, nil) },
		"ledger":  func(f Filter) RequestPage { return pageFromLedger(heatmapPeriod, f, 0, 1, all) },
	} {
		t.Run(name, func(t *testing.T) {
			h := query(Filter{}).Heat
			if h == nil {
				t.Fatal("no heatmap")
			}
			if h.From != "2025-10-06" || h.To != "2026-10-09" {
				t.Fatalf("span: %s to %s", h.From, h.To)
			}
			want := []Day{
				{Date: "2025-10-06", Calls: 2, Tokens: 135, Cost: 0.3},
				{Date: "2026-10-08", Calls: 1, Tokens: 30, Cost: 0.3},
				{Date: "2026-10-09", Calls: 2, Tokens: 48, Cost: 0.4, Unpriced: 1},
			}
			for i := range h.Days {
				h.Days[i].Cost = math.Round(h.Days[i].Cost*1e9) / 1e9
			}
			if !reflect.DeepEqual(h.Days, want) {
				t.Fatalf("days:\n got %+v\nwant %+v", h.Days, want)
			}
			// a provider picked: only its rows; a day picked: still every day
			if h := query(Filter{Provider: "b", Day: "2026-10-09"}).Heat; len(h.Days) != 1 || h.Days[0].Date != "2025-10-06" || h.Days[0].Tokens != 20 {
				t.Fatalf("provider b's days: %+v", h.Days)
			}
			if p := query(Filter{}); p.Heat == nil {
				t.Fatal("heatmap dropped")
			}
		})
	}
	if p := buildRequestPage(Month, Filter{}, 0, 1, gateway, nil); p.Heat != nil {
		t.Fatal("a page of another period has no heatmap")
	}
}

// HeatmapOf reads the log itself: a call appended today is today's, and the
// filter given narrows it.
func TestHeatmapOf(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local))
	for i, at := range []time.Time{now.AddDate(0, 0, -400), now.AddDate(0, -3, 0), now.Add(-time.Hour), now.Add(-time.Minute)} {
		Append(Record{Time: at, Agent: "claude", Provider: "openai", Model: "m", Input: 10 + i, Status: 200})
	}
	Append(Record{Time: now.Add(-time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100, Status: 200})
	h := HeatmapOf(Filter{Agent: "claude", Day: "2026-10-09"})
	want := []Day{{Date: now.AddDate(0, -3, 0).Format(time.DateOnly), Calls: 1, Tokens: 11}, {Date: "2026-10-09", Calls: 2, Tokens: 25}}
	for i := range h.Days {
		h.Days[i].Cost = 0
	}
	if h.From != "2025-10-06" || h.To != "2026-10-09" || !reflect.DeepEqual(h.Days, want) {
		t.Fatalf("heatmap: %+v", h)
	}
}

// A live agent writes its session file all the time. The heatmap is kept
// while only those files change, for heatFresh, so the Requests tab's
// refresh doesn't work out a year of calls again each time (#3493); a call
// through magpie is still counted at once, and a session's call once
// heatFresh is up.
func TestHeatmapKeptWhileOnlySessionsChange(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	builds := 0
	building = func() { builds++ }
	t.Cleanup(func() { building = nil })
	calls := func() int {
		n := 0
		for _, d := range HeatmapOf(Filter{}).Days {
			n += d.Calls
		}
		return n
	}
	file := func(id string) string {
		return filepath.Join(sessions.CodexDir(), "sessions", "2026", "09", "30", "rollout-2026-09-30T10-00-00-"+id+".jsonl")
	}
	writeCodexCall(t, file("a"), "a", "sa", `"vscode"`, "resp-a", "m", "low", now.Add(-time.Hour), 100, 10)
	if n := calls(); n != 1 || builds != 1 {
		t.Fatalf("%d calls, %d builds", n, builds)
	}
	writeCodexCall(t, file("b"), "b", "sb", `"vscode"`, "resp-b", "m", "low", now.Add(-time.Hour), 100, 10)
	if n := calls(); n != 1 || builds != 1 {
		t.Fatalf("a session's new call worked the heatmap out again: %d calls, %d builds", n, builds)
	}
	if p := QueryPage(Today, Filter{}, 0, 100); p.Total != 2 {
		t.Fatalf("the Requests page kept too: %d rows", p.Total)
	}
	Append(Record{Time: now.Add(-time.Minute), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 5, Status: 200})
	if n := calls(); n != 3 {
		t.Fatalf("a call through magpie isn't counted at once: %d calls", n)
	}
	writeCodexCall(t, file("c"), "c", "sc", `"vscode"`, "resp-c", "m", "low", now.Add(-time.Hour), 100, 10)
	old := heatFresh
	heatFresh = 0
	t.Cleanup(func() { heatFresh = old })
	if n := calls(); n != 4 {
		t.Fatalf("heatFresh up, %d calls", n)
	}
}

// A call through magpie changes the gateway's log, and the heatmap is
// worked out again at once: from the sums of the chunks it had, adding up
// again only those the call changed, not every row of the year (#2278).
// What it shows is what a cold build shows.
func TestHeatmapAfterACallAddsUpOnlyWhatChanged(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	const files = 8
	for i := range files {
		at := now.AddDate(0, 0, -3*i).Add(-time.Hour)
		id := string(rune('a' + i))
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, 100+i, 10)
	}
	Append(Record{Time: now.AddDate(0, 0, -40), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 3, Status: 200})
	// the gateway is in use: its log has a call of the last days already
	Append(Record{Time: now.AddDate(0, 0, -1), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 4, Status: 200})
	read := 0
	heatReading = func(*rowChunk) { read++ }
	t.Cleanup(func() { heatReading = nil })
	calls := func(h Heatmap) int {
		n := 0
		for _, d := range h.Days {
			n += d.Calls
		}
		return n
	}
	if h := HeatmapOf(Filter{}); calls(h) != files+2 || read < files {
		t.Fatalf("cold: %d calls, %d chunks added up", calls(h), read)
	}
	read = 0
	Append(Record{Time: now.Add(-time.Minute), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 5, Status: 200})
	warm := HeatmapOf(Filter{})
	if calls(warm) != files+3 {
		t.Fatalf("a call through magpie isn't counted at once: %d calls", calls(warm))
	}
	if read > 1 {
		t.Fatalf("a call added up %d chunks again, want only the one it changed", read)
	}
	requestCache.Lock()
	requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
	requestCache.Unlock()
	if cold := HeatmapOf(Filter{}); !reflect.DeepEqual(cold, warm) {
		t.Fatalf("kept sums %+v, cold %+v", warm, cold)
	}
}

// A call through magpie pairs again only the chunks of the last days: the
// days before are the last pairing's of every chunk (#2278). What it shows
// is what a cold build shows, with calls paired across the days' edge, a
// session file of old and new calls, and the pairing kept no longer than
// heatOldFresh, or once a settled file changes.
func TestHeatmapAfterACallPairsOnlyTheLastDays(t *testing.T) {
	pairAtOnce(t)
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	cut := time.Date(2026, 9, 28, 0, 0, 0, 0, time.Local)
	write := func(id string, at time.Time, input int) string {
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, input, 10)
		return path
	}
	// the gateway's calls of the session files' own: each counted once
	gateway := func(id string, at time.Time, input int) {
		Append(Record{Time: at, Agent: "codex", Provider: "openai", Model: "m", Input: input, Output: 10, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	// a full block of the gateway's log whose newest call is before cut,
	// the call of a session file's row after it
	for range logBlockRows - 1 {
		Append(Record{Time: now.AddDate(0, 0, -30), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 1, Status: 200})
	}
	gateway("x", cut.Add(-time.Minute), 120)
	write("x", cut.Add(time.Minute), 120)
	for i, at := range []time.Time{now.AddDate(0, 0, -40), now.AddDate(0, 0, -10), now.AddDate(0, 0, -5), cut.Add(-time.Minute), cut.Add(time.Minute), now.Add(-time.Hour)} {
		id := string(rune('a' + i))
		write(id, at, 100+i)
		if i%2 == 1 {
			// two minutes on: the call at cut-1m is the gateway's after cut
			gateway(id, at.Add(2*time.Minute), 100+i)
		}
	}
	settledFile := write("old", now.AddDate(0, 0, -20), 7)
	for i := range 6 {
		write(fmt.Sprintf("o%d", i), now.AddDate(0, 0, -12-3*i), 5)
	}
	Append(Record{Time: now.AddDate(0, 0, -60), Agent: "claude-code", Provider: "anthropic", Model: "x", Input: 3, Status: 200})
	paired := []int{}
	heatPairing = func(n int) { paired = append(paired, n) }
	t.Cleanup(func() { heatPairing = nil })
	cold := func() Heatmap {
		requestCache.Lock()
		requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
		os.Remove(heatDiskPath())
		return HeatmapOf(Filter{})
	}
	first := HeatmapOf(Filter{})
	if len(paired) != 1 {
		t.Fatalf("paired %v", paired)
	}
	all := paired[0]
	call := 0
	next := func() Heatmap {
		call++
		gateway(fmt.Sprintf("new%d", call), now.Add(-time.Duration(call)*time.Second), 50)
		return HeatmapOf(Filter{})
	}
	for range 3 {
		warm := next()
		if n := paired[len(paired)-1]; n >= all/2 {
			t.Fatalf("a call paired %d of %d chunks again", n, all)
		}
		if c := cold(); !reflect.DeepEqual(warm, c) {
			t.Fatalf("after the last days' pairing %+v, cold %+v", warm, c)
		}
	}
	if reflect.DeepEqual(first, cold()) {
		t.Fatal("the calls weren't counted")
	}
	// a session file of the last days that holds a call of the days before
	write("e", cut.Add(time.Minute), 104)
	path := filepath.Join(sessions.CodexDir(), "sessions", cut.Add(-30*time.Hour).Format("2006/01/02"), "rollout-"+cut.Add(-30*time.Hour).Format("2006-01-02T15-04-05")+"-mixed.jsonl")
	writeCodexCall(t, path, "mixed", "smixed", `"vscode"`, "resp-mixed-old", "m", "low", cut.Add(-30*time.Hour), 9, 1)
	cold()
	writeCodexCall(t, path, "mixed", "smixed", `"vscode"`, "resp-mixed-new", "m", "low", now.Add(-time.Minute), 9, 1)
	if warm := next(); !reflect.DeepEqual(warm, cold()) {
		t.Fatalf("a file of old and new calls: %+v, cold %+v", warm, cold())
	}
	// a new session file that holds a call of last week too (a rollout
	// forked from an old one): its call of then is counted
	next() // kept: the pairing the call below starts from
	scratch := t.TempDir()
	writeCodexCall(t, filepath.Join(scratch, "then"), "fork", "sfork", `"vscode"`, "resp-fork-then", "m", "low", now.AddDate(0, 0, -7), 30, 3)
	writeCodexCall(t, filepath.Join(scratch, "now"), "fork", "sfork", `"vscode"`, "resp-fork-now", "m", "low", now.Add(-2*time.Minute), 31, 3)
	then, err := os.ReadFile(filepath.Join(scratch, "then"))
	if err != nil {
		t.Fatal(err)
	}
	later, err := os.ReadFile(filepath.Join(scratch, "now"))
	if err != nil {
		t.Fatal(err)
	}
	_, later, _ = bytes.Cut(later, []byte("\n")) // its session_meta is then's
	// the session began then
	then = bytes.Replace(then, []byte(`"2026-09-30T10:00:00Z"`), []byte(`"`+now.AddDate(0, 0, -7).Add(-time.Minute).UTC().Format(time.RFC3339)+`"`), 1)
	forked := filepath.Join(sessions.CodexDir(), "sessions", now.Format("2006/01/02"), "rollout-"+now.Add(-2*time.Minute).Format("2006-01-02T15-04-05")+"-fork.jsonl")
	if os.WriteFile(forked, append(then, later...), 0600) != nil {
		t.Fatal("fork")
	}
	if warm := next(); !reflect.DeepEqual(warm, cold()) {
		t.Fatalf("a new file with a call of last week: %+v, cold %+v", warm, cold())
	}
	// a settled file written again: every chunk is paired again
	next()
	writeCodexCall(t, settledFile, "old", "sold", `"vscode"`, "resp-old", "m", "low", now.AddDate(0, 0, -20), 70, 10)
	if os.Chtimes(settledFile, now.AddDate(0, 0, -20), now.AddDate(0, 0, -20)) != nil {
		t.Fatal("chtimes")
	}
	before := len(paired)
	warm := next()
	if paired[before] < all {
		t.Fatalf("a settled file changed, paired %d of %d", paired[before], all)
	}
	if !reflect.DeepEqual(warm, cold()) {
		t.Fatalf("a settled file changed: %+v, cold %+v", warm, cold())
	}
	// past heatOldFresh the days are named again, and paired again only
	// when what they were counted of changed: a thread's identity learnt
	old := heatOldFresh
	heatOldFresh = 0
	t.Cleanup(func() { heatOldFresh = old })
	before = len(paired)
	next()
	if paired[before] >= all {
		t.Fatalf("past heatOldFresh with nothing changed, paired %d of %d", paired[before], all)
	}
	write("forked-id", now.AddDate(0, 0, -15), 8)
	before = len(paired)
	if warm := next(); paired[before] < all || !reflect.DeepEqual(warm, cold()) {
		t.Fatalf("past heatOldFresh with a settled file new, paired %d of %d", paired[before], all)
	}
}

// A started magpie takes the heatmap's settled days from the last run's
// pairing of every chunk, and pairs only the last days' chunks (#2278):
// the whole history's pairing was the cold start's peak. A settled file
// changed since, or another filter, pairs every chunk again.
func TestHeatmapSettledDaysFromDisk(t *testing.T) {
	pairAtOnce(t)
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	write := func(id string, at time.Time, input int) string {
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, input, 10)
		return path
	}
	var settled string
	for i, days := range []int{40, 20, 10, 5, 0} {
		id := string(rune('a' + i))
		at := now.AddDate(0, 0, -days).Add(-time.Hour)
		path := write(id, at, 100+i)
		if days == 20 {
			settled = path
		}
		Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	paired := []int{}
	heatPairing = func(n int) { paired = append(paired, n) }
	t.Cleanup(func() { heatPairing = nil })
	restart := func() {
		requestCache.Lock()
		requestCache.heatParts, requestCache.heatOld, requestCache.heats, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.Unlock()
	}
	first := HeatmapOf(Filter{})
	all := paired[0]
	if _, err := os.Stat(heatDiskPath()); err != nil {
		t.Fatal(err)
	}
	restart()
	if again := HeatmapOf(Filter{}); paired[len(paired)-1] >= all || !reflect.DeepEqual(again, first) {
		t.Fatalf("started again, paired %d of %d: %+v, was %+v", paired[len(paired)-1], all, again, first)
	}
	restart()
	if HeatmapOf(Filter{Model: "m"}); paired[len(paired)-1] < all {
		t.Fatalf("another filter paired %d of %d", paired[len(paired)-1], all)
	}
	writeCodexCall(t, settled, "b", "sb", `"vscode"`, "resp-b", "m", "low", now.AddDate(0, 0, -20).Add(-time.Hour), 1001, 10)
	restart()
	changed := HeatmapOf(Filter{})
	if paired[len(paired)-1] < all {
		t.Fatalf("a settled file changed, paired %d of %d", paired[len(paired)-1], all)
	}
	restart()
	os.Remove(heatDiskPath())
	if cold := HeatmapOf(Filter{}); !reflect.DeepEqual(cold, changed) {
		t.Fatalf("after a settled file changed %+v, cold %+v", changed, cold)
	}
}

// The settled days' key names a filter's zone by its name: written with
// %#v it named the zone's pointer, another in each run, and every start
// paired the whole history again for the heatmap.
func TestHeatSettledKeyNamesTheZone(t *testing.T) {
	cut := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	d := heatDiskRef{root: "r", meta: "m"}
	key := func(f Filter) string {
		k, ok := heatSettledKey(d, cut, time.Time{}, f, nil, [3][]*rowChunk{}, heatSum{})
		if !ok {
			t.Fatal("not named")
		}
		return k
	}
	a, b := key(Filter{Zone: time.FixedZone("Z", 3600)}), key(Filter{Zone: time.FixedZone("Z", 3600)})
	if a != b {
		t.Fatalf("one zone, two keys: %s, %s", a, b)
	}
	if key(Filter{}) == a || key(Filter{Zone: time.FixedZone("Y", 7200)}) == a {
		t.Fatal("another zone, the same key")
	}
}

// A probe's chunk (MCP and search calls) is made again for each query and
// has no serial. When it is among the settled days the count is still kept
// on disk, named by its rows, or every refresh counts the days again.
func TestHeatSettledKeyNamesAProbeChunk(t *testing.T) {
	cut := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	d := heatDiskRef{root: "r", meta: "m"}
	probe := func(input int) *rowChunk {
		c := &rowChunk{}
		for i := 1; i <= 3; i++ {
			c.add(Row{Record: Record{Time: cut.Add(-time.Duration(i) * time.Hour), Agent: "claude", Model: "m", Input: i * input}}, fmt.Sprint("msg", i), int64(i), false)
		}
		return c
	}
	key := func(c *rowChunk) string {
		if c.serial != 0 {
			t.Fatalf("serial %d, want a probe's 0", c.serial)
		}
		k, ok := heatSettledKey(d, cut, time.Time{}, Filter{}, nil, [3][]*rowChunk{{c}}, heatSum{})
		if !ok {
			t.Fatal("a probe's chunk left the settled days unnamed")
		}
		return k
	}
	a, b := key(probe(10)), key(probe(10))
	if a != b {
		t.Fatalf("the same rows, two keys: %s, %s", a, b)
	}
	if key(probe(11)) == a {
		t.Fatal("other rows, the same key")
	}
}
