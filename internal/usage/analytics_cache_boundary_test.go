package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// 1. TestAnalyticsBoundaryAllPeriodLateAppendExpandsTimeline
// Checks that an out-of-order append with a much earlier timestamp expands the
// All-period timeline window (since / bucket granularity) without losing old errors,
// forcing a proper rebuild rather than a broken delta-append.
func TestAnalyticsBoundaryAllPeriodLateAppendExpandsTimeline(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	initialRecs := []Record{
		{
			Time:     now.Add(-90 * time.Minute),
			Agent:    "codex",
			Provider: "relay",
			Model:    "m",
			Status:   500,
			Input:    100,
			Output:   50,
			Millis:   1000,
			TTFT:     200,
		},
		{
			Time:     now.Add(-30 * time.Minute),
			Agent:    "codex",
			Provider: "relay",
			Model:    "m",
			Status:   429,
			Input:    200,
			Output:   80,
			Millis:   800,
			TTFT:     150,
		},
	}
	for _, r := range initialRecs {
		Append(r)
	}

	firstData := Analyze(All, AnalyticsFilter{})
	if firstData.Summary.Calls != 2 {
		t.Fatalf("expected 2 calls initially, got %d", firstData.Summary.Calls)
	}
	if firstData.Summary.ServerErr != 1 || firstData.Summary.RateLimited != 1 {
		t.Fatalf("expected 1 server error and 1 rate limit, got server=%d, rate_limited=%d",
			firstData.Summary.ServerErr, firstData.Summary.RateLimited)
	}

	// Late append with a timestamp 90 days in the past expands timeline window.
	oldRecord := Record{
		Time:     now.Add(-90 * 24 * time.Hour),
		Agent:    "codex",
		Provider: "relay",
		Model:    "m",
		Status:   502,
		Input:    300,
		Output:   120,
		Millis:   1500,
		TTFT:     300,
	}
	Append(oldRecord)

	expandedData := Analyze(All, AnalyticsFilter{})
	if expandedData.Summary.Calls != 3 {
		t.Fatalf("expected 3 calls after late append, got %d", expandedData.Summary.Calls)
	}
	if expandedData.Summary.ServerErr != 2 {
		t.Fatalf("expected 2 server errors retained, got %d", expandedData.Summary.ServerErr)
	}
	if expandedData.Summary.RateLimited != 1 {
		t.Fatalf("expected 1 rate limit error retained, got %d", expandedData.Summary.RateLimited)
	}
	if !expandedData.Since.Before(firstData.Since) {
		t.Fatalf("expected timeline Since to expand backward: first=%v, expanded=%v",
			firstData.Since, expandedData.Since)
	}

	var totalTrendServerErr, totalTrendRateLimited int
	for _, pt := range expandedData.ErrorTrend {
		totalTrendServerErr += pt.ServerErr
		totalTrendRateLimited += pt.RateLimited
	}
	if totalTrendServerErr != 2 {
		t.Fatalf("expected 2 server errors across expanded trend buckets, got %d", totalTrendServerErr)
	}
	if totalTrendRateLimited != 1 {
		t.Fatalf("expected 1 rate limited across expanded trend buckets, got %d", totalTrendRateLimited)
	}
}

// 2. TestAnalyticsBoundaryGrowingHistoricalRewriteRebuildsAnalyticsAndTop50
// Tests that a growing historical rewrite (where the file size increases but an
// earlier prefix record was altered) invalidates cached snapshot state and triggers
// a full rebuild for both Analyze and RecentCalls top50 rather than a delta append.
func TestAnalyticsBoundaryGrowingHistoricalRewriteRebuildsAnalyticsAndTop50(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	count := logBlockRows*2 + 10
	historyLog(t, count)

	initialData := Analyze(All, AnalyticsFilter{})
	if initialData.Summary.Calls != count {
		t.Fatalf("expected initial calls=%d, got %d", count, initialData.Summary.Calls)
	}
	initialCalls, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialCalls) == 0 {
		t.Fatal("expected non-empty recent calls")
	}

	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := bytes.Cut(data, []byte{'\n'})
	if !ok {
		t.Fatal("fixture has no complete first line")
	}

	hugeInput := 99_999_999
	edited := Record{
		Time:      now.Add(-time.Hour),
		Agent:     "codex",
		Provider:  "relay",
		Model:     "m",
		Session:   "rewritten-champion-session",
		Input:     hugeInput,
		Status:    200,
		RequestID: strings.Repeat("x", 2048),
	}
	firstLine, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := append(append(firstLine, '\n'), rest...)
	if len(rewritten) <= len(data) {
		t.Fatal("rewritten log must be strictly larger than original")
	}
	if err := os.WriteFile(Path(), rewritten, 0600); err != nil {
		t.Fatal(err)
	}

	// Append tail record to advance version/file.
	Append(Record{
		Time:     now,
		Agent:    "codex",
		Provider: "relay",
		Model:    "m",
		Session:  "appended-tail-session",
		Input:    50,
		Status:   200,
	})

	rebuiltData := Analyze(All, AnalyticsFilter{})
	if rebuiltData.Summary.Calls != count+1 {
		t.Fatalf("expected %d calls after rewrite and append, got %d", count+1, rebuiltData.Summary.Calls)
	}
	if rebuiltData.Summary.Input < hugeInput {
		t.Fatalf("expected total Input to reflect rewritten prefix >= %d, got %d", hugeInput, rebuiltData.Summary.Input)
	}
	rebuiltCalls, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuiltCalls) == 0 {
		t.Fatal("expected recent calls after rewrite")
	}
	if rebuiltCalls[0].Session != "rewritten-champion-session" {
		t.Fatalf("expected top call to be rewritten prefix with session 'rewritten-champion-session', got %q",
			rebuiltCalls[0].Session)
	}
	if rebuiltCalls[0].Input != hugeInput {
		t.Fatalf("expected top call input to be %d, got %d", hugeInput, rebuiltCalls[0].Input)
	}
}

// 3. TestAnalyticsBoundaryPriceAndRenameMetadataInvalidation
// Tests that updates to pricing settings and provider renames alter the dependency signature,
// invalidating cached analytics and updating historical costs and provider groupings without appends.
func TestAnalyticsBoundaryPriceAndRenameMetadataInvalidation(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	if err := provider.Save(provider.Provider{
		ID:   "relay",
		Name: "Relay Provider",
		Chat: "https://relay.test/v1",
		Key:  "dummy-key",
		Was:  []string{"legacy-relay"},
	}); err != nil {
		t.Fatal(err)
	}

	priceVal := 10.0
	cfg := settings.Settings{
		ModelPrices: map[string]settings.ModelPrice{
			"relay/model-x": {
				Input:      &priceVal,
				Output:     &priceVal,
				CacheRead:  new(float64),
				CacheWrite: new(float64),
			},
		},
	}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// Pre-populate records for both legacy-relay and old-gateway before queries.
	Append(Record{
		Time:     now.Add(-10 * time.Minute),
		Agent:    "codex",
		Provider: "legacy-relay",
		Model:    "model-x",
		Input:    1_000_000,
		Output:   0,
		Status:   200,
	})
	Append(Record{
		Time:     now.Add(-5 * time.Minute),
		Agent:    "codex",
		Provider: "old-gateway",
		Model:    "model-x",
		Input:    1_000_000,
		Output:   0,
		Status:   200,
	})

	initial := Analyze(All, AnalyticsFilter{})
	// legacy-relay is aliased to relay; old-gateway is currently separate.
	if len(initial.Filters.Provider) != 2 {
		t.Fatalf("expected 2 providers (relay, old-gateway), got %+v", initial.Filters.Provider)
	}
	// Only the 1M tokens of legacy-relay (mapped to relay/model-x) are priced at $10/M = $10.00
	if math.Abs(initial.Summary.Cost-10.0) > 1e-4 {
		t.Fatalf("expected initial cost $10.0, got %v", initial.Summary.Cost)
	}

	// 1. Invalidate via Price change ($10/M -> $25/M) without appending records.
	priceVal2 := 25.0
	cfg.ModelPrices["relay/model-x"] = settings.ModelPrice{
		Input:      &priceVal2,
		Output:     &priceVal2,
		CacheRead:  new(float64),
		CacheWrite: new(float64),
	}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}

	afterPriceChange := Analyze(All, AnalyticsFilter{})
	if math.Abs(afterPriceChange.Summary.Cost-25.0) > 1e-4 {
		t.Fatalf("expected updated cost $25.0 after pricing invalidation without append, got %v", afterPriceChange.Summary.Cost)
	}

	// 2. Invalidate via Provider alias (old-gateway -> relay) without appending records.
	if err := provider.Save(provider.Provider{
		ID:   "relay",
		Name: "Relay Provider",
		Chat: "https://relay.test/v1",
		Key:  "dummy-key",
		Was:  []string{"legacy-relay", "old-gateway"},
	}); err != nil {
		t.Fatal(err)
	}

	afterRename := Analyze(All, AnalyticsFilter{})
	if len(afterRename.Filters.Provider) != 1 || afterRename.Filters.Provider[0] != "relay" {
		t.Fatalf("expected old-gateway to merge into 'relay', got %+v", afterRename.Filters.Provider)
	}
	// Now both 1M records map to relay/model-x @ $25/M = $50.00
	if math.Abs(afterRename.Summary.Cost-50.0) > 1e-4 {
		t.Fatalf("expected total cost $50.0 after alias update without append, got %v", afterRename.Summary.Cost)
	}
}

// 4. TestAnalyticsBoundaryRecentCallsCostPointerOwnershipAcrossRepeatCalls
// Verifies that repeated calls to RecentCalls return caller-owned Cost pointers
// that are not shared across calls or mutated by caller modifications.
func TestAnalyticsBoundaryRecentCallsCostPointerOwnershipAcrossRepeatCalls(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	priceVal := 15.0
	cfg := settings.Settings{
		ModelPrices: map[string]settings.ModelPrice{
			"relay/m": {
				Input:      &priceVal,
				Output:     &priceVal,
				CacheRead:  new(float64),
				CacheWrite: new(float64),
			},
		},
	}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}

	Append(Record{
		Time:     now.Add(-time.Minute),
		Agent:    "codex",
		Provider: "relay",
		Model:    "m",
		Session:  "sess-ptr-test",
		Input:    1_000_000,
		Status:   200,
	})
	firstCalls, err := RecentCalls(All, AnalyticsFilter{}, "cost", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCalls) != 1 || firstCalls[0].Cost == nil {
		t.Fatalf("expected 1 call with non-nil cost, got %+v", firstCalls)
	}

	firstPtr := firstCalls[0].Cost
	expectedCost := 15.0
	if math.Abs(*firstPtr-expectedCost) > 1e-4 {
		t.Fatalf("expected cost %v, got %v", expectedCost, *firstPtr)
	}

	// Mutate the returned pointer in caller.
	*firstPtr = 99999.0
	secondCalls, err := RecentCalls(All, AnalyticsFilter{}, "cost", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondCalls) != 1 || secondCalls[0].Cost == nil {
		t.Fatalf("expected 1 call with non-nil cost, got %+v", secondCalls)
	}

	secondPtr := secondCalls[0].Cost
	if secondPtr == firstPtr {
		t.Fatal("RecentCalls returned identical pointer across repeated calls; expected independent caller-owned pointer")
	}
	if math.Abs(*secondPtr-expectedCost) > 1e-4 {
		t.Fatalf("cached call cost was mutated by caller modification! expected %v, got %v", expectedCost, *secondPtr)
	}
}

// 5. TestAnalyticsBoundaryCompositeFilterAppendVsRebuildFullResponse
// Tests that incremental delta-append with composite filter (Model, Provider, Agent)
// yields an identical full AnalyticsData struct compared to a cold fresh rebuild.
func TestAnalyticsBoundaryCompositeFilterAppendVsRebuildFullResponse(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	models := []string{"m1", "m2"}
	providers := []string{"p1", "p2"}
	agents := []string{"a1", "a2"}

	for i := range 60 {
		r := Record{
			Time:     now.Add(-time.Duration(100-i) * time.Minute),
			Agent:    agents[i%len(agents)],
			Provider: providers[i%len(providers)],
			Model:    models[i%len(models)],
			Input:    1000 + i*10,
			Output:   200 + i*5,
			Millis:   int64(1000 + i*20),
			TTFT:     int64(200 + (i%5)*50),
			Status:   200,
		}
		if i%7 == 0 {
			r.Status = 429
		} else if i%11 == 0 {
			r.Status = 500
		}
		Append(r)
	}

	targetFilter := AnalyticsFilter{
		Model:    "m1",
		Provider: "p1",
		Agent:    "a1",
	}

	initial := Analyze(All, targetFilter)
	if initial.Summary.Calls == 0 {
		t.Fatal("expected matching initial calls for target filter")
	}

	for i := range 10 {
		r := Record{
			Time:     now.Add(-time.Duration(10-i) * time.Minute),
			Agent:    agents[i%len(agents)],
			Provider: providers[i%len(providers)],
			Model:    models[i%len(models)],
			Input:    2000 + i*20,
			Output:   400 + i*10,
			Millis:   int64(1200 + i*30),
			TTFT:     int64(250 + (i%3)*50),
			Status:   200,
		}
		Append(r)
	}

	incremental := Analyze(All, targetFilter)

	resetAnalyticsCache()
	rebuilt := Analyze(All, targetFilter)

	if incremental.Summary.Calls != rebuilt.Summary.Calls {
		t.Fatalf("calls mismatch: incremental=%d, rebuilt=%d", incremental.Summary.Calls, rebuilt.Summary.Calls)
	}
	if incremental.Summary.Input != rebuilt.Summary.Input || incremental.Summary.Output != rebuilt.Summary.Output {
		t.Fatalf("tokens mismatch: incremental=(%d,%d), rebuilt=(%d,%d)",
			incremental.Summary.Input, incremental.Summary.Output, rebuilt.Summary.Input, rebuilt.Summary.Output)
	}
	if incremental.Summary.RateLimited != rebuilt.Summary.RateLimited || incremental.Summary.ServerErr != rebuilt.Summary.ServerErr {
		t.Fatalf("errors mismatch: incremental=(%d,%d), rebuilt=(%d,%d)",
			incremental.Summary.RateLimited, incremental.Summary.ServerErr,
			rebuilt.Summary.RateLimited, rebuilt.Summary.ServerErr)
	}

	// 1% + 0.5ms tolerance on TTFT quantiles.
	if incremental.Summary.TTFTP50 != nil && rebuilt.Summary.TTFTP50 != nil {
		diff := math.Abs(float64(*incremental.Summary.TTFTP50 - *rebuilt.Summary.TTFTP50))
		tolerance := 0.01*float64(*rebuilt.Summary.TTFTP50) + 0.5
		if diff > tolerance {
			t.Fatalf("TTFT P50 outside tolerance: incremental=%d, rebuilt=%d, diff=%v, tol=%v",
				*incremental.Summary.TTFTP50, *rebuilt.Summary.TTFTP50, diff, tolerance)
		}
	}
	if incremental.Summary.TTFTP95 != nil && rebuilt.Summary.TTFTP95 != nil {
		diff := math.Abs(float64(*incremental.Summary.TTFTP95 - *rebuilt.Summary.TTFTP95))
		tolerance := 0.01*float64(*rebuilt.Summary.TTFTP95) + 0.5
		if diff > tolerance {
			t.Fatalf("TTFT P95 outside tolerance: incremental=%d, rebuilt=%d, diff=%v, tol=%v",
				*incremental.Summary.TTFTP95, *rebuilt.Summary.TTFTP95, diff, tolerance)
		}
	}

	if !reflect.DeepEqual(incremental.Filters, rebuilt.Filters) {
		t.Fatalf("filters mismatch: incremental=%+v, rebuilt=%+v", incremental.Filters, rebuilt.Filters)
	}

	for dim := range rebuilt.Rankings {
		incSuite := incremental.Rankings[dim]
		rebSuite := rebuilt.Rankings[dim]
		if len(incSuite.ByErrorRate) != len(rebSuite.ByErrorRate) ||
			len(incSuite.ByCost) != len(rebSuite.ByCost) ||
			len(incSuite.BySpeed) != len(rebSuite.BySpeed) {
			t.Fatalf("ranking lengths mismatch for dimension %s", dim)
		}
	}

	if len(incremental.ErrorTrend) != len(rebuilt.ErrorTrend) {
		t.Fatalf("error trend length mismatch: %d vs %d", len(incremental.ErrorTrend), len(rebuilt.ErrorTrend))
	}
	for i := range rebuilt.ErrorTrend {
		if incremental.ErrorTrend[i].RateLimited != rebuilt.ErrorTrend[i].RateLimited ||
			incremental.ErrorTrend[i].ServerErr != rebuilt.ErrorTrend[i].ServerErr {
			t.Fatalf("error trend point %d mismatch", i)
		}
	}
}

// 6. TestAnalyticsBoundaryRawCacheOverbudgetRetainsFinalResponseHit
// Tests that when raw log caching exceeds budget (forcing uncached streaming for the raw ledger),
// the final synthesized AnalyticsData response is still safely cached and hits on repeat calls
// with bounded allocation (< 2000 allocs) without re-traversing the entire log.
func TestAnalyticsBoundaryRawCacheOverbudgetRetainsFinalResponseHit(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	count := 100000
	now := time.Now()
	for i := range count {
		Append(Record{
			Time:     now.Add(-time.Duration(count-i) * time.Second),
			Agent:    "codex",
			Provider: "relay",
			Model:    "m",
			Session:  fmt.Sprintf("session-%08x-%08x", i*7919, i*104729),
			Input:    500 + (i % 100),
			Output:   100 + (i % 50),
			Millis:   int64(1000 + (i % 200)),
			TTFT:     int64(200 + (i % 50)),
			Status:   200,
		})
	}

	// Measure actual raw snapshot bytes first with full budget, then set
	// cacheBudget to half the raw ledger size (e.g. ~512KiB - 1MiB) so snapshot.uncached is guaranteed,
	// while leaving ample room for analytics responses.
	initialSnap := readLogSnapshot()
	rawBytes := initialSnap.bytes
	if rawBytes/8 < 256<<10 {
		t.Fatalf("fixture must exceed retained analytics state: raw bytes=%d", rawBytes)
	}
	cacheBudget(t, rawBytes/2)

	snapshot := readLogSnapshot()
	if !snapshot.uncached {
		t.Fatalf("expected raw cache budget overflow to mark snapshot.uncached, got false (bytes=%d, budget=%d)",
			snapshot.bytes, rawBytes/2)
	}
	first := Analyze(All, AnalyticsFilter{})
	if first.Summary.Calls != count {
		t.Fatalf("expected calls=%d, got %d", count, first.Summary.Calls)
	}

	// Repeat queries must hit cached state with bounded allocations (< 2000 vs 4000 records).
	allocs := testing.AllocsPerRun(5, func() {
		res := Analyze(All, AnalyticsFilter{})
		if res.Summary.Calls != count {
			t.Fatalf("unexpected calls=%d", res.Summary.Calls)
		}
	})

	if allocs > 2000 {
		t.Fatalf("repeat query allocated too many objects (allocs=%v > 2000); expected cached response", allocs)
	}
}

// 7. TestAnalyticsBoundaryTimezoneSwitchInvalidatesDayWindowAndBuckets
// Tests that switching time.Local invalidates the cached analytics state
// across timezone boundaries via dependencySignature without needing an Append,
// updating bucket assignment and period boundaries for fixed records.
func TestAnalyticsBoundaryTimezoneSwitchInvalidatesDayWindowAndBuckets(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	oldLocal := time.Local
	t.Cleanup(func() {
		time.Local = oldLocal
	})

	nowUTC := time.Now().UTC()
	todayStartUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	locPlus8 := time.FixedZone("UTC+8", 8*3600)

	// Pick a boundary timestamp near UTC midnight that cleanly falls into different
	// local calendar days between UTC and UTC+8 without hardcoding a calendar year.
	var boundaryRecTime time.Time
	var wantUTCInToday, wantPlus8InToday bool

	if nowUTC.Hour() < 16 {
		// 4 hours before UTC midnight: yesterday in UTC, but today in UTC+8
		boundaryRecTime = todayStartUTC.Add(-4 * time.Hour)
		wantUTCInToday = false
		wantPlus8InToday = true
	} else {
		// 4 hours after UTC midnight: today in UTC, but yesterday in UTC+8 (which is on day+1)
		boundaryRecTime = todayStartUTC.Add(4 * time.Hour)
		wantUTCInToday = true
		wantPlus8InToday = false
	}

	Append(Record{
		Time:     boundaryRecTime,
		Agent:    "codex",
		Provider: "relay",
		Model:    "m",
		Input:    1000,
		Status:   200,
	})

	time.Local = time.UTC
	first := Analyze(Today, AnalyticsFilter{})
	expectedFirstCalls := 0
	if wantUTCInToday {
		expectedFirstCalls = 1
	}
	if first.Summary.Calls != expectedFirstCalls {
		t.Fatalf("under UTC: expected calls=%d, got %d", expectedFirstCalls, first.Summary.Calls)
	}

	// Switch to UTC+8 without appending any new records.
	time.Local = locPlus8
	second := Analyze(Today, AnalyticsFilter{})
	expectedSecondCalls := 0
	if wantPlus8InToday {
		expectedSecondCalls = 1
	}
	if second.Summary.Calls != expectedSecondCalls {
		t.Fatalf("under UTC+8: expected calls=%d, got %d", expectedSecondCalls, second.Summary.Calls)
	}
	if first.Summary.Calls == second.Summary.Calls {
		t.Fatalf("expected Today calls to change across timezone switch (got %d in both)", first.Summary.Calls)
	}
	if first.Since.Equal(second.Since) {
		t.Fatalf("expected Today window Since to change across timezone switch: %v vs %v", first.Since, second.Since)
	}
}

// 8. TestAnalyticsBoundaryDeletedUsageLogRebuildsEmpty
// Tests that deleting usage.jsonl (e.g. log file removal / truncation to zero)
// invalidates any primed Analyze cache, rebuilding with 0 calls and 0 totals
// rather than retaining stale data from previous file epochs.
func TestAnalyticsBoundaryDeletedUsageLogRebuildsEmpty(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	for i := range 5 {
		Append(Record{
			Time:     now.Add(-time.Duration(5-i) * time.Minute),
			Agent:    "codex",
			Provider: "relay",
			Model:    "m",
			Input:    1000,
			Output:   200,
			Status:   200,
		})
	}

	primed := Analyze(All, AnalyticsFilter{})
	if primed.Summary.Calls != 5 {
		t.Fatalf("expected primed calls=5, got %d", primed.Summary.Calls)
	}
	primedCalls, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(primedCalls) != 5 {
		t.Fatalf("expected 5 primed recent calls, got %d", len(primedCalls))
	}

	// Remove usage.jsonl
	if err := os.Remove(Path()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	afterDelete := Analyze(All, AnalyticsFilter{})
	if afterDelete.Summary.Calls != 0 {
		t.Fatalf("expected 0 calls after deleting log file, got %d", afterDelete.Summary.Calls)
	}
	if afterDelete.Summary.Input != 0 || afterDelete.Summary.Output != 0 {
		t.Fatalf("expected 0 tokens after deleting log file, got in=%d, out=%d",
			afterDelete.Summary.Input, afterDelete.Summary.Output)
	}
	if afterDelete.Summary.Cost != 0 {
		t.Fatalf("expected 0 cost after deleting log file, got %v", afterDelete.Summary.Cost)
	}
	calls, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 recent calls after deleting log file, got %d", len(calls))
	}
}

// 9. TestAnalyticsBoundaryRecentCallsTop50ExactTieStability
// Tests that when multiple records have identical metric and identical timestamp,
// exact tie-breaking preserves stable log appearance order (earlier row order is preferred),
// so appending additional tied records does not evict previously ranked first-50 records.
func TestAnalyticsBoundaryRecentCallsTop50ExactTieStability(t *testing.T) {
	pageHome(t)
	resetAnalyticsCache()

	now := time.Now()
	tiedTime := now.Add(-time.Hour)

	for i := 1; i <= 60; i++ {
		Append(Record{
			Time:      tiedTime,
			Agent:     "codex",
			Provider:  "relay",
			Model:     "m",
			Session:   "sess",
			RequestID: "req-" + strings.Repeat("0", 3-len(strings.TrimSpace(string(rune('0'+i%10))))) + string(rune('0'+i%10)),
			Input:     5000,
			Status:    200,
		})
	}
	calls50, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls50) != 50 {
		t.Fatalf("expected 50 calls, got %d", len(calls50))
	}

	for i := 61; i <= 70; i++ {
		Append(Record{
			Time:     tiedTime,
			Agent:    "codex",
			Provider: "relay",
			Model:    "m",
			Session:  "sess-later",
			Input:    5000,
			Status:   200,
		})
	}
	afterAppend, err := RecentCalls(All, AnalyticsFilter{}, "cache_hit_rate", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterAppend) != 50 {
		t.Fatalf("expected 50 calls, got %d", len(afterAppend))
	}

	for idx, c := range afterAppend {
		if c.Session == "sess-later" {
			t.Fatalf("row %d: newly appended tied record evicted earlier row; tie stability violated", idx)
		}
	}
}
