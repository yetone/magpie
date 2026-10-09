package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// historyLog writes n calls of 90 days ago, a second apart, and one at
// Clock's time.
func historyLog(t testing.TB, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	now := Clock()
	old := now.AddDate(0, 0, -90)
	for i := 0; i < n; i++ {
		r := Record{Time: old.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "m", Session: "s", Input: 10, Status: 200}
		if i == n-1 {
			r.Time = now
		}
		if err := enc.Encode(r); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Cache performance tests start after the filesystem's change stamp has settled.
func settleLogClock(t testing.TB) {
	t.Helper()
	old := logIndexNow
	logIndexNow = func() time.Time { return time.Now().Add(2 * logStampSettle) }
	t.Cleanup(func() { logIndexNow = old })
}

func TestUsageOverviewDoesNotReparseHistory(t *testing.T) {
	var allocations [2]float64
	for i, n := range []int{10, 4000} {
		pageHome(t)
		holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
		historyLog(t, n)
		if s := Summarize(Today); s.Calls != 1 {
			t.Fatal(s.Calls)
		}
		allocations[i] = testing.AllocsPerRun(2, func() {
			Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1, Status: 200})
			Summarize(Today)
			time.Sleep(logStampSettle + 20*time.Millisecond)
			Summarize(Today)
		})
	}
	// Measure what history adds, excluding the append and two queries' fixed cost.
	t.Logf("append + unsettled + settled summary: %.0f allocations over 4000 rows, %.0f over 10", allocations[1], allocations[0])
	if grown := allocations[1] - allocations[0]; grown > 1500 {
		t.Fatalf("append then settle reparsed history: %.0f extra allocations", grown)
	}
}

func TestUsageViasSkipsOldHistory(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	historyLog(t, 4000)
	since := time.Now().Add(-time.Hour)
	if v := Vias(since); len(v) != 1 || v["codex|s"][0].Calls != 1 {
		t.Fatal(v)
	}
	if n := testing.AllocsPerRun(2, func() { Vias(since) }); n > 1500 {
		t.Fatalf("recent vias reparsed history: %.0f allocations", n)
	}
}

func TestUsageOverviewRefreshesAfterAppendAndReplacement(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 2, Status: 200})
	if s := Summarize(Today); s.Input != 2 {
		t.Fatal(s)
	}
	Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 3, Status: 200})
	if s := Summarize(Today); s.Input != 5 {
		t.Fatal(s)
	}
	f := filepath.Join(filepath.Dir(Path()), "replacement.jsonl")
	b, _ := json.Marshal(Record{Time: now, Agent: "codex", Provider: "relay", Input: 7, Status: 200})
	if err := os.WriteFile(f, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f, Path()); err != nil {
		t.Fatal(err)
	}
	if s := Summarize(Today); s.Input != 7 || s.Calls != 1 {
		t.Fatal(s)
	}
}

func TestLogAppendReusesSealedBlocks(t *testing.T) {
	pageHome(t)
	historyLog(t, logBlockRows*4+10)
	old := readLogSnapshot()
	before := old.blocks[0].Archive
	Append(Record{Agent: "codex", Input: 3, Status: 200, Operation: "turn"})
	next := readLogSnapshot()
	for i := 0; i < len(old.blocks)-1; i++ {
		if old.blocks[i] != next.blocks[i] {
			t.Fatalf("sealed block %d copied", i)
		}
	}
	if old.blocks[len(old.blocks)-1] == next.blocks[len(next.blocks)-1] {
		t.Fatal("published tail mutated")
	}
	if string(before) != string(old.blocks[0].Archive) {
		t.Fatal("sealed data changed")
	}
	if next.blocks[len(next.blocks)-1].Count != 11 {
		t.Fatal(next.blocks[len(next.blocks)-1].Count)
	}
	var last Record
	next.visit(time.Now().Add(-time.Hour), func(r Record) { last = r })
	if last.Operation != "turn" {
		t.Fatal(last)
	}
}

func TestLogAppendThenSettleReusesHistory(t *testing.T) {
	pageHome(t)
	historyLog(t, 4000)
	before := readLogSnapshot()
	Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1, Status: 200})
	appended := readLogSnapshot()
	if logChangeStamp(appended.info) == "" {
		t.Skip("filesystem does not expose change time")
	}
	if appended.settled || appended.hash == "" {
		t.Fatal("append must leave a recent snapshot with a fingerprint")
	}
	if appended.blocks[0] != before.blocks[0] {
		t.Fatal("append rebuilt the sealed history")
	}
	time.Sleep(logStampSettle + 20*time.Millisecond)
	settled := readLogSnapshot()
	if !settled.settled || settled.version != appended.version || settled.blocks[0] != appended.blocks[0] {
		t.Fatalf("unchanged settled read rebuilt history: version %d -> %d", appended.version, settled.version)
	}
	if appended.settled {
		t.Fatal("promotion mutated the published snapshot")
	}
}

func TestLogPartialLineAndTruncation(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	Append(Record{Agent: "codex", Input: 2, Status: 200})
	Summarize(Today)
	b, _ := json.Marshal(Record{Time: now, Agent: "codex", Input: 7, Status: 200})
	f, e := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Write(b)
	f.Close()
	if s := Summarize(Today); s.Input != 2 {
		t.Fatal(s)
	}
	f, e = os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte{'\n'})
	f.Close()
	if s := Summarize(Today); s.Input != 9 {
		t.Fatal(s)
	}
	if e := os.WriteFile(Path(), append(b, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	if s := Summarize(Today); s.Input != 7 || s.Calls != 1 {
		t.Fatal(s)
	}
}

func TestCachedSummaryRemainsCallerOwned(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	Append(Record{Agent: "codex", Input: 3, Status: 200})
	s := Summarize(Today)
	s.Series[0].Calls = 999
	s.Agents[0].Calls = 999
	next := Summarize(Today)
	if next.Series[0].Calls == 999 || next.Agents[0].Calls == 999 {
		t.Fatal("caller changed cache")
	}
}

func BenchmarkIncrementalUsage(b *testing.B) {
	settleLogClock(b)
	// benchmark homes are isolated just as pageHome isolates test homes
	testenv.SetHome(b, b.TempDir())
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_CACHE_HOME", b.TempDir())
	historyLog(b, 100000)
	Summarize(Today)
	b.Run("today", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Summarize(Today)
		}
	})
	b.Run("recent-vias", func(b *testing.B) {
		since := time.Now().Add(-time.Hour)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Vias(since)
		}
	})
	b.Run("append-index", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Append(Record{Agent: "codex", Input: 1, Status: 200})
			readLogSnapshot()
		}
	})
}

func TestAppendDoesNotWaitForIndexBuild(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	logIndex.Lock()
	done := make(chan struct{})
	go func() { Append(Record{Agent: "codex", Input: 1, Status: 200}); close(done) }()
	blocked := false
	select {
	case <-done:
	case <-time.After(time.Second):
		blocked = true
	}
	logIndex.Unlock()
	<-done
	if blocked {
		t.Fatal("accounting waited for a historical index build")
	}
	if Summarize(Today).Calls != 1 {
		t.Fatal("concurrent append lost")
	}
}
