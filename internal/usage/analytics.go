package usage

import (
	"errors"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// MinDecodeIntervalMs is the minimum delivery interval (ms - ttft_ms) required
// for a call to be eligible for TPS / decode speed calculations in Analytics.
// Shorter intervals reflect buffered deliveries or clock-resolution artifacts
// rather than actual model generation speed and are excluded from speed metrics.
const MinDecodeIntervalMs = 100

func isEligibleDecode(r Record) bool {
	return !r.Failed() && r.TTFT > 0 && r.Output > 0 && r.Millis > r.TTFT && (r.Millis-r.TTFT) >= MinDecodeIntervalMs
}

func isShortDecode(r Record) bool {
	return !r.Failed() && r.TTFT > 0 && r.Output > 0 && r.Millis > r.TTFT && (r.Millis-r.TTFT) < MinDecodeIntervalMs
}
var ErrInvalidChart = errors.New("invalid chart_id")

// AnalyticsFilter holds composite filters for analytics queries.
type AnalyticsFilter struct {
	Model    string
	Provider string
	Agent    string
}

// AnalyticsSummary embeds existing Totals and always emits extra fields.
type AnalyticsSummary struct {
	Totals
	SuccessRate  *float64 `json:"success_rate"`
	ErrorRate    *float64 `json:"error_rate"`
	RateLimited  int      `json:"rate_limited"`
	ServerErr    int      `json:"server_err"`
	OtherErr     int      `json:"other_err"`
	Canceled     int      `json:"canceled"`
	TTFTP50      *int64   `json:"ttft_p50"`
	TTFTP95      *int64   `json:"ttft_p95"`
	DecodeCalls  int      `json:"decode_calls"`
	ExcludedDecodeCalls int `json:"excluded_decode_calls"`
	Speed        *float64 `json:"speed"`
	CacheHitRate *float64 `json:"cache_hit_rate"`
}

// RankItem is one ranked entry in a ranking list.
type RankItem struct {
	Key          string   `json:"key"`
	MetricVal    *float64 `json:"metric_val"`
	Insufficient bool     `json:"insufficient"`
	UnknownCache bool     `json:"unknown_cache"`
	HasUnpriced  bool     `json:"has_unpriced"`
	Share        float64  `json:"share"`
	AnalyticsSummary
}

// RankingSuite holds the 5 ranking lists for a dimension.
type RankingSuite struct {
	ByErrorRate []RankItem `json:"by_error_rate"`
	ByTTFT      []RankItem `json:"by_ttft"`
	BySpeed     []RankItem `json:"by_speed"`
	ByCost      []RankItem `json:"by_cost"`
	ByCacheRate []RankItem `json:"by_cache_rate"`
}

// TrendPoint is one time bucket in the error trend.
type TrendPoint struct {
	Time        time.Time `json:"time"`
	Label       string    `json:"label"`
	RateLimited int       `json:"rate_limited"`
	ServerErr   int       `json:"server_err"`
	OtherErr    int       `json:"other_err"`
}

// FilterOptions holds available filter options for the current period.
type FilterOptions struct {
	Model    []string `json:"model"`
	Provider []string `json:"provider"`
	Agent    []string `json:"agent"`
}

// AnalyticsData is the complete response for GET /api/analytics.
type AnalyticsData struct {
	Period     Period                  `json:"period"`
	Since      time.Time               `json:"since"`
	Bucket     string                  `json:"bucket"`
	Summary    AnalyticsSummary        `json:"summary"`
	Rankings   map[string]RankingSuite `json:"rankings"`
	ErrorTrend []TrendPoint            `json:"error_trend"`
	Filters    FilterOptions           `json:"filters"`
}

// CallWithCost is a Record with an attached nullable cost for /api/analytics/calls.
type CallWithCost struct {
	Record
	Cost *float64 `json:"cost"`
}

// PriceLookup resolves the effective price for a model under a provider.
type PriceLookup func(providerID, modelID string) *catalog.Price

// defaultPriceLookup shares the ledger's effective-price resolution and memoization.
func defaultPriceLookup() PriceLookup {
	priceOf := pricer()
	return func(providerID, modelID string) *catalog.Price {
		return priceOf(Record{Provider: providerID, Model: modelID})
	}
}

// quantilesNearestRank computes nearest-rank ceil(p * n) - 1.
// values must be sorted in ascending order.
func quantilesNearestRank(vals []int64, p float64) int64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return vals[idx]
}

// entityAccumulator aggregates calls for a single dimension entity or summary.
type entityAccumulator struct {
	totals      Totals
	rateLimited int // 429
	serverErr   int // 5xx or stream error
	otherErr    int // 4xx except 429, 499
	canceled    int // 499
	hasUnpriced bool
	ttftSamples []int64 // valid ttfts (!Failed() && TTFT > 0)
	decodeCalls int     // eligible decode calls: !Failed() && TTFT > 0 && Output > 0 && (Millis - TTFT) >= MinDecodeIntervalMs
	excludedDecodeCalls int // short intervals: !Failed() && TTFT > 0 && Output > 0 && Millis > TTFT && (Millis - TTFT) < MinDecodeIntervalMs
	analyticsDecodeMs int64 // decode time (ms) for eligible decode calls only
	analyticsDecodeOut int   // output tokens for eligible decode calls only
}

func (ea *entityAccumulator) add(r Record, pr *catalog.Price) {
	ea.totals.add(r, pr)

	if r.Input+r.Output > 0 && pr == nil {
		ea.hasUnpriced = true
	}

	// Classify status (499 is canceled, excluded from error counts)
	if r.Status == 499 {
		ea.canceled++
	} else if r.Status == 429 {
		ea.rateLimited++
	} else if r.Status >= 500 {
		ea.serverErr++
	} else if r.Status >= 400 {
		ea.otherErr++
	} else if r.Failed() {
		// Streamed failure where HTTP status was left at 200
		ea.serverErr++
	}

	// Streamed TTFT samples and decode call count
	if r.TTFT > 0 && !r.Failed() {
		ea.ttftSamples = append(ea.ttftSamples, r.TTFT)
		if isEligibleDecode(r) {
			ea.decodeCalls++
			ea.analyticsDecodeMs += r.Millis - r.TTFT
			ea.analyticsDecodeOut += r.Output
		} else if isShortDecode(r) {
			ea.excludedDecodeCalls++
		}
	}
}

func (ea *entityAccumulator) toSummary() AnalyticsSummary {
	s := AnalyticsSummary{
		Totals:      ea.totals,
		RateLimited: ea.rateLimited,
		ServerErr:   ea.serverErr,
		OtherErr:    ea.otherErr,
		Canceled:    ea.canceled,
		DecodeCalls:         ea.decodeCalls,
		ExcludedDecodeCalls: ea.excludedDecodeCalls,
	}

	// Success & Error rates (calls == 0 => null; error_rate excludes 499)
	if ea.totals.Calls > 0 {
		succRate := float64(ea.totals.Calls-ea.totals.Errors) / float64(ea.totals.Calls)
		s.SuccessRate = &succRate
		errCount := ea.rateLimited + ea.serverErr + ea.otherErr
		errRate := float64(errCount) / float64(ea.totals.Calls)
		s.ErrorRate = &errRate
	}

	// TTFT quantiles (timed == 0 => null)
	if ea.totals.Timed > 0 && len(ea.ttftSamples) > 0 {
		slices.Sort(ea.ttftSamples)
		p50 := quantilesNearestRank(ea.ttftSamples, 0.50)
		p95 := quantilesNearestRank(ea.ttftSamples, 0.95)
		s.TTFTP50 = &p50
		s.TTFTP95 = &p95
	}

	// Speed using analytics-only decode sums (decode_calls == 0 => null)
	if ea.decodeCalls > 0 && ea.analyticsDecodeMs > 0 {
		spd := float64(ea.analyticsDecodeOut) / (float64(ea.analyticsDecodeMs) / 1000.0)
		s.Speed = &spd
	}

	// Cache hit rate (input + cache_read == 0 => null)
	denom := ea.totals.Input + ea.totals.CacheRead
	if denom > 0 {
		chr := float64(ea.totals.CacheRead) / float64(denom)
		s.CacheHitRate = &chr
	}

	return s
}

// analyticsCalendarDays counts local dates without treating a DST day as 24 hours.
func analyticsCalendarDays(from, to time.Time) int {
	f := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	t := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(t.Sub(f) / (24 * time.Hour))
}

func analyticsSince(p Period, now time.Time, recs []Record) (time.Time, string, int) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch p {
	case Today:
		// a day 24.5 hours long (a half-hour DST change) has a 25th bucket
		// for its last half hour, as the Usage timeline draws it
		n := 0
		for end := day.AddDate(0, 0, 1); day.Add(time.Duration(n) * time.Hour).Before(end); n++ {
		}
		return day, "hour", n
	case Week:
		return day.AddDate(0, 0, -6), "day", 7
	case Month:
		return day.AddDate(0, 0, -29), "day", 30
	default:
		// All
		since := day
		if len(recs) > 0 {
			var first time.Time
			for _, r := range recs {
				if r.IsRejected() {
					continue
				}
				t := r.Time.In(now.Location())
				if first.IsZero() || t.Before(first) {
					first = t
				}
			}
			if !first.IsZero() {
				since = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, now.Location())
			}
		}
		off := (int(since.Weekday()) + 6) % 7
		since = since.AddDate(0, 0, -off)
		n := analyticsCalendarDays(since, day)/7 + 1
		if n <= 0 {
			n = 1
		}
		return since, "week", n
	}
}

// Analyze analyzes usage records for a period with filters as of now.
func Analyze(p Period, filter AnalyticsFilter) AnalyticsData {
	now := time.Now()
	var since time.Time
	if p != All {
		since, _, _ = analyticsSince(p, now, nil)
	}
	return analyzeWith(p, filter, now, Load(since), defaultPriceLookup())
}

// analyzeWith is the internal deterministic engine for Analyze.
func analyzeWith(p Period, filter AnalyticsFilter, now time.Time, recs []Record, priceOf PriceLookup) AnalyticsData {
	renamed := provider.Renamed()
	since, bucket, n := analyticsSince(p, now, recs)

	out := AnalyticsData{
		Period: p,
		Since:  since,
		Bucket: bucket,
		Rankings: map[string]RankingSuite{
			"model":    {},
			"provider": {},
			"agent":    {},
		},
		ErrorTrend: []TrendPoint{},
		Filters: FilterOptions{
			Model:    []string{},
			Provider: []string{},
			Agent:    []string{},
		},
	}

	// Initialize error trend timeline
	switch out.Bucket {
	case "hour":
		for h := range n {
			t := out.Since.Add(time.Duration(h) * time.Hour)
			out.ErrorTrend = append(out.ErrorTrend, TrendPoint{Label: t.Format("15"), Time: t})
		}
	case "day":
		for i := range n {
			t := out.Since.AddDate(0, 0, i)
			out.ErrorTrend = append(out.ErrorTrend, TrendPoint{Label: t.Format("Jan 2"), Time: t})
		}
	case "week":
		for i := range n {
			t := out.Since.AddDate(0, 0, 7*i)
			out.ErrorTrend = append(out.ErrorTrend, TrendPoint{Label: t.Format("Jan 2"), Time: t})
		}
	}

	modelsSet := map[string]struct{}{}
	providersSet := map[string]struct{}{}
	agentsSet := map[string]struct{}{}

	summaryAcc := &entityAccumulator{}
	modelAccs := map[string]*entityAccumulator{}
	providerAccs := map[string]*entityAccumulator{}
	agentAccs := map[string]*entityAccumulator{}

	// Single pass: collect filter options for the period and aggregate matching records
	for _, rawR := range recs {
		if rawR.IsRejected() {
			continue
		}
		r := rawR
		if newP, ok := renamed[r.Provider]; ok {
			r.Provider = newP
		}
		t := r.Time.In(now.Location())
		if t.Before(out.Since) {
			continue
		}

		// Collect filter options from all records in the period before filtering
		aid := AgentOf(r.Agent)
		if r.Model != "" {
			modelsSet[r.Model] = struct{}{}
		}
		if r.Provider != "" {
			providersSet[r.Provider] = struct{}{}
		}
		if aid != "" {
			agentsSet[aid] = struct{}{}
		}

		// Apply composite filter
		if filter.Model != "" && r.Model != filter.Model {
			continue
		}
		if filter.Provider != "" && r.Provider != filter.Provider {
			continue
		}
		if filter.Agent != "" && aid != filter.Agent {
			continue
		}

		pr := priceOf(r.Provider, r.Model)

		// 1. Summary
		summaryAcc.add(r, pr)

		// 2. Error trend: status counts exclude 499
		var bi int
		switch out.Bucket {
		case "hour":
			bi = int(t.Sub(out.Since).Hours())
		case "day":
			bi = analyticsCalendarDays(out.Since, t)
		case "week":
			bi = analyticsCalendarDays(out.Since, t) / 7
		}
		if bi >= 0 && bi < len(out.ErrorTrend) {
			if r.Status == 429 {
				out.ErrorTrend[bi].RateLimited++
			} else if r.Status >= 500 {
				out.ErrorTrend[bi].ServerErr++
			} else if r.Status >= 400 && r.Status != 499 {
				out.ErrorTrend[bi].OtherErr++
			} else if r.Failed() && r.Status != 499 {
				out.ErrorTrend[bi].ServerErr++
			}
		}

		// 3. Dimensional groupings (original keys)
		if r.Model != "" {
			mAcc := modelAccs[r.Model]
			if mAcc == nil {
				mAcc = &entityAccumulator{}
				modelAccs[r.Model] = mAcc
			}
			mAcc.add(r, pr)
		}

		if r.Provider != "" {
			pAcc := providerAccs[r.Provider]
			if pAcc == nil {
				pAcc = &entityAccumulator{}
				providerAccs[r.Provider] = pAcc
			}
			pAcc.add(r, pr)
		}

		if aid != "" {
			aAcc := agentAccs[aid]
			if aAcc == nil {
				aAcc = &entityAccumulator{}
				agentAccs[aid] = aAcc
			}
			aAcc.add(r, pr)
		}
	}

	for m := range modelsSet {
		out.Filters.Model = append(out.Filters.Model, m)
	}
	sort.Strings(out.Filters.Model)

	for p := range providersSet {
		out.Filters.Provider = append(out.Filters.Provider, p)
	}
	sort.Strings(out.Filters.Provider)

	for a := range agentsSet {
		out.Filters.Agent = append(out.Filters.Agent, a)
	}
	sort.Strings(out.Filters.Agent)

	out.Summary = summaryAcc.toSummary()
	totalCost := out.Summary.Cost

	out.Rankings["model"] = buildRankingSuite(modelAccs, totalCost)
	out.Rankings["provider"] = buildRankingSuite(providerAccs, totalCost)
	out.Rankings["agent"] = buildRankingSuite(agentAccs, totalCost)

	return out
}

func sortRanking(items []RankItem, ascending bool, tierFn func(r *RankItem) int) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := &items[i], &items[j]
		tierA, tierB := tierFn(a), tierFn(b)
		if tierA != tierB {
			return tierA < tierB
		}
		if tierA == 0 {
			va, vb := 0.0, 0.0
			if a.MetricVal != nil {
				va = *a.MetricVal
			}
			if b.MetricVal != nil {
				vb = *b.MetricVal
			}
			if va != vb {
				if ascending {
					return va < vb
				}
				return va > vb
			}
		}
		return a.Key < b.Key
	})
}

// buildRankingSuite builds the 5 sorted rankings for one dimension.
func buildRankingSuite(accs map[string]*entityAccumulator, totalCost float64) RankingSuite {
	suite := RankingSuite{
		ByErrorRate: []RankItem{},
		ByTTFT:      []RankItem{},
		BySpeed:     []RankItem{},
		ByCost:      []RankItem{},
		ByCacheRate: []RankItem{},
	}

	keys := make([]string, 0, len(accs))
	for k := range accs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		ea := accs[k]
		summary := ea.toSummary()

		share := 0.0
		if totalCost > 0 && summary.Cost > 0 {
			share = summary.Cost / totalCost
		}

		baseItem := RankItem{
			Key:              k,
			HasUnpriced:      ea.hasUnpriced,
			Share:            share,
			AnalyticsSummary: summary,
		}

		// 1. ByErrorRate (threshold calls >= 10, error_rate DESC)
		itemErr := baseItem
		if summary.ErrorRate != nil {
			val := *summary.ErrorRate
			itemErr.MetricVal = &val
		}
		itemErr.Insufficient = summary.Calls < 10
		suite.ByErrorRate = append(suite.ByErrorRate, itemErr)

		// 2. ByTTFT (threshold timed >= 10, ttft_p95 DESC)
		itemTTFT := baseItem
		if summary.TTFTP95 != nil {
			val := float64(*summary.TTFTP95)
			itemTTFT.MetricVal = &val
		}
		itemTTFT.Insufficient = summary.Timed < 10
		suite.ByTTFT = append(suite.ByTTFT, itemTTFT)

		// 3. BySpeed (threshold decode_calls >= 10, speed ASC)
		itemSpd := baseItem
		if summary.Speed != nil {
			val := *summary.Speed
			itemSpd.MetricVal = &val
		}
		itemSpd.Insufficient = summary.DecodeCalls < 10
		suite.BySpeed = append(suite.BySpeed, itemSpd)

		// 4. ByCost (cost DESC, has_unpriced flag when entity has unpriced records)
		itemCost := baseItem
		valCost := summary.Cost
		itemCost.MetricVal = &valCost
		itemCost.Insufficient = false
		suite.ByCost = append(suite.ByCost, itemCost)

		// 5. ByCacheRate (threshold input + cache_read >= 10000, unknown_cache if read=write=0, cache_hit_rate ASC)
		itemCache := baseItem
		if summary.CacheHitRate != nil {
			val := *summary.CacheHitRate
			itemCache.MetricVal = &val
		}
		cacheInput := summary.Input + summary.CacheRead
		itemCache.Insufficient = cacheInput < 10000
		itemCache.UnknownCache = (summary.CacheRead == 0 && summary.CacheWrite == 0)
		suite.ByCacheRate = append(suite.ByCacheRate, itemCache)
	}

	tierInsufficient := func(r *RankItem) int {
		if r.Insufficient {
			return 1
		}
		return 0
	}

	tierCache := func(r *RankItem) int {
		if r.UnknownCache {
			return 2
		}
		if r.Insufficient {
			return 1
		}
		return 0
	}

	sortRanking(suite.ByErrorRate, false, tierInsufficient)
	sortRanking(suite.ByTTFT, false, tierInsufficient)
	sortRanking(suite.BySpeed, true, tierInsufficient)
	sortRanking(suite.ByCost, false, func(*RankItem) int { return 0 })
	sortRanking(suite.ByCacheRate, true, tierCache)

	return suite
}

// RecentCalls retrieves and filters up to limit (max 50) records for a specific chart.
func RecentCalls(p Period, filter AnalyticsFilter, chartID string, limit int) ([]CallWithCost, error) {
	now := time.Now()
	var since time.Time
	if p != All {
		since, _, _ = analyticsSince(p, now, nil)
	}
	return recentCallsWith(p, filter, chartID, limit, now, Load(since), defaultPriceLookup())
}

// recentCallsWith is the internal deterministic engine for RecentCalls.
func recentCallsWith(p Period, filter AnalyticsFilter, chartID string, limit int, now time.Time, recs []Record, priceOf PriceLookup) ([]CallWithCost, error) {
	// 1. Validate chart_id first before checking records or limit
	switch chartID {
	case "1.1", "2.1", "2.2", "3.1", "3.2":
	default:
		return nil, ErrInvalidChart
	}

	if limit <= 0 || limit > 50 {
		limit = 50
	}

	renamed := provider.Renamed()
	since, _, _ := analyticsSince(p, now, recs)

	type filteredItem struct {
		rec       Record
		speed     float64
		cost      float64
		costKnown bool
	}

	var matched []filteredItem

	for _, rawR := range recs {
		if rawR.IsRejected() {
			continue
		}
		r := rawR
		if newP, ok := renamed[r.Provider]; ok {
			r.Provider = newP
		}
		t := r.Time.In(now.Location())
		if t.Before(since) {
			continue
		}

		aid := AgentOf(r.Agent)
		if filter.Model != "" && r.Model != filter.Model {
			continue
		}
		if filter.Provider != "" && r.Provider != filter.Provider {
			continue
		}
		if filter.Agent != "" && aid != filter.Agent {
			continue
		}

		// Filter by chart criteria
		switch chartID {
		case "1.1":
			// Failed calls excluding 499
			if r.Failed() && r.Status != 499 {
				matched = append(matched, filteredItem{rec: r})
			}
		case "2.1":
			// success TTFT > 0
			if !r.Failed() && r.TTFT > 0 {
				matched = append(matched, filteredItem{rec: r})
			}
		case "2.2":
			// valid decode out/((ms-ttft)/1000), minimum 100ms interval required
			if isEligibleDecode(r) {
				spd := float64(r.Output) / (float64(r.Millis-r.TTFT) / 1000.0)
				matched = append(matched, filteredItem{rec: r, speed: spd})
			}
		case "3.1":
			// costs
			matched = append(matched, filteredItem{rec: r})
		case "3.2":
			// input
			matched = append(matched, filteredItem{rec: r})
		}
	}

	// Keep price data with each record so sorting preserves its association.
	if chartID == "3.1" {
		for i := range matched {
			m := &matched[i]
			r := m.rec
			if r.Input+r.Output > 0 {
				if pr := priceOf(r.Provider, r.Model); pr != nil {
					m.cost = pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
					m.costKnown = true
				}
			}
		}
	}

	// Sort per chartID specification
	switch chartID {
	case "1.1":
		// time DESC
		sort.SliceStable(matched, func(i, j int) bool {
			return matched[i].rec.Time.After(matched[j].rec.Time)
		})
	case "2.1":
		// TTFT DESC
		sort.SliceStable(matched, func(i, j int) bool {
			if matched[i].rec.TTFT != matched[j].rec.TTFT {
				return matched[i].rec.TTFT > matched[j].rec.TTFT
			}
			return matched[i].rec.Time.After(matched[j].rec.Time)
		})
	case "2.2":
		// valid decode speed ASC
		sort.SliceStable(matched, func(i, j int) bool {
			if matched[i].speed != matched[j].speed {
				return matched[i].speed < matched[j].speed
			}
			return matched[i].rec.Time.After(matched[j].rec.Time)
		})
	case "3.1":
		// costs DESC, known before unknown
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := &matched[i], &matched[j]
			if a.costKnown != b.costKnown {
				return a.costKnown
			}
			if a.costKnown && a.cost != b.cost {
				return a.cost > b.cost
			}
			return matched[i].rec.Time.After(matched[j].rec.Time)
		})
	case "3.2":
		// input DESC
		sort.SliceStable(matched, func(i, j int) bool {
			if matched[i].rec.Input != matched[j].rec.Input {
				return matched[i].rec.Input > matched[j].rec.Input
			}
			return matched[i].rec.Time.After(matched[j].rec.Time)
		})
	}

	// Truncate to limit
	if len(matched) > limit {
		matched = matched[:limit]
	}

	// Compute cost only for the final returned items
	res := make([]CallWithCost, len(matched))
	for i, m := range matched {
		r := m.rec
		var costPtr *float64
		if chartID == "3.1" {
			if m.costKnown {
				c := m.cost
				costPtr = &c
			}
		} else if r.Input+r.Output > 0 {
			if pr := priceOf(r.Provider, r.Model); pr != nil {
				c := pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
				costPtr = &c
			}
		}
		res[i] = CallWithCost{
			Record: r,
			Cost:   costPtr,
		}
	}

	return res, nil
}
