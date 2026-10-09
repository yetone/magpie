package usage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/DataDog/sketches-go/ddsketch"
)

func writePerfJSONLLog(t testing.TB, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	now := time.Now()
	// Spread records across the last 20 days so today, week, month, and all periods have data
	for i := range n {
		secOffset := time.Duration(n-i) * (20 * 24 * time.Hour) / time.Duration(n)
		at := now.Add(-secOffset)
		r := Record{
			Time:       at,
			Agent:      []string{"codex", "claude", "gemini"}[i%3],
			Provider:   []string{"relay", "openai", "anthropic"}[i%3],
			Model:      []string{"gpt-5", "claude-sonnet-5", "gemini-2.5-pro"}[i%3],
			Input:      1000 + (i % 500),
			Output:     200 + (i % 300),
			CacheRead:  500 + (i % 200),
			CacheWrite: 100,
			Millis:     int64(1500 + (i % 1000)),
			TTFT:       int64(200 + (i % 400)),
			Status:     200,
			RouteID:    int64(1000 + i),
		}
		if i%20 == 0 {
			r.Status = 429
		} else if i%50 == 0 {
			r.Status = 500
		} else if i%100 == 0 {
			r.Status = 499
		}
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
}

// 1. TestAnalyticsIncrementalVsRebuild verifies that delta append produces identical results to a fresh rebuild.
func TestAnalyticsIncrementalVsRebuild(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	writePerfJSONLLog(t, 200)
	initial := Analyze(Month, AnalyticsFilter{})

	// Append 10 more records
	now := time.Now()
	for i := range 10 {
		Append(Record{
			Time:     now.Add(time.Duration(i) * time.Second),
			Agent:    "codex",
			Provider: "relay",
			Model:    "gpt-5",
			Input:    1500,
			Output:   300,
			Millis:   2000,
			TTFT:     400,
			Status:   200,
			RouteID:  int64(5000 + i),
		})
	}

	incremental := Analyze(Month, AnalyticsFilter{})

	// Reset cache and do a fresh rebuild
	resetAnalyticsCache()
	rebuilt := Analyze(Month, AnalyticsFilter{})

	if incremental.Summary.Calls != rebuilt.Summary.Calls {
		t.Fatalf("calls mismatch: incremental=%d, rebuilt=%d", incremental.Summary.Calls, rebuilt.Summary.Calls)
	}
	if incremental.Summary.Input != rebuilt.Summary.Input || incremental.Summary.Output != rebuilt.Summary.Output {
		t.Fatalf("tokens mismatch: incremental in/out=%d/%d, rebuilt in/out=%d/%d",
			incremental.Summary.Input, incremental.Summary.Output, rebuilt.Summary.Input, rebuilt.Summary.Output)
	}
	if math.Abs(incremental.Summary.Cost-rebuilt.Summary.Cost) > 1e-6 {
		t.Fatalf("cost mismatch: incremental=%v, rebuilt=%v", incremental.Summary.Cost, rebuilt.Summary.Cost)
	}
	if incremental.Summary.Calls <= initial.Summary.Calls {
		t.Fatalf("calls did not increase: initial=%d, incremental=%d", initial.Summary.Calls, incremental.Summary.Calls)
	}
	if !reflect.DeepEqual(incremental, rebuilt) {
		t.Fatalf("incremental response differs from rebuild:\n%+v\n%+v", incremental, rebuilt)
	}
}

// 2. TestAnalyticsNoDataNoOldTraversal: verifies that a query when there is no new data returns cached response with minimal allocation.
func TestAnalyticsNoDataNoOldTraversal(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	writePerfJSONLLog(t, 4000)
	first := Analyze(Month, AnalyticsFilter{})

	// Measure allocations on exact cache hit (only cloneResponse + dependencySignature)
	allocs := testing.AllocsPerRun(10, func() {
		_ = Analyze(Month, AnalyticsFilter{})
	})
	// cloneResponse deep clones 5 ranking slices x 3 dimensions (~15 slices) + TrendPoints
	// It should be bounded (~700-900 allocs) and NOT scale with ledger size (e.g. 100 or 200,000)
	if allocs > 2000 {
		t.Fatalf("too many allocations on cached hit: %v allocs/op", allocs)
	}

	second := Analyze(Month, AnalyticsFilter{})
	if first.Summary.Calls != second.Summary.Calls {
		t.Fatalf("repeated call calls mismatch: %d vs %d", first.Summary.Calls, second.Summary.Calls)
	}
}

// 3. TestAnalyticsExactSumsAndRatesVsContracts verifies rates and totals against contracts.
func TestAnalyticsExactSumsAndRatesVsContracts(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	// 10 calls: 7 success, 1 429, 1 500, 1 499 (canceled)
	// total = 10, errors = 3 (429, 500, 499 all >= 400), rateLimited = 1, serverErr = 1, canceled = 1
	// errorRate numerator = 429 + 500 + otherErr = 2 (excludes 499) -> 2 / 10 = 0.20
	// successRate = (10 - 3) / 10 = 0.70
	for i := range 7 {
		Append(Record{Time: now.Add(-time.Duration(10-i) * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 200, Input: 100, Output: 50, TTFT: 100, Millis: 500})
	}
	Append(Record{Time: now.Add(-3 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 429, Input: 100})
	Append(Record{Time: now.Add(-2 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 500, Input: 100})
	Append(Record{Time: now.Add(-1 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 499, Input: 100})

	res := Analyze(All, AnalyticsFilter{})
	if res.Summary.Calls != 10 {
		t.Fatalf("calls = %d, want 10", res.Summary.Calls)
	}
	if res.Summary.SuccessRate == nil || math.Abs(*res.Summary.SuccessRate-0.70) > 1e-6 {
		t.Fatalf("success rate = %v, want 0.70", res.Summary.SuccessRate)
	}
	if res.Summary.ErrorRate == nil || math.Abs(*res.Summary.ErrorRate-0.20) > 1e-6 {
		t.Fatalf("error rate = %v, want 0.20", res.Summary.ErrorRate)
	}
	if res.Summary.Canceled != 1 {
		t.Fatalf("canceled = %d, want 1", res.Summary.Canceled)
	}
	if res.Summary.RateLimited != 1 || res.Summary.ServerErr != 1 {
		t.Fatalf("status counts: 429=%d, 500=%d", res.Summary.RateLimited, res.Summary.ServerErr)
	}
}

// 4. TestAnalyticsSketchNearestRankExtremesAndMerge verifies 1, 2, 10 samples, duplicate values, MaxInt64, and MergeWith.
func TestAnalyticsSketchNearestRankExtremesAndMerge(t *testing.T) {
	for _, values := range [][]int64{
		{500}, {100, 200}, {1, 1, 1, 1, 1, 1, 1, 1, 1, math.MaxInt64},
		{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000},
		{math.MaxInt64},
	} {
		direct, _ := ddsketch.LogCollapsingLowestDenseDDSketch(0.01, 4096)
		left, _ := ddsketch.LogCollapsingLowestDenseDDSketch(0.01, 4096)
		right, _ := ddsketch.LogCollapsingLowestDenseDDSketch(0.01, 4096)
		for i, value := range values {
			if err := direct.Add(float64(value)); err != nil {
				t.Fatal(err)
			}
			part := left
			if i >= len(values)/2 {
				part = right
			}
			if err := part.Add(float64(value)); err != nil {
				t.Fatal(err)
			}
		}
		if err := left.MergeWith(right); err != nil {
			t.Fatal(err)
		}
		for _, q := range []float64{0.50, 0.95} {
			want := values[int(math.Ceil(q*float64(len(values))))-1]
			for _, sketch := range []*ddsketch.DDSketch{direct, left} {
				got := sketchNearestRank(sketch, q)
				if got <= 0 || math.Abs(float64(got)-float64(want)) > 0.01*float64(want)+0.5 {
					t.Fatalf("samples=%v q=%v estimate=%d exact=%d", values, q, got, want)
				}
			}
			if sketchNearestRank(direct, q) != sketchNearestRank(left, q) {
				t.Fatalf("merged sketch differs from direct samples=%v q=%v", values, q)
			}
		}
	}
}

// 5. TestRecentCallsTop50AllChartOrdersAndTies verifies Top 50 extraction for all 5 chart orders and tie breaking.
func TestRecentCallsTop50AllChartOrdersAndTies(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	// Create 80 calls with ties
	for i := range 80 {
		r := Record{
			Time:     now.Add(time.Duration(i) * time.Second),
			Agent:    "agent",
			Provider: "relay",
			Model:    "m",
			Input:    100 + (i%10)*10,
			Output:   100,
			Millis:   1000 + int64(i%5)*100,
			TTFT:     100 + int64(i%5)*50,
			Status:   200,
			RouteID:  int64(i + 1),
		}
		if i%3 == 0 {
			r.Status = 500
		}
		Append(r)
	}

	chartIDs := []string{"error_rate", "ttft", "speed", "cost", "cache_hit_rate"}
	for _, cid := range chartIDs {
		calls, err := RecentCalls(Month, AnalyticsFilter{}, cid, 50)
		if err != nil {
			t.Fatalf("RecentCalls %s failed: %v", cid, err)
		}
		if len(calls) > 50 {
			t.Fatalf("RecentCalls %s exceeded limit 50: got %d", cid, len(calls))
		}
		if len(calls) == 0 {
			t.Fatalf("RecentCalls %s returned empty", cid)
		}

		// Verify order monotonicity according to chart criteria
		for j := 1; j < len(calls); j++ {
			prev, cur := calls[j-1], calls[j]
			switch cid {
			case "error_rate":
				if cur.Time.After(prev.Time) {
					t.Fatalf("error_rate not time DESC at %d: prev=%v, cur=%v", j, prev.Time, cur.Time)
				}
			case "ttft":
				if cur.TTFT > prev.TTFT {
					t.Fatalf("ttft not TTFT DESC at %d: prev=%d, cur=%d", j, prev.TTFT, cur.TTFT)
				}
			case "speed":
				if cur.Speed == nil || prev.Speed == nil {
					continue
				}
				if *cur.Speed < *prev.Speed {
					t.Fatalf("speed not Speed ASC at %d: prev=%v, cur=%v", j, *prev.Speed, *cur.Speed)
				}
			case "cache_hit_rate":
				if cur.Input > prev.Input {
					t.Fatalf("cache_hit_rate not Input DESC at %d: prev=%d, cur=%d", j, prev.Input, cur.Input)
				}
			}
		}
	}
}

// 6. TestAnalyticsReturnedOwnership: caller modifying returned structs must not affect cache.
func TestAnalyticsReturnedOwnership(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	Append(Record{Time: now, Agent: "a", Provider: "p", Model: "m", Input: 100, Output: 50, Status: 200})

	first := Analyze(All, AnalyticsFilter{})
	// Mutate returned fields
	if first.Summary.SuccessRate != nil {
		*first.Summary.SuccessRate = 0.12345
	}
	if len(first.Filters.Model) > 0 {
		first.Filters.Model[0] = "mutated-model"
	}
	if len(first.ErrorTrend) > 0 {
		first.ErrorTrend[0].ServerErr = 9999
	}
	if len(first.Rankings["model"].ByCost) > 0 {
		first.Rankings["model"].ByCost[0].Key = "mutated-rank"
	}

	second := Analyze(All, AnalyticsFilter{})
	if second.Summary.SuccessRate != nil && *second.Summary.SuccessRate == 0.12345 {
		t.Fatal("cache modified via returned SuccessRate pointer!")
	}
	if len(second.Filters.Model) > 0 && second.Filters.Model[0] == "mutated-model" {
		t.Fatal("cache modified via returned Filters slice!")
	}
	if len(second.ErrorTrend) > 0 && second.ErrorTrend[0].ServerErr == 9999 {
		t.Fatal("cache modified via returned ErrorTrend slice!")
	}
	if len(second.Rankings["model"].ByCost) > 0 && second.Rankings["model"].ByCost[0].Key == "mutated-rank" {
		t.Fatal("cache modified via returned Rankings slice!")
	}
}

// 7. TestAnalyticsReplacementTruncationRewrite: handles file being rewritten or truncated cleanly.
func TestAnalyticsReplacementTruncationRewrite(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	writePerfJSONLLog(t, 200)
	first := Analyze(Month, AnalyticsFilter{})
	if first.Summary.Calls != 200 {
		t.Fatalf("first calls = %d, want 200", first.Summary.Calls)
	}

	// Truncate to 10 rows
	writePerfJSONLLog(t, 10)
	second := Analyze(Month, AnalyticsFilter{})
	if second.Summary.Calls != 10 {
		t.Fatalf("truncated calls = %d, want 10", second.Summary.Calls)
	}
}

// 8. TestAnalyticsTimestampDisorderAndPartialLine: handles disordered timestamps and trailing partial lines.
func TestAnalyticsTimestampDisorderAndPartialLine(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	// Out-of-order records
	Append(Record{Time: now.Add(-5 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 200, Input: 10})
	Append(Record{Time: now.Add(-10 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 200, Input: 20})
	Append(Record{Time: now.Add(-1 * time.Minute), Agent: "a", Provider: "p", Model: "m", Status: 200, Input: 30})

	// Add partial uncompleted line to file
	f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"t":"` + now.Format(time.RFC3339Nano) + `","agent":"partial`)
	f.Close()

	res := Analyze(All, AnalyticsFilter{})
	if res.Summary.Calls != 3 {
		t.Fatalf("calls = %d, want 3 (partial line ignored)", res.Summary.Calls)
	}

	// Complete the line
	f, err = os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`","provider":"p","model":"m","in":40,"status":200}` + "\n")
	f.Close()

	res2 := Analyze(All, AnalyticsFilter{})
	if res2.Summary.Calls != 4 {
		t.Fatalf("calls after completing line = %d, want 4", res2.Summary.Calls)
	}
}

// 9. TestAnalyticsConcurrencyAndBudget: concurrent queries under budget restrictions do not crash or race.
func TestAnalyticsConcurrencyAndBudget(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	writePerfJSONLLog(t, 100)
	cacheBudget(t, 1<<20)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			filter := AnalyticsFilter{}
			want := 100
			if id%2 == 0 {
				filter.Model = "gpt-5"
				want = 34
			}
			if got := Analyze(All, filter).Summary.Calls; got != want {
				t.Errorf("concurrent filter=%+v calls=%d want=%d", filter, got, want)
			}
			if _, err := RecentCalls(All, filter, "error_rate", 20); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	analyticsCache.Lock()
	defer analyticsCache.Unlock()
	var retained int64
	for _, state := range analyticsCache.states {
		retained += state.approxBytes
	}
	for _, calls := range analyticsCache.calls {
		retained += calls.approxBytes
	}
	if retained > analyticsBudget() {
		t.Fatalf("analytics retained %d beyond budget %d", retained, analyticsBudget())
	}
}

// 10. Benchmark200kSuite: Cold / Hit / Append / Filter / RecentCalls on 200k records.
func Benchmark200kSuite(b *testing.B) {
	pageHome(b)
	resetAnalyticsCache()

	// This fixture compresses to about 4.58 MiB and fits both budgets.
	// Cold resets analytics state; it reuses the ledger index after its first build.
	// Oversized ledger fallback is covered separately by the boundary regression.
	n := 200_000
	writePerfJSONLLog(b, n)

	b.Run("default-24MiB-cold", func(b *testing.B) {
		resetAnalyticsCache()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			resetAnalyticsCache()
			data := Analyze(Month, AnalyticsFilter{})
			if data.Summary.Calls == 0 {
				b.Fatal("calls is 0")
			}
		}
	})

	// Repeat with an explicit larger retention budget.
	cacheBudget(b, 128<<20)

	b.Run("cached-128MiB-cold", func(b *testing.B) {
		resetAnalyticsCache()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			resetAnalyticsCache()
			data := Analyze(Month, AnalyticsFilter{})
			if data.Summary.Calls == 0 {
				b.Fatal("calls is 0")
			}
		}
	})

	b.Run("cached-128MiB-hit", func(b *testing.B) {
		// Prime the cache
		_ = Analyze(Month, AnalyticsFilter{})
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			data := Analyze(Month, AnalyticsFilter{})
			if data.Summary.Calls == 0 {
				b.Fatal("calls is 0")
			}
		}
	})

	b.Run("cached-128MiB-append-delta", func(b *testing.B) {
		_ = Analyze(Month, AnalyticsFilter{})
		now := time.Now()
		b.ResetTimer()
		b.ReportAllocs()
		for i := range b.N {
			Append(Record{
				Time:     now.Add(time.Duration(i) * time.Second),
				Agent:    "codex",
				Provider: "relay",
				Model:    "gpt-5",
				Input:    1000,
				Output:   200,
				Status:   200,
			})
			data := Analyze(Month, AnalyticsFilter{})
			if data.Summary.Calls == 0 {
				b.Fatal("calls is 0")
			}
		}
	})

	b.Run("cached-128MiB-composite-filter", func(b *testing.B) {
		filter := AnalyticsFilter{Model: "gpt-5", Provider: "relay"}
		_ = Analyze(Month, filter)
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			data := Analyze(Month, filter)
			if data.Summary.Calls == 0 {
				b.Fatal("calls is 0")
			}
		}
	})

	b.Run("cached-128MiB-recent-calls-top50", func(b *testing.B) {
		_ = Analyze(Month, AnalyticsFilter{})
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			calls, err := RecentCalls(Month, AnalyticsFilter{}, "ttft", 50)
			if err != nil || len(calls) == 0 {
				b.Fatalf("RecentCalls failed: %v", err)
			}
		}
	})
}
