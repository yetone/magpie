package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
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

func TestUsageSameTickRewriteIsNotCached(t *testing.T) {
	for _, overBudget := range []bool{false, true} {
		for _, appendAfter := range []bool{false, true} {
			for _, afterSettle := range []bool{false, true} {
				t.Run(fmt.Sprintf("uncached=%t/append=%t/after-settle=%t", overBudget, appendAfter, afterSettle), func(t *testing.T) {
					pageHome(t)
					if overBudget {
						cacheBudget(t, 128)
					}
					clock := holdClock(t, time.Now())
					tick := time.Now().Truncate(time.Second)
					holdLogTick(t, tick)
					historyLog(t, 1)
					Summarize(All)
					QueryPage(All, Filter{}, 0, 100)
					warm := readLogSnapshot()
					if warm.settled || readLogSnapshot() != warm {
						t.Fatal("unchanged same-tick metadata must reuse its fingerprint without becoming settled")
					}
					before, err := statLogFile(Path())
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
					after, err := statLogFile(Path())
					if err != nil || !sameLogInfo(before, after) {
						t.Fatal("fixture must keep file identity, size, mtime and change stamp", err)
					}
					if appendAfter {
						Append(Record{Time: clock, Agent: "codex", Provider: "relay", Model: "m", Input: 19})
					}
					if !afterSettle && !appendAfter {
						// Inside the tick the read before the rewrite may
						// stand, as a read a moment earlier would have had
						// it; the first read once the tick settles checks
						// it (#4368).
						Summarize(All)
						afterSettle = true
					}
					if afterSettle {
						logIndexNow = func() time.Time { return tick.Add(time.Hour) }
					}
					got, want := Summarize(All), summarize(All, clock, Load(time.Time{}))
					if got.Totals != want.Totals {
						t.Errorf("same-tick summary stale: got %+v want %+v", got.Totals, want.Totals)
					}
					equalPage(t, QueryPage(All, Filter{}, 0, 100), pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
					logIndexNow = func() time.Time { return tick.Add(time.Hour) }
					readLogSnapshot() // An oversized read publishes a metadata-only copy.
					stable := readLogSnapshot()
					for range 3 {
						if readLogSnapshot() != stable {
							t.Fatal("settled unchanged metadata was not cached")
						}
					}
				})
			}
		}
	}
}

func holdLogTick(t *testing.T, tick time.Time) {
	t.Helper()
	changeTime, now := logChangeTime, logIndexNow
	logChangeTime = func(info os.FileInfo) time.Time {
		if info == nil {
			return time.Time{}
		}
		return tick
	}
	logIndexNow = func() time.Time { return tick.Add(10 * time.Millisecond) }
	t.Cleanup(func() { logChangeTime, logIndexNow = changeTime, now })
}

// The parse is the fingerprint (#1357), so a rewrite can land before the read
// (after the stat that decided to read) or between the read and the
// after-read fingerprint check. Neither may leave the old bytes cached.
func TestUsageRewriteBeforeFingerprintIsNotCached(t *testing.T) {
	for _, rewriteAt := range []string{"before-read", "after-read"} {
		t.Run(rewriteAt, func(t *testing.T) {
			pageHome(t)
			now := holdClock(t, time.Now())
			tick := time.Now().Truncate(time.Second)
			holdLogTick(t, tick)
			logIndexNow = func() time.Time { return tick.Add(time.Hour) }
			historyLog(t, 1)
			info, err := statLogFile(Path())
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
			rewritten := false
			rewrite := func(path string) {
				if rewritten {
					return
				}
				rewritten = true
				if err := os.WriteFile(path, edited, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			hash, changeTime := logRecordHash, logChangeTime
			if rewriteAt == "before-read" {
				// The build's first look at the change time follows its stat
				// and precedes the open that reads and hashes the lines.
				logChangeTime = func(i os.FileInfo) time.Time {
					rewrite(Path())
					return changeTime(i)
				}
			} else {
				logRecordHash = func(path string, n int64) string {
					rewrite(path)
					return hash(path, n)
				}
			}
			t.Cleanup(func() { logRecordHash, logChangeTime = hash, changeTime })
			Summarize(All)
			if !rewritten {
				t.Fatal("fixture did not rewrite at the read boundary")
			}
			got, want := Summarize(All), summarize(All, now, Load(time.Time{}))
			if got.Totals != want.Totals {
				t.Fatalf("post-read fingerprint hid a rewrite: got %+v want %+v", got.Totals, want.Totals)
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

// A history whose index is larger than the cache budget is still read from
// the file once: every later summary, page, filter, heatmap, Visit and Vias
// reads the kept blocks. Over the budget the blocks used to be dropped and
// each of them parsed the whole file again, 6 s of CPU at 750k calls, which
// left the Requests page spinning on every refresh and filter (Vantiboo1ean).
func TestUsageOverBudgetHistoryIsReadOnce(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	settleLogClock(t)
	cacheBudget(t, 128)
	historyLog(t, 1034)
	reads := func() int {
		logIndex.Lock()
		defer logIndex.Unlock()
		return logIndex.reads
	}
	readAll := func() {
		t.Helper()
		cold := Load(time.Time{})
		for _, p := range []Period{Today, Week, Month, All} {
			got, want := Summarize(p), summarize(p, now, cold)
			if got.Totals != want.Totals {
				t.Fatalf("%s summary: %+v / %+v", p, got.Totals, want.Totals)
			}
			for _, f := range []Filter{{}, {Model: "m"}, {Agent: "codex"}, {Query: "relay"}} {
				equalPage(t, QueryPage(p, f, 0, 100), pageFromLedger(p, f, 0, 100, LedgerOf(p, f)))
			}
		}
		HeatmapOf(Filter{})
		Visit(now.AddDate(0, 0, -7), func(Record) {})
		Vias(now.AddDate(0, 0, -7))
	}
	readAll()
	before := reads()
	readAll()
	if n := reads() - before; n != 0 {
		t.Fatalf("an unchanged over-budget history was parsed %d more times", n)
	}
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", Input: 11})
	before = reads()
	readAll()
	if n := reads() - before; n != 1 {
		t.Fatalf("an append was read %d times, want once (from where the last read ended)", n)
	}
	historyLog(t, 20) // a rewrite is read again in full, once
	before = reads()
	readAll()
	if n := reads() - before; n != 1 {
		t.Fatalf("a rewrite was read %d times, want once", n)
	}
}

// The session files' chunks stay within the budget; the gateway's index is
// kept whatever its size and is not counted against it.
func TestUsageSessionChunksStayInBudget(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	historyLog(t, 1034)
	cacheBudget(t, 1)
	equalPage(t, QueryPage(All, Filter{}, 0, 100), pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
	requestCache.Lock()
	defer requestCache.Unlock()
	var retained int64
	for _, c := range requestCache.chunks {
		retained += c.Bytes
	}
	if retained > requestCacheBytes {
		t.Fatalf("session chunk bytes %d exceed %d", retained, requestCacheBytes)
	}
}

func TestUsageLargeSummaryIsCompleteAndKeptTrimmed(t *testing.T) {
	pageHome(t)
	for i := 0; i < summaryCacheSessions+1; i++ {
		Append(Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Session: fmt.Sprint(i), Input: 1 + i})
	}
	worked := func() int {
		summaries.Lock()
		defer summaries.Unlock()
		return summaries.worked
	}
	got := Summarize(All)
	if len(got.Sessions) != summaryCacheSessions+1 || got.Calls != summaryCacheSessions+1 || got.SessionCount != summaryCacheSessions+1 {
		t.Fatalf("large summary lost sessions: %d / %d", len(got.Sessions), got.Calls)
	}
	summaries.Lock()
	kept := summaries.entries[summaryKey{All, time.Local}]
	summaries.Unlock()
	if len(kept.value.Sessions) != summaryCacheSessions || kept.value.SessionCount != summaryCacheSessions+1 {
		t.Fatalf("the cache kept %d sessions of %d", len(kept.value.Sessions), kept.value.SessionCount)
	}
	// the Usage page and the TUI ask for the top few, and a long history's
	// summary is not worked out again for them (Vantiboo1ean, ~750k
	// requests in ~84k sessions); asked for all, it is
	before := worked()
	for _, n := range []int{0, 100, summaryCacheSessions} {
		top := SummarizeTop(All, n)
		if len(top.Sessions) != n || top.SessionCount != summaryCacheSessions+1 || top.Calls != got.Calls || n > 0 && top.Sessions[0] != got.Sessions[0] {
			t.Fatalf("top %d: %d sessions of %d", n, len(top.Sessions), top.SessionCount)
		}
	}
	if s, _ := Summaries(All); len(s.Sessions) != summaryCacheSessions || s.SessionCount != summaryCacheSessions+1 {
		t.Fatalf("Summaries: %d sessions of %d", len(s.Sessions), s.SessionCount)
	}
	if n := worked() - before; n != 0 {
		t.Fatalf("a summary of the top sessions was worked out %d more times", n)
	}
	if len(Summarize(All).Sessions) != summaryCacheSessions+1 || worked() != before+1 {
		t.Fatal("asked for every session, the summary is not worked out afresh")
	}
	// what a caller is handed is its own
	top := SummarizeTop(All, 10)
	top.Sessions[0].Calls, top.Agents[0].Calls = -1, -1
	if again := SummarizeTop(All, 10); again.Sessions[0].Calls == -1 || again.Agents[0].Calls == -1 {
		t.Fatal("a caller's change reached the cache")
	}
}

// A page of a history larger than the budget is kept, so asking it again
// (the Requests page's refresh with nothing new) costs nothing.
func TestUsageOverBudgetPageIsCached(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	historyLog(t, 1034)
	cacheBudget(t, 128)
	got := QueryPage(All, Filter{}, 0, 100)
	equalPage(t, got, pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
	requestCache.Lock()
	defer requestCache.Unlock()
	if _, ok := requestCache.pages[pageKey{All, Filter{}, 0, 100, "2026-09-30"}]; !ok {
		t.Fatal("page of an over-budget history not cached")
	}
}

// A page asked for again while it is still being worked out waits for that
// one: the Requests page's refresh, pressed while a long history's All was
// read (Vantiboo1ean, ~750k calls), read the whole history once more beside
// it, and each press added another.
func TestUsagePageAskedTwiceIsWorkedOutOnce(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	historyLog(t, 1034)
	var mu sync.Mutex
	builds := 0
	building = func() {
		mu.Lock()
		builds++
		mu.Unlock()
		time.Sleep(300 * time.Millisecond) // the others ask while this one works
	}
	t.Cleanup(func() { building = nil })
	start := make(chan struct{})
	var wg sync.WaitGroup
	pages := make([]RequestPage, 4)
	for i := range pages {
		wg.Go(func() {
			<-start
			pages[i] = QueryPage(All, Filter{}, 0, 100)
		})
	}
	close(start)
	wg.Wait()
	if builds != 1 {
		t.Fatalf("the same page was worked out %d times at once, want 1", builds)
	}
	want := pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{}))
	for _, p := range pages {
		equalPage(t, p, want)
	}
}

// The TUI, magpie usage, the CSV export and sync read a period's ledger
// from the gateway's index, not by parsing usage.jsonl again: over
// Vantiboo1ean's ~750k requests each parse took seconds. What they read is
// what Load reads, appends and an unfinished last line included.
func TestUsageLedgerReadsTheIndexNotTheFile(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	settleLogClock(t)
	historyLog(t, 1034)
	Summaries(Today) // the index is read
	Append(Record{Time: now.Add(-time.Hour), Agent: "claude-code", Provider: "anthropic", Model: "claude-x", Requested: "magpie/x", Served: "claude-x-1", Session: "s2", Subagent: "sub", Input: 5, Output: 7, CacheRead: 3, CacheWrite: 2, CacheWrite1h: 1, Reasoning: 4, Status: 500, Error: "boom", ErrType: "overloaded", ProviderAccount: "a@b", Effort: "high", TTFT: 30, Millis: 90, Archive: "2026-09-30/x", Via: "mini", CallerKeyID: "k1", Rejected: true})
	f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"t":"2026-09-30T11:59:00Z","agent":"codex"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, since := range []time.Time{{}, now.AddDate(0, 0, -7), now.Add(-2 * time.Hour), now.Add(time.Hour)} {
		got, want := gatewayRecords(since), Load(since)
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if len(got) != len(want) || string(g) != string(w) {
			t.Fatalf("since %v: the index read %d records, the file %d", since, len(got), len(want))
		}
	}
	mu.Lock()
	before := loads
	mu.Unlock()
	Summaries(All)
	Direct(Week)
	LedgerOf(Month, Filter{})
	Ledger(Today, Filter{Model: "m"})
	OwnDays(now.AddDate(0, 0, -3))
	mu.Lock()
	defer mu.Unlock()
	if n := loads - before; n != 0 {
		t.Fatalf("the ledger parsed the whole log %d times", n)
	}
}

// While the page is asked for, the session files' chunks are kept to
// requestCacheBytes, so a refresh after a new call reads none of them again;
// once it goes unasked for requestIdle they are held to requestIdleBytes
// (#3493).
func TestRequestChunksTrimmedWhenIdle(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	oldIdle, oldIdleBytes := requestIdle, requestIdleBytes
	t.Cleanup(func() {
		requestCache.Lock()
		requestIdle, requestIdleBytes = oldIdle, oldIdleBytes
		requestCache.Unlock()
	})
	cacheBudget(t, 1<<30)
	requestCache.Lock()
	requestIdle, requestIdleBytes = time.Hour, 1
	requestCache.Unlock()
	dir := filepath.Join(sessions.CodexDir(), "sessions")
	os.MkdirAll(dir, 0700)
	for i := range 3 {
		meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"s%d","model_provider":"custom"}}`, i) + "\n" + `{"type":"turn_context","payload":{"model":"m"}}` + "\n"
		line := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}}`+"\n", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("rollout-2026-09-30T00-00-0%d-s%d.jsonl", i, i)), []byte(meta+line), 0600)
	}
	chunks := func() int {
		requestCache.Lock()
		defer requestCache.Unlock()
		return len(requestCache.chunks)
	}
	if p := QueryPage(All, Filter{}, 0, 100); p.Total != 3 {
		t.Fatalf("total %d, want 3", p.Total)
	}
	if n := chunks(); n != 3 {
		t.Fatalf("%d chunks kept while the page is asked for, want 3", n)
	}
	requestCache.Lock()
	requestIdle = time.Millisecond
	requestCache.Unlock()
	QueryPage(All, Filter{}, 0, 100) // asked again: the idle wait starts over
	for deadline := time.Now().Add(10 * time.Second); chunks() != 0; {
		if time.Now().After(deadline) {
			t.Fatalf("%d chunks kept after the page went unasked, want 0", chunks())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Within requestIdleBytes the chunks stay through the trim, and go
// requestForget after it when nothing asked for them since; an ask
// after the trim keeps them.
func TestRequestChunksForgottenLongIdle(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	oldIdle, oldIdleBytes, oldForget := requestIdle, requestIdleBytes, requestForget
	t.Cleanup(func() {
		requestCache.Lock()
		requestIdle, requestIdleBytes, requestForget = oldIdle, oldIdleBytes, oldForget
		requestCache.Unlock()
	})
	cacheBudget(t, 1<<30)
	requestCache.Lock()
	requestIdle, requestIdleBytes, requestForget = time.Hour, 1<<30, time.Hour
	if requestCache.forget != nil { // armed by a test before
		requestCache.forget.Stop()
		requestCache.forget = nil
	}
	requestCache.Unlock()
	dir := filepath.Join(sessions.CodexDir(), "sessions")
	os.MkdirAll(dir, 0700)
	for i := range 3 {
		meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"s%d","model_provider":"custom"}}`, i) + "\n" + `{"type":"turn_context","payload":{"model":"m"}}` + "\n"
		line := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}}`+"\n", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("rollout-2026-09-30T00-00-0%d-s%d.jsonl", i, i)), []byte(meta+line), 0600)
	}
	chunks := func() int {
		requestCache.Lock()
		defer requestCache.Unlock()
		return len(requestCache.chunks)
	}
	trimmed := func() bool {
		requestCache.Lock()
		defer requestCache.Unlock()
		return requestCache.forget != nil
	}
	ask := func() {
		if p := QueryPage(All, Filter{}, 0, 100); p.Total != 3 {
			t.Fatalf("total %d, want 3", p.Total)
		}
	}
	wait := func(what string, done func() bool) {
		for deadline := time.Now().Add(10 * time.Second); !done(); {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d chunks kept", what, chunks())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	ask()
	requestCache.Lock()
	requestIdle = time.Millisecond
	requestCache.Unlock()
	ask() // the trim comes a millisecond on; the forget an hour after it
	wait("trimmed", trimmed)
	if n := chunks(); n != 3 {
		t.Fatalf("%d chunks kept after the trim within its bound, want 3", n)
	}

	// an ask after the trim: the forget armed before it lets nothing go
	requestCache.Lock()
	requestIdle = time.Hour
	requestCache.forget.Reset(time.Millisecond)
	requestCache.asks++ // as an ask that comes as the forget fires
	requestCache.Unlock()
	time.Sleep(50 * time.Millisecond)
	if n := chunks(); n != 3 {
		t.Fatalf("%d chunks kept after an ask since the trim, want 3", n)
	}

	requestCache.Lock()
	requestIdle, requestForget = time.Millisecond, time.Millisecond
	requestCache.Unlock()
	ask()
	wait("forgotten", func() bool { return chunks() == 0 })
	ask() // read back from the file of them
}

// A trim that lets most chunks go gives back the map's table too: a map
// never shrinks, and the forget's trim(0) of a real history's index kept
// an empty 853 KB table for the life of the process.
func TestRequestTrimGivesBackTheMap(t *testing.T) {
	const n = 100000
	idx := &requestIndex{chunks: make(map[string]*rowChunk)}
	c := &rowChunk{Bytes: 1, used: new(atomic.Uint64)}
	keys := make([]string, n) // held, so only the table is measured
	for i := range n {
		keys[i] = fmt.Sprint("/s/rollout-", i, ".jsonl")
		idx.chunks[keys[i]] = c
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	before := heap()
	idx.trim(0)
	after := heap()
	if len(idx.chunks) != 0 {
		t.Fatalf("%d chunks after trim(0)", len(idx.chunks))
	}
	// the table of 100k entries is ~3 MB
	if after+1<<20 > before {
		t.Fatalf("heap %d → %d after trim(0): the map's table was kept", before, after)
	}
	idx.chunks["/s/kept"] = c // the map is usable after
	runtime.KeepAlive(keys)
}
