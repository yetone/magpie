package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

func statsOutput(st Stats) (n int, cost float64) {
	for _, s := range st.Sessions {
		n += s.Tokens.Output
		cost += s.Cost
	}
	return
}

// TestStatsKeptBetweenPolls: a poll over files that haven't changed gives
// the ranges the last one read, not each put together again (#2278), and a
// file that changed, or a price, is read into them.
func TestStatsKeptBetweenPolls(t *testing.T) {
	claude, codex := setup(t)
	ranges := []int{0, 7, 30}
	first, native := StatsAndNativeAll(ranges)
	if len(first[0].Sessions) == 0 || len(native) == 0 {
		t.Fatalf("no stats: %+v %v", first[0], native)
	}
	again, _ := StatsAndNativeAll(ranges)
	if &again[0].Sessions[0] != &first[0].Sessions[0] {
		t.Fatal("the unchanged files' stats were put together again")
	}
	if !reflect.DeepEqual(again, first) {
		t.Fatalf("kept %+v\nread %+v", again, first)
	}

	out, _ := statsOutput(first[0])
	f, err := os.OpenFile(filepath.Join(claude, "projects", "-work-app", "11111111-2222-3333-4444-555555555555.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"parentUuid":"a1","isSidechain":false,"message":{"model":"claude-opus-5-5","id":"msg_kept","type":"message","role":"assistant","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":1,"output_tokens":7000}},"requestId":"req_kept","type":"assistant","uuid":"a-kept","timestamp":"2026-09-20T10:30:00.000Z","cwd":"/work/app","sessionId":"11111111-2222-3333-4444-555555555555"}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	grown, _ := StatsAndNativeAll(ranges)
	if n, _ := statsOutput(grown[0]); n != out+7000 {
		t.Fatalf("output %d after the file grew, %d before", n, out)
	}

	_, cost := statsOutput(grown[0])
	PriceOf = func(_ settings.Settings, m string) (catalog.Price, bool) {
		return catalog.Price{Input: 40, Output: 200, CacheRead: 2, CacheWrite: 50}, true
	}
	repriced, _ := StatsAndNativeAll(ranges)
	if _, c := statsOutput(repriced[0]); c <= cost {
		t.Fatalf("cost %v after the prices went up, %v before", c, cost)
	}

	// a thread with no name when the ranges were read, named in Codex since
	const cx = "01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd"
	line := fmt.Sprintf("{\"id\":%q,\"thread_name\":\"Named in Codex\",\"updated_at\":%q}\n", cx, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(codex, "session_index.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	named, _ := StatsAndNativeAll(ranges)
	i := slices.IndexFunc(named[0].Sessions, func(s Summary) bool { return s.ID == cx })
	if i < 0 || named[0].Sessions[i].Title != "Named in Codex" {
		t.Fatalf("Codex's thread after it was named: %d %+v", i, named[0].Sessions)
	}
}

// TestTitleAnswersSettled: the ids a title was asked for and none given are
// held once each, at their length, and asked again.
func TestTitleAnswersSettled(t *testing.T) {
	var a titleAnswers
	for _, id := range []string{"c", "a", "b", "a", "c", "c"} {
		a.note(id, "")
	}
	a.note("n", "Named")
	a.settle()
	if !slices.Equal(a.none, []string{"a", "b", "c"}) || cap(a.none) != len(a.none) {
		t.Fatalf("none %v, cap %d", a.none, cap(a.none))
	}
	if !a.still(func(id string) string { return map[string]string{"n": "Named"}[id] }) {
		t.Fatal("unchanged answers read as changed")
	}
	if a.still(func(id string) string { return map[string]string{"n": "Named", "b": "B"}[id] }) {
		t.Fatal("an id named since read as unchanged")
	}
	if a.still(func(string) string { return "" }) {
		t.Fatal("a name taken away read as unchanged")
	}
}

// TestStatsKeptNewDay: the day turning over reads the ranges again, as
// "today" and the ranges' first days moved.
func TestStatsKeptNewDay(t *testing.T) {
	setup(t)
	now := time.Now()
	a := statsReadAll([]int{7}, now, nil, nil, "")[0]
	b := statsReadAll([]int{7}, now.AddDate(0, 0, 1), nil, nil, "")[0]
	if a.To == b.To {
		t.Fatalf("the next day's range is today's: %s", b.To)
	}
}

// TestStatsKeptRangesBounded: a days= of every number is not kept.
func TestStatsKeptRangesBounded(t *testing.T) {
	setup(t)
	now := time.Now()
	for d := 1; d <= 40; d++ {
		statsReadAll([]int{0, d}, now, nil, nil, "")
	}
	mu.Lock()
	n, all := len(keptStats.ranges), keptStats.ranges[0].To != ""
	mu.Unlock()
	if n > keptRanges || !all {
		t.Fatalf("%d ranges kept, every day's among them: %v", n, all)
	}
}

// TestMergeExternalStatsWritesNoInput: the gateway's sessions are merged
// into a copy, as the Stats merged into are kept for the next poll.
func TestMergeExternalStatsWritesNoInput(t *testing.T) {
	usage := make([]Usage, 1, 4)
	usage[0] = Usage{Agent: "claude", Model: "m", Tokens: Tokens{Output: 1}}
	sums := make([]Summary, 1, 4)
	sums[0] = Summary{Key: "claude:a", Agent: "claude", ID: "a", Last: time.Unix(10, 0)}
	st := Stats{From: "2026-10-01", To: "2026-10-02", Days: []Day{{Date: "2026-10-02", Usage: usage}}, Sessions: sums}
	keep := Stats{From: st.From, To: st.To, Days: []Day{{Date: "2026-10-02", Usage: slices1(usage)}}, Sessions: slices1(sums)}
	merged := MergeExternalStats(st, []ExternalDayUsage{{Date: "2026-10-02", Agent: "claude", Model: "m", Tokens: Tokens{Output: 5}}, {Date: "2026-10-02", Agent: "opencode", Model: "x"}},
		[]ExternalSession{{Agent: "opencode", ID: "g", Last: time.Unix(20, 0)}})
	if all := merged.AllSessions(); len(all) != 2 || all[0].ID != "g" || merged.Days[0].Usage[0].Tokens.Output != 6 || len(merged.Days[0].Usage) != 2 {
		t.Fatalf("merged %+v", merged)
	}
	if !reflect.DeepEqual(st.Days[0].Usage, keep.Days[0].Usage) || !reflect.DeepEqual(st.Sessions, keep.Sessions) ||
		!reflect.DeepEqual(usage[:2], []Usage{keep.Days[0].Usage[0], {}}) || !reflect.DeepEqual(sums[:2], []Summary{keep.Sessions[0], {}}) {
		t.Fatalf("the merge wrote what it was given: %+v %+v", usage[:2], sums[:2])
	}
}

func slices1[T any](s []T) []T { return append([]T(nil), s...) }
