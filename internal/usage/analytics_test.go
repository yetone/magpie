package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// mockPriceLookup creates a mock price lookup function for tests.
func mockPriceLookup(pricing map[string]*catalog.Price) PriceLookup {
	return func(providerID, modelID string) *catalog.Price {
		return pricing[providerID+"/"+modelID]
	}
}

func TestAnalyzeMetricsAndQuantiles(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	pricing := map[string]*catalog.Price{
		"prov1/modelA": {Input: 10, Output: 30, CacheRead: 5, CacheWrite: 15},
	}
	lookup := mockPriceLookup(pricing)

	// Construct 10 calls with varying TTFT and status to test quantiles, 499 exclusion, and speeds
	var recs []Record
	for i := range 10 {
		ttft := int64(100 * (i + 1)) // 100, 200, ..., 1000
		recs = append(recs, Record{
			Time:       now.Add(-time.Duration(10-i) * time.Minute),
			Agent:      "agent1",
			Provider:   "prov1",
			Model:      "modelA",
			Input:      1000,
			Output:     500,
			CacheRead:  200,
			CacheWrite: 100,
			Millis:     ttft + 1000, // decodeMs = 1000
			TTFT:       ttft,
			Status:     200,
		})
	}

	// Add 1 rate-limited (429), 1 server error (500), 1 other 4xx (404), 1 canceled (499)
	recs = append(recs,
		Record{Time: now.Add(-5 * time.Minute), Agent: "agent1", Provider: "prov1", Model: "modelA", Status: 429},
		Record{Time: now.Add(-4 * time.Minute), Agent: "agent1", Provider: "prov1", Model: "modelA", Status: 500},
		Record{Time: now.Add(-3 * time.Minute), Agent: "agent1", Provider: "prov1", Model: "modelA", Status: 404},
		Record{Time: now.Add(-2 * time.Minute), Agent: "agent1", Provider: "prov1", Model: "modelA", Status: 499},
	)

	// Total calls = 10 + 4 = 14
	// Errors = 4 (status >= 400 counts in Totals.Errors)
	// Success rate = (14 - 4) / 14 = 10 / 14
	// Error rate (excludes 499) = (1 + 1 + 1) / 14 = 3 / 14
	// Canceled = 1 -> cancel_rate = 1 / 14
	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)

	if data.Summary.Calls != 14 {
		t.Fatalf("summary calls = %d, want 14", data.Summary.Calls)
	}
	if data.Summary.Canceled != 1 {
		t.Fatalf("summary canceled = %d, want 1", data.Summary.Canceled)
	}
	wantCancelRate := 1.0 / 14.0
	if data.Summary.CancelRate == nil || math.Abs(*data.Summary.CancelRate-wantCancelRate) > 1e-6 {
		t.Fatalf("cancel_rate = %v, want %v", data.Summary.CancelRate, wantCancelRate)
	}
	if data.Summary.Errors != 4 {
		t.Fatalf("errors = %d, want 4", data.Summary.Errors)
	}
	if data.Summary.RateLimited != 1 || data.Summary.ServerErr != 1 || data.Summary.OtherErr != 1 || data.Summary.Canceled != 1 {
		t.Fatalf("status counts mismatch: %+v", data.Summary)
	}
	wantSucc := 10.0 / 14.0
	if data.Summary.SuccessRate == nil || math.Abs(*data.Summary.SuccessRate-wantSucc) > 1e-6 {
		t.Fatalf("success_rate = %v, want %v", data.Summary.SuccessRate, wantSucc)
	}
	wantErr := 3.0 / 14.0
	if data.Summary.ErrorRate == nil || math.Abs(*data.Summary.ErrorRate-wantErr) > 1e-6 {
		t.Fatalf("error_rate = %v, want %v", data.Summary.ErrorRate, wantErr)
	}

	// TTFT quantiles for 10 samples: [100, 200, 300, 400, 500, 600, 700, 800, 900, 1000]
	// exact p50: ceil(0.50 * 10) - 1 = 5 - 1 = 4 -> 500
	// exact p95: ceil(0.95 * 10) - 1 = 10 - 1 = 9 -> 1000
	// DDSketch tolerance: abs(estimate - exact) <= 0.01 * exact + 1 ms
	if data.Summary.TTFTP50 == nil || math.Abs(float64(*data.Summary.TTFTP50-500)) > 500*0.01+1 {
		t.Fatalf("ttft_p50 = %v, want ~500", data.Summary.TTFTP50)
	}
	if data.Summary.TTFTP95 == nil || math.Abs(float64(*data.Summary.TTFTP95-1000)) > 1000*0.01+1 {
		t.Fatalf("ttft_p95 = %v, want ~1000", data.Summary.TTFTP95)
	}
	// Speed: 10 decode calls, each output=500, decodeMs=1000 (1.0s)
	// Total DecodeOut = 5000, Total DecodeMs = 10000 -> 5000 / 10 = 500 tok/s
	if data.Summary.DecodeCalls != 10 {
		t.Fatalf("decode_calls = %d, want 10", data.Summary.DecodeCalls)
	}
	if data.Summary.Speed == nil || math.Abs(*data.Summary.Speed-500.0) > 1e-6 {
		t.Fatalf("speed = %v, want 500", data.Summary.Speed)
	}

	// Cache hit rate: input = 10000, cache_read = 2000 -> 2000 / (10000 + 2000) = 2000 / 12000 = 1/6
	// cache_write = 1000 (does not enter denom)
	wantHitRate := 2000.0 / 12000.0
	if data.Summary.CacheHitRate == nil || math.Abs(*data.Summary.CacheHitRate-wantHitRate) > 1e-6 {
		t.Fatalf("cache_hit_rate = %v, want %v", data.Summary.CacheHitRate, wantHitRate)
	}
}

func TestAnalyzeRankingsSortAndThresholds(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	pricing := map[string]*catalog.Price{
		"p/m1": {Input: 10, Output: 20},
		"p/m2": {Input: 10, Output: 20},
		"p/m3": {Input: 10, Output: 20},
	}
	lookup := mockPriceLookup(pricing)

	var recs []Record
	// m1: 10 calls, all 429 -> error_rate = 1.0 (sufficient)
	for range 10 {
		recs = append(recs, Record{
			Time: now.Add(-time.Hour), Agent: "a", Provider: "p", Model: "m1",
			Status: 429, Input: 100, Output: 100,
		})
	}

	// m2: 12 calls, 3 errors -> error_rate = 3 / 12 = 0.25 (sufficient)
	for i := range 12 {
		status := 200
		if i < 3 {
			status = 500
		}
		recs = append(recs, Record{
			Time: now.Add(-time.Hour), Agent: "a", Provider: "p", Model: "m2",
			Status: status, Input: 1000, Output: 500, TTFT: 200, Millis: 1200,
		})
	}

	// m3: 3 calls, all 500 -> error_rate = 1.0 (insufficient because calls < 10)
	for range 3 {
		recs = append(recs, Record{
			Time: now.Add(-time.Hour), Agent: "a", Provider: "p", Model: "m3",
			Status: 500, Input: 100, Output: 100,
		})
	}

	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)
	suite := data.Rankings["model"]

	// Check by_error_rate:
	// m1 (1.0, sufficient) > m2 (0.25, sufficient) > m3 (insufficient at end)
	if len(suite.ByErrorRate) != 3 {
		t.Fatalf("by_error_rate len = %d, want 3", len(suite.ByErrorRate))
	}
	if suite.ByErrorRate[0].Key != "m1" || suite.ByErrorRate[0].Insufficient {
		t.Fatalf("first item should be m1 sufficient: %+v", suite.ByErrorRate[0])
	}
	if suite.ByErrorRate[1].Key != "m2" || suite.ByErrorRate[1].Insufficient {
		t.Fatalf("second item should be m2 sufficient: %+v", suite.ByErrorRate[1])
	}
	if suite.ByErrorRate[2].Key != "m3" || !suite.ByErrorRate[2].Insufficient {
		t.Fatalf("third item should be m3 insufficient: %+v", suite.ByErrorRate[2])
	}
	// Verify shared summaries map contains all entities
	if len(suite.Summaries) != 3 {
		t.Fatalf("summaries len = %d, want 3", len(suite.Summaries))
	}
	if sumM1, ok := suite.Summaries["m1"]; !ok || sumM1.Calls != 10 {
		t.Fatalf("summaries[m1] calls = %v, want 10", sumM1.Calls)
	}
}

func TestAnalyzeCacheRateUnknownAndInsufficient(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	pricing := map[string]*catalog.Price{}
	lookup := mockPriceLookup(pricing)

	recs := []Record{
		// normalA: input=10000, cacheRead=5000 -> hit rate = 5000 / 15000 = 0.333 (sufficient)
		{Time: now.Add(-time.Hour), Provider: "p", Model: "normalA", Input: 10000, CacheRead: 5000, CacheWrite: 1000, Status: 200},
		// normalB: input=8000, cacheRead=8000 -> hit rate = 8000 / 16000 = 0.50 (sufficient)
		{Time: now.Add(-time.Hour), Provider: "p", Model: "normalB", Input: 8000, CacheRead: 8000, CacheWrite: 1000, Status: 200},
		// small: input=1000, cacheRead=500, cacheWrite=100 -> input+cacheRead=1500 < 10000 (insufficient)
		{Time: now.Add(-time.Hour), Provider: "p", Model: "small", Input: 1000, CacheRead: 500, CacheWrite: 100, Status: 200},
		// unknown: input=50000, cacheRead=0, cacheWrite=0 (unknown_cache, placed at suffix)
		{Time: now.Add(-time.Hour), Provider: "p", Model: "unknown", Input: 50000, CacheRead: 0, CacheWrite: 0, Status: 200},
	}

	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)
	byCache := data.Rankings["model"].ByCacheRate

	if len(byCache) != 4 {
		t.Fatalf("len = %d, want 4", len(byCache))
	}

	// Order:
	// 0: normalA (hit rate 0.333, ASC)
	// 1: normalB (hit rate 0.50, ASC)
	// 2: small (insufficient, tier 1)
	// 3: unknown (unknown_cache, tier 2 suffix)
	if byCache[0].Key != "normalA" || byCache[0].Insufficient || byCache[0].UnknownCache {
		t.Fatalf("0 want normalA, got %+v", byCache[0])
	}
	if byCache[1].Key != "normalB" || byCache[1].Insufficient || byCache[1].UnknownCache {
		t.Fatalf("1 want normalB, got %+v", byCache[1])
	}
	if byCache[2].Key != "small" || !byCache[2].Insufficient || byCache[2].UnknownCache {
		t.Fatalf("2 want small insufficient, got %+v", byCache[2])
	}
	if byCache[3].Key != "unknown" || !byCache[3].UnknownCache {
		t.Fatalf("3 want unknown unknown_cache, got %+v", byCache[3])
	}
}

func TestAnalyzeCompositeFiltersAndOptions(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	lookup := mockPriceLookup(map[string]*catalog.Price{})

	recs := []Record{
		{Time: now.Add(-time.Hour), Agent: "codex", Provider: "prov1", Model: "m1", Input: 100},
		{Time: now.Add(-time.Hour), Agent: "claude", Provider: "prov2", Model: "m2", Input: 200},
		{Time: now.Add(-time.Hour), Agent: "codex", Provider: "prov2", Model: "m1", Input: 300},
	}

	// Filter options before composite filter must include all available options:
	// Models: m1, m2
	// Providers: prov1, prov2
	// Agents: codex, claude
	dataAll := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)
	if len(dataAll.Filters.Model) != 2 || len(dataAll.Filters.Provider) != 2 || len(dataAll.Filters.Agent) != 2 {
		t.Fatalf("filters before composite: %+v", dataAll.Filters)
	}

	// Now apply composite filter: Provider="prov2", Model="m1"
	// Should match only record 3 (codex, prov2, m1, Input: 300)
	dataFiltered := analyzeWith(Today, AnalyticsFilter{Provider: "prov2", Model: "m1"}, now, recs, lookup)
	if dataFiltered.Summary.Calls != 1 || dataFiltered.Summary.Input != 300 {
		t.Fatalf("filtered summary: %+v", dataFiltered.Summary)
	}
	// The filter options themselves are still the full period's options
	if len(dataFiltered.Filters.Model) != 2 {
		t.Fatalf("filter options should remain complete: %+v", dataFiltered.Filters)
	}
}

func TestRecentCallsFilteringAndSorting(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	pricing := map[string]*catalog.Price{
		"p/m": {Input: 10, Output: 20}, // $10/M in, $20/M out
	}
	lookup := mockPriceLookup(pricing)

	// Create 100 records for limit test
	var recs []Record
	for i := range 100 {
		recs = append(recs, Record{
			Time:     now.Add(-time.Duration(100-i) * time.Minute),
			Agent:    "a",
			Provider: "p",
			Model:    "m",
			Input:    i + 1,
			Output:   10,
			TTFT:     int64(i + 1),
			Millis:   int64(i + 1 + 1000),
			Status:   200,
		})
	}

	// 1. Chart "ttft" (TTFT DESC): limit 50
	calls, err := recentCallsWith(Today, AnalyticsFilter{}, "ttft", 50, now, recs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 50 {
		t.Fatalf("calls len = %d, want 50", len(calls))
	}
	// Largest TTFT first: record 99 has TTFT 100, record 98 has 99, etc.
	if calls[0].TTFT != 100 || calls[49].TTFT != 51 {
		t.Fatalf("first=%d, 50th=%d", calls[0].TTFT, calls[49].TTFT)
	}
	if calls[0].Cost == nil {
		t.Fatal("cost should be calculated")
	}

	// 2. Chart "error_rate": Error calls (status >= 400 excluding 499) time DESC
	errRecs := []Record{
		{Time: now.Add(-3 * time.Minute), Provider: "p", Model: "m", Status: 500},
		{Time: now.Add(-2 * time.Minute), Provider: "p", Model: "m", Status: 499}, // Canceled: MUST BE EXCLUDED
		{Time: now.Add(-1 * time.Minute), Provider: "p", Model: "m", Status: 429},
	}
	calls11, err := recentCallsWith(Today, AnalyticsFilter{}, "error_rate", 50, now, errRecs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls11) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls11))
	}
	if calls11[0].Status != 429 || calls11[1].Status != 500 {
		t.Fatalf("expected 429 then 500: %+v", calls11)
	}

	// 3. Chart "speed": decode speed ASC (out / ((ms - ttft) / 1000))
	speedRecs := []Record{
		{Time: now.Add(-3 * time.Minute), Provider: "p", Model: "m", Status: 200, Output: 100, TTFT: 100, Millis: 1100}, // 100 / 1s = 100 tok/s
		{Time: now.Add(-2 * time.Minute), Provider: "p", Model: "m", Status: 200, Output: 20, TTFT: 100, Millis: 1100},  // 20 / 1s = 20 tok/s (slower)
		{Time: now.Add(-1 * time.Minute), Provider: "p", Model: "m", Status: 200, Output: 0, TTFT: 100, Millis: 1100},   // Output=0 invalid
	}
	calls22, err := recentCallsWith(Today, AnalyticsFilter{}, "speed", 50, now, speedRecs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls22) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls22))
	}
	if calls22[0].Output != 20 || calls22[1].Output != 100 {
		t.Fatalf("expected slower (20 tok/s) first, got: %+v", calls22)
	}

	// 4. Chart "cost": costs DESC known before unknown
	costRecs := []Record{
		{Time: now.Add(-4 * time.Minute), Provider: "unknown", Model: "m", Status: 200, Input: 1000, Output: 1000}, // unpriced
		{Time: now.Add(-3 * time.Minute), Provider: "p", Model: "m", Status: 200, Input: 100, Output: 100},         // cheaper
		{Time: now.Add(-2 * time.Minute), Provider: "p", Model: "m", Status: 200, Input: 10000, Output: 10000},     // expensive
		{Time: now.Add(-1 * time.Minute), Provider: "p", Model: "m", Status: 200, Input: 0, Output: 0},             // 0 tokens: cost null
	}
	calls31, err := recentCallsWith(Today, AnalyticsFilter{}, "cost", 50, now, costRecs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls31) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(calls31))
	}
	if calls31[0].Input != 10000 || calls31[1].Input != 100 {
		t.Fatalf("priced items should be sorted DESC: %+v", calls31)
	}
	if calls31[2].Cost != nil || calls31[3].Cost != nil {
		t.Fatalf("unpriced items should be null cost at the end: %+v", calls31)
	}

	// 5. Chart "cache_hit_rate": input DESC
	calls32, err := recentCallsWith(Today, AnalyticsFilter{}, "cache_hit_rate", 50, now, costRecs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if calls32[0].Input != 10000 || calls32[1].Input != 1000 || calls32[2].Input != 100 || calls32[3].Input != 0 {
		t.Fatalf("input DESC failed: %+v", calls32)
	}

	// 6. Invalid chart returns ErrInvalidChart
	_, err = recentCallsWith(Today, AnalyticsFilter{}, "9.9", 50, now, costRecs, lookup)
	if err != ErrInvalidChart {
		t.Fatalf("expected ErrInvalidChart, got %v", err)
	}
}

func TestProviderRenameInAnalytics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://preview.invalid/v1", Key: "synthetic-only", Was: []string{"oldrelay"}}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-time.Hour), Provider: "oldrelay", Model: "m1", Status: 200, Input: 100},
	}
	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, mockPriceLookup(nil))
	if len(data.Filters.Provider) != 1 || data.Filters.Provider[0] != "relay" {
		t.Fatalf("renamed provider expected 'relay', got %+v", data.Filters.Provider)
	}
	if len(data.Rankings["provider"].ByCost) != 1 || data.Rankings["provider"].ByCost[0].Key != "relay" {
		t.Fatalf("rankings provider key expected 'relay', got %+v", data.Rankings["provider"].ByCost)
	}
}

func TestRecentCallsCostStaysWithRecord(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	lookup := mockPriceLookup(map[string]*catalog.Price{"p/m": {Input: 10}})
	recs := []Record{
		{Time: now.Add(-time.Minute), Provider: "unknown", Model: "m", Input: 900000, Session: "unknown"},
		{Time: now.Add(-2 * time.Minute), Provider: "p", Model: "m", Input: 100000, Session: "cheap"},
		{Time: now.Add(-4 * time.Minute), Provider: "p", Model: "m", Input: 300000, Session: "expensive"},
		{Time: now.Add(-3 * time.Minute), Provider: "p", Model: "m", Input: 200000, Session: "middle"},
	}
	for _, limit := range []int{50, 2} {
		calls, err := recentCallsWith(Today, AnalyticsFilter{}, "cost", limit, now, recs, lookup)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"expensive", "middle", "cheap", "unknown"}
		if limit == 2 {
			want = want[:2]
		}
		if len(calls) != len(want) {
			t.Fatalf("limit %d: got %d calls, want %d", limit, len(calls), len(want))
		}
		for i, session := range want {
			if calls[i].Session != session {
				t.Fatalf("limit %d: row %d session = %q, want %q", limit, i, calls[i].Session, session)
			}
			if session == "unknown" {
				if calls[i].Cost != nil {
					t.Fatal("unknown pricing returned a cost")
				}
			} else if calls[i].Cost == nil || math.Abs(*calls[i].Cost-float64(calls[i].Input)*10/1e6) > 1e-9 {
				t.Fatalf("cost does not belong to %s: %+v", session, calls[i])
			}
		}
	}
}

func TestAnalyzeTrendCalendarBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	lordHowe, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		period    Period
		now, call time.Time
		wantDay   int
		wantHour  int
	}{
		{"spring week", Week, time.Date(2026, 3, 10, 12, 0, 0, 0, loc), time.Date(2026, 3, 9, 0, 15, 0, 0, loc), 9, 0},
		{"spring all", All, time.Date(2026, 3, 9, 12, 0, 0, 0, loc), time.Date(2026, 3, 9, 0, 15, 0, 0, loc), 9, 0},
		{"fall today", Today, time.Date(2026, 11, 1, 23, 45, 0, 0, loc), time.Date(2026, 11, 1, 23, 15, 0, 0, loc), 1, 23},
		// a 24.5-hour day: its last half hour is a 25th bucket, not lost
		{"half-hour fall today", Today, time.Date(2026, 4, 5, 23, 50, 0, 0, lordHowe), time.Date(2026, 4, 5, 23, 40, 0, 0, lordHowe), 5, 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := []Record{{Time: tc.call, Model: "m", Provider: "p", Status: 429}}
			if tc.period == All {
				recs = append(recs, Record{Time: time.Date(2026, 3, 2, 12, 0, 0, 0, loc), Status: 200})
			}
			data := analyzeWith(tc.period, AnalyticsFilter{}, tc.now, recs, mockPriceLookup(nil))
			count := 0
			for _, point := range data.ErrorTrend {
				count += point.RateLimited
				if point.RateLimited > 0 && (point.Time.Day() != tc.wantDay || (tc.period == Today && point.Time.Hour() != tc.wantHour)) {
					t.Fatalf("error bucket = %s, want day %d hour %d", point.Time, tc.wantDay, tc.wantHour)
				}
			}
			if count != 1 {
				t.Fatalf("trend lost error: count = %d", count)
			}
		})
	}
}
func TestAnalyzeStreamFailureAndRejectionInvariants(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	pricing := map[string]*catalog.Price{
		"prov1/m1": {Input: 10, Output: 30},
	}
	lookup := mockPriceLookup(pricing)

	recs := []Record{
		// 1. Clean success: status 200, streamed
		{
			Time:     now.Add(-20 * time.Minute),
			Agent:    "agent1",
			Provider: "prov1",
			Model:    "m1",
			Input:    1000,
			Output:   500,
			Millis:   1200,
			TTFT:     200,
			Status:   200,
			Session:  "sess-ok",
		},
		// 2. Stream error with HTTP 200: status 200, but Error is nonempty
		{
			Time:     now.Add(-15 * time.Minute),
			Agent:    "agent1",
			Provider: "prov1",
			Model:    "m1",
			Input:    1000,
			Output:   200,
			Millis:   1500,
			TTFT:     300,
			Status:   200,
			Error:    "synthetic stream failure",
			Session:  "sess-stream-err",
		},
		// 3. Local gateway rejection: Rejected=true, status 503
		{
			Time:     now.Add(-10 * time.Minute),
			Agent:    "agent1",
			Provider: "prov1",
			Model:    "m1",
			Status:   503,
			Rejected: true,
			Error:    "no provider ready",
			Session:  "sess-rejected",
		},
		// 4. Legacy local rejection: empty provider, status 400
		{
			Time:     now.Add(-5 * time.Minute),
			Agent:    "agent1",
			Provider: "",
			Model:    "m1",
			Status:   400,
			Error:    "bad model",
			Session:  "sess-legacy-rejected",
		},
	}

	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)

	// Rejections must be completely excluded: total calls should be 2 (sess-ok and sess-stream-err)
	if data.Summary.Calls != 2 {
		t.Fatalf("summary calls = %d, want 2 (rejections must be excluded)", data.Summary.Calls)
	}

	// Stream error (HTTP 200 + Record.Error) must count as an error and other_err (not server_err 5xx)
	if data.Summary.Errors != 1 {
		t.Fatalf("summary errors = %d, want 1", data.Summary.Errors)
	}
	if data.Summary.OtherErr != 1 {
		t.Fatalf("summary other_err = %d, want 1", data.Summary.OtherErr)
	}
	if data.Summary.ServerErr != 0 {
		t.Fatalf("summary server_err = %d, want 0 (HTTP 200+err must not be server_err)", data.Summary.ServerErr)
	}
	if data.Summary.RateLimited != 0 || data.Summary.Canceled != 0 {
		t.Fatalf("unexpected error breakdown: %+v", data.Summary)
	}

	// Success rate = (2 - 1) / 2 = 0.5
	if data.Summary.SuccessRate == nil || math.Abs(*data.Summary.SuccessRate-0.5) > 1e-6 {
		t.Fatalf("success_rate = %v, want 0.5", data.Summary.SuccessRate)
	}
	// Error rate = 1 / 2 = 0.5
	if data.Summary.ErrorRate == nil || math.Abs(*data.Summary.ErrorRate-0.5) > 1e-6 {
		t.Fatalf("error_rate = %v, want 0.5", data.Summary.ErrorRate)
	}

	// TTFT sample eligibility:
	// sess-stream-err has TTFT 300, but failed; only sess-ok (TTFT 200) must be included.
	if data.Summary.TTFTP50 == nil || math.Abs(float64(*data.Summary.TTFTP50-200)) > 200*0.01+1 {
		t.Fatalf("ttft_p50 = %v, want ~200", data.Summary.TTFTP50)
	}
	if data.Summary.TTFTP95 == nil || math.Abs(float64(*data.Summary.TTFTP95-200)) > 200*0.01+1 {
		t.Fatalf("ttft_p95 = %v, want ~200", data.Summary.TTFTP95)
	}

	// Decode calls: only sess-ok should be counted (1 call)
	if data.Summary.DecodeCalls != 1 {
		t.Fatalf("decode_calls = %d, want 1", data.Summary.DecodeCalls)
	}

	// Error trend: stream error must appear as server_err in the trend
	var trendServerErr, trendRateLimited, trendOtherErr int
	for _, pt := range data.ErrorTrend {
		trendServerErr += pt.ServerErr
		trendRateLimited += pt.RateLimited
		trendOtherErr += pt.OtherErr
	}
	if trendOtherErr != 1 || trendRateLimited != 0 || trendServerErr != 0 {
		t.Fatalf("trend error counts: other=%d rate=%d server=%d, want 1, 0, 0", trendOtherErr, trendRateLimited, trendServerErr)
	}

	// Check RecentCalls chart "error_rate" (Error drilldown)
	errorCalls, err := recentCallsWith(Today, AnalyticsFilter{}, "error_rate", 50, now, recs, lookup)
	if err != nil {
		t.Fatalf("recentCalls error_rate failed: %v", err)
	}
	if len(errorCalls) != 1 {
		t.Fatalf("recentCalls error_rate returned %d calls, want 1", len(errorCalls))
	}
	if errorCalls[0].Session != "sess-stream-err" {
		t.Fatalf("recentCalls error_rate call session = %q, want sess-stream-err", errorCalls[0].Session)
	}
	if errorCalls[0].Status != 200 || errorCalls[0].Error != "synthetic stream failure" {
		t.Fatalf("recentCalls error_rate call status/error = %d / %q", errorCalls[0].Status, errorCalls[0].Error)
	}

	// Check RecentCalls chart "ttft" (TTFT drilldown)
	ttftCalls, err := recentCallsWith(Today, AnalyticsFilter{}, "ttft", 50, now, recs, lookup)
	if err != nil {
		t.Fatalf("recentCalls ttft failed: %v", err)
	}
	if len(ttftCalls) != 1 {
		t.Fatalf("recentCalls ttft returned %d calls, want 1", len(ttftCalls))
	}
	if ttftCalls[0].Session != "sess-ok" {
		t.Fatalf("recentCalls ttft call session = %q, want sess-ok", ttftCalls[0].Session)
	}

	// Check RecentCalls chart "speed" (Speed drilldown)
	speedCalls, err := recentCallsWith(Today, AnalyticsFilter{}, "speed", 50, now, recs, lookup)
	if err != nil {
		t.Fatalf("recentCalls speed failed: %v", err)
	}
	if len(speedCalls) != 1 {
		t.Fatalf("recentCalls speed returned %d calls, want 1", len(speedCalls))
	}
	if speedCalls[0].Session != "sess-ok" {
		t.Fatalf("recentCalls speed call session = %q, want sess-ok", speedCalls[0].Session)
	}
}

func TestAnalyticsReasoningSpeedUsesAnswerWindow(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-3 * time.Minute), Provider: "p", Model: "answer", Status: 200, Output: 2200, Reasoning: 2000, TTFT: 100, FirstText: 1000, Millis: 3000},
		{Time: now.Add(-2 * time.Minute), Provider: "p", Model: "plain", Status: 200, Output: 50, TTFT: 100, Millis: 1100},
		{Time: now.Add(-time.Minute), Provider: "p", Model: "tools", Status: 200, Output: 2000, Reasoning: 2000, TTFT: 100, Millis: 1100},
	}
	lookup := mockPriceLookup(nil)
	data := analyzeWith(Today, AnalyticsFilter{}, now, recs, lookup)
	if data.Summary.DecodeCalls != 2 || data.Summary.Speed == nil || math.Abs(*data.Summary.Speed-250.0/3) > 1e-6 {
		t.Fatalf("summary speed/count = %v/%d, want 250/3 and 2", data.Summary.Speed, data.Summary.DecodeCalls)
	}
	calls, err := recentCallsWith(Today, AnalyticsFilter{}, "speed", 50, now, recs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Model != "plain" || calls[1].Model != "answer" || calls[1].Speed == nil || *calls[1].Speed != 100 {
		t.Fatalf("speed calls = %+v, want plain 50 then answer 100, no tool-only reply", calls)
	}
}

func TestDefaultPriceLookupAndAnalyticsSettingsPricing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// Local deterministic catalog fixture for maker fallback.
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	makerFixture := `{"openai":{"models":{"catalog-model":{"id":"catalog-model","cost":{"input":5,"output":15}}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(makerFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	makePrice := func(v float64) settings.ModelPrice {
		return settings.ModelPrice{Input: new(v), Output: new(v), CacheRead: new(v), CacheWrite: new(v)}
	}

	// Settings with:
	// - explicit provider/model: "prov-a/model-explicit" -> $10/M
	// - known zero price: "prov-a/model-free" -> $0/M
	// - wildcard provider override: "prov-b/*" -> $7/M
	// - wildcard model override across all providers: "*/model-wildcard" -> $3/M
	cfg := settings.Settings{
		ModelPrices: map[string]settings.ModelPrice{
			"prov-a/model-explicit": makePrice(10),
			"prov-a/model-free":     makePrice(0),
			"prov-b/*":              makePrice(7),
			"*/model-wildcard":      makePrice(3),
		},
	}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// Verify defaultPriceLookup resolves identical to NewPricer / pricer across all cases:
	lookup := defaultPriceLookup()
	pricerSum := NewPricer()

	testCases := []struct {
		name      string
		provider  string
		model     string
		wantPrice bool
		wantRate  float64
	}{
		{"explicit provider/model", "prov-a", "model-explicit", true, 10},
		{"known zero price", "prov-a", "model-free", true, 0},
		{"wildcard provider override", "prov-b", "any-model", true, 7},
		{"wildcard any-provider model override", "prov-c", "model-wildcard", true, 3},
		{"maker catalog fallback", "openai", "catalog-model", true, 5},
		{"unknown remains unknown", "unknown-prov", "unknown-model", false, 0},
	}

	for _, tc := range testCases {
		t.Run("lookup_"+tc.name, func(t *testing.T) {
			got := lookup(tc.provider, tc.model)
			rec := Record{Provider: tc.provider, Model: tc.model, Input: 1_000_000, Output: 0}
			tot := pricerSum([]Record{rec})

			if !tc.wantPrice {
				if got != nil {
					t.Fatalf("expected nil price, got %+v", got)
				}
				if tot.Cost != 0 || tot.Unpriced != 1 {
					t.Fatalf("NewPricer expected unpriced, got cost=%v unpriced=%d", tot.Cost, tot.Unpriced)
				}
				return
			}

			if got == nil {
				t.Fatalf("expected price rate %v, got nil", tc.wantRate)
			}
			if got.Input != tc.wantRate {
				t.Fatalf("price input rate = %v, want %v", got.Input, tc.wantRate)
			}
			expectedCost := got.Cost(rec.Input, rec.Output, rec.CacheRead, rec.CacheWrite)
			if tot.Cost != expectedCost || tot.Unpriced != 0 {
				t.Fatalf("NewPricer totals mismatch: cost=%v want=%v unpriced=%d", tot.Cost, expectedCost, tot.Unpriced)
			}
		})
	}

	// Integration verification with Analyze() and RecentCalls() using usage log
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	// the last week, not Today: records minutes old cross midnight just after it
	now := time.Now()
	records := []Record{
		{Time: now.Add(-10 * time.Minute), Provider: "prov-a", Model: "model-explicit", Input: 1_000_000, Status: 200},
		{Time: now.Add(-8 * time.Minute), Provider: "prov-a", Model: "model-free", Input: 1_000_000, Status: 200},
		{Time: now.Add(-6 * time.Minute), Provider: "prov-b", Model: "other-model", Input: 1_000_000, Status: 200},
		{Time: now.Add(-4 * time.Minute), Provider: "prov-c", Model: "model-wildcard", Input: 1_000_000, Status: 200},
		{Time: now.Add(-2 * time.Minute), Provider: "openai", Model: "catalog-model", Input: 1_000_000, Status: 200},
		{Time: now.Add(-1 * time.Minute), Provider: "unknown-prov", Model: "unknown-model", Input: 1_000_000, Status: 200},
	}
	for _, r := range records {
		Append(r)
	}

	data := Analyze(Week, AnalyticsFilter{})
	expectedTotalCost := 10.0 + 0.0 + 7.0 + 3.0 + 5.0
	if math.Abs(data.Summary.Cost-expectedTotalCost) > 1e-6 {
		t.Fatalf("Analyze Summary.Cost = %v, want %v", data.Summary.Cost, expectedTotalCost)
	}
	if data.Summary.Unpriced != 1 {
		t.Fatalf("Analyze Summary.Unpriced = %d, want 1 (known zero is priced)", data.Summary.Unpriced)
	}

	modelRankings := data.Rankings["model"].ByCost
	for _, item := range modelRankings {
		if item.Key == "model-free" {
			if item.HasUnpriced {
				t.Errorf("model-free should not have HasUnpriced=true")
			}
			if item.MetricVal == nil || *item.MetricVal != 0.0 {
				t.Errorf("model-free MetricVal = %v, want 0.0", item.MetricVal)
			}
		}
		if item.Key == "unknown-model" {
			if !item.HasUnpriced {
				t.Errorf("unknown-model should have HasUnpriced=true")
			}
		}
	}

	recent, err := RecentCalls(Week, AnalyticsFilter{}, "cost", 10)
	if err != nil {
		t.Fatalf("RecentCalls failed: %v", err)
	}
	if len(recent) != 6 {
		t.Fatalf("RecentCalls returned %d records, want 6", len(recent))
	}

	costByModel := map[string]*float64{}
	for _, call := range recent {
		costByModel[call.Record.Model] = call.Cost
	}
	if c, ok := costByModel["model-explicit"]; !ok || c == nil || *c != 10.0 {
		t.Errorf("model-explicit cost = %v, want 10.0", c)
	}
	if c, ok := costByModel["model-free"]; !ok || c == nil || *c != 0.0 {
		t.Errorf("model-free cost = %v, want 0.0", c)
	}
	if c, ok := costByModel["other-model"]; !ok || c == nil || *c != 7.0 {
		t.Errorf("other-model (prov-b/*) cost = %v, want 7.0", c)
	}
	if c, ok := costByModel["model-wildcard"]; !ok || c == nil || *c != 3.0 {
		t.Errorf("model-wildcard (*/model-wildcard) cost = %v, want 3.0", c)
	}
	if c, ok := costByModel["catalog-model"]; !ok || c == nil || *c != 5.0 {
		t.Errorf("catalog-model cost = %v, want 5.0", c)
	}
	if c, ok := costByModel["unknown-model"]; !ok || c != nil {
		t.Errorf("unknown-model cost = %v, want nil", c)
	}
}
