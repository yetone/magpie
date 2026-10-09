package usage

import (
	"container/heap"
	"errors"
	"math"
	"slices"
	"sort"
	"sync"
	"time"
	"unsafe"

	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

var ErrInvalidChart = errors.New("invalid chart_id")

// AnalyticsFilter holds composite filters for analytics queries.
type AnalyticsFilter struct {
	Model    string
	Provider string
	Agent    string
}

func canonicalPeriod(p Period) Period {
	switch p {
	case Today, Week, Month, All:
		return p
	default:
		return All
	}
}

// AnalyticsSummary embeds existing Totals and always emits extra fields.
type AnalyticsSummary struct {
	Totals
	SuccessRate  *float64 `json:"success_rate"`
	ErrorRate    *float64 `json:"error_rate"`
	CancelRate   *float64 `json:"cancel_rate"`
	RateLimited  int      `json:"rate_limited"`
	ServerErr    int      `json:"server_err"`
	OtherErr     int      `json:"other_err"`
	Canceled     int      `json:"canceled"`
	TTFTP50      *int64   `json:"ttft_p50"`
	TTFTP95      *int64   `json:"ttft_p95"`
	DecodeCalls  int      `json:"decode_calls"`
	Speed        *float64 `json:"speed"`
	CacheHitRate *float64 `json:"cache_hit_rate"`
}

// RankItem is one lightweight ranked entry in a ranking list.
type RankItem struct {
	Key          string   `json:"key"`
	MetricVal    *float64 `json:"metric_val"`
	Insufficient bool     `json:"insufficient"`
	UnknownCache bool     `json:"unknown_cache,omitempty"`
	HasUnpriced  bool     `json:"has_unpriced,omitempty"`
	Share        float64  `json:"share,omitempty"`
}

// RankingSuite holds a shared summary map for all entities in the dimension
// plus 5 lightweight ranked lists referencing those entities by Key.
type RankingSuite struct {
	Summaries   map[string]AnalyticsSummary `json:"summaries"`
	ByErrorRate []RankItem                  `json:"by_error_rate"`
	ByTTFT      []RankItem                  `json:"by_ttft"`
	BySpeed     []RankItem                  `json:"by_speed"`
	ByCost      []RankItem                  `json:"by_cost"`
	ByCacheRate []RankItem                  `json:"by_cache_rate"`
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

type analyticsKey struct {
	Period   Period
	Model    string
	Provider string
	Agent    string
}

type analyticsState struct {
	epoch       uint64
	version     uint64
	cursor      int64 // packedRow.Order of last processed row
	meta        string
	since       time.Time
	bucket      string
	first       time.Time
	summaryAcc  *entityAccumulator
	modelAccs   map[string]*entityAccumulator
	provAccs    map[string]*entityAccumulator
	agentAccs   map[string]*entityAccumulator
	errorTrend  []TrendPoint
	modelsSet   map[string]struct{}
	provsSet    map[string]struct{}
	agentsSet   map[string]struct{}
	response    AnalyticsData
	approxBytes int64
}

type callsCandidate struct {
	rec       Record
	speed     float64
	cost      float64
	costKnown bool
	order     int64
}

type callsKey struct {
	Period   Period
	Model    string
	Provider string
	Agent    string
	ChartID  string
}

type callsEntry struct {
	epoch       uint64
	version     uint64
	cursor      int64
	meta        string
	first       time.Time
	top50       []callsCandidate
	approxBytes int64
}

var analyticsCache struct {
	sync.Mutex
	states map[analyticsKey]*analyticsState
	calls  map[callsKey]*callsEntry
}

var analyticsStripes [32]sync.Mutex

func stripeForKey(parts ...string) *sync.Mutex {
	var h uint32 = 2166136261
	for _, part := range parts {
		for i := range len(part) {
			h ^= uint32(part[i])
			h *= 16777619
		}
		h *= 16777619
	}
	return &analyticsStripes[h%32]
}
func (s *analyticsState) add(r Record, priceOf PriceLookup, renamed map[string]string, filter AnalyticsFilter, loc *time.Location) {
	if r.IsRejected() {
		return
	}
	if newP, ok := renamed[r.Provider]; ok {
		r.Provider = newP
	}
	t := r.Time.In(loc)
	if t.Before(s.since) {
		return
	}

	aid := AgentOf(r.Agent)
	if r.Model != "" {
		s.modelsSet[r.Model] = struct{}{}
	}
	if r.Provider != "" {
		s.provsSet[r.Provider] = struct{}{}
	}
	if aid != "" {
		s.agentsSet[aid] = struct{}{}
	}

	if filter.Model != "" && r.Model != filter.Model {
		return
	}
	if filter.Provider != "" && r.Provider != filter.Provider {
		return
	}
	if filter.Agent != "" && aid != filter.Agent {
		return
	}

	pr := priceOf(r.Provider, r.Model)
	s.summaryAcc.add(r, pr)

	bi := bucketIndex(s.bucket, s.since, t)
	if bi >= 0 && bi < len(s.errorTrend) {
		switch classifyStatus(r) {
		case statusRateLimited:
			s.errorTrend[bi].RateLimited++
		case statusServerErr:
			s.errorTrend[bi].ServerErr++
		case statusOtherErr:
			s.errorTrend[bi].OtherErr++
		}
	}

	if r.Model != "" {
		mAcc := s.modelAccs[r.Model]
		if mAcc == nil {
			mAcc = &entityAccumulator{}
			s.modelAccs[r.Model] = mAcc
		}
		mAcc.add(r, pr)
	}
	if r.Provider != "" {
		pAcc := s.provAccs[r.Provider]
		if pAcc == nil {
			pAcc = &entityAccumulator{}
			s.provAccs[r.Provider] = pAcc
		}
		pAcc.add(r, pr)
	}
	if aid != "" {
		aAcc := s.agentAccs[aid]
		if aAcc == nil {
			aAcc = &entityAccumulator{}
			s.agentAccs[aid] = aAcc
		}
		aAcc.add(r, pr)
	}
}

func cloneFloat(f *float64) *float64 {
	if f == nil {
		return nil
	}
	v := *f
	return &v
}

func cloneInt64(i *int64) *int64 {
	if i == nil {
		return nil
	}
	v := *i
	return &v
}

func cloneAnalyticsSummary(s AnalyticsSummary) AnalyticsSummary {
	out := s
	out.SuccessRate = cloneFloat(s.SuccessRate)
	out.ErrorRate = cloneFloat(s.ErrorRate)
	out.CancelRate = cloneFloat(s.CancelRate)
	out.TTFTP50 = cloneInt64(s.TTFTP50)
	out.TTFTP95 = cloneInt64(s.TTFTP95)
	out.Speed = cloneFloat(s.Speed)
	out.CacheHitRate = cloneFloat(s.CacheHitRate)
	return out
}

func cloneRankItem(item RankItem) RankItem {
	out := item
	out.MetricVal = cloneFloat(item.MetricVal)
	return out
}

func cloneRankSlice(items []RankItem) []RankItem {
	if items == nil {
		return nil
	}
	out := make([]RankItem, len(items))
	for i, item := range items {
		out[i] = cloneRankItem(item)
	}
	return out
}

func (s *analyticsState) cloneResponse() AnalyticsData {
	out := s.response
	out.Summary = cloneAnalyticsSummary(s.response.Summary)
	out.ErrorTrend = slices.Clone(s.response.ErrorTrend)
	out.Filters.Model = slices.Clone(s.response.Filters.Model)
	out.Filters.Provider = slices.Clone(s.response.Filters.Provider)
	out.Filters.Agent = slices.Clone(s.response.Filters.Agent)
	out.Rankings = make(map[string]RankingSuite, len(s.response.Rankings))
	for k, suite := range s.response.Rankings {
		clonedSummaries := make(map[string]AnalyticsSummary, len(suite.Summaries))
		for sk, sum := range suite.Summaries {
			clonedSummaries[sk] = cloneAnalyticsSummary(sum)
		}
		out.Rankings[k] = RankingSuite{
			Summaries:   clonedSummaries,
			ByErrorRate: cloneRankSlice(suite.ByErrorRate),
			ByTTFT:      cloneRankSlice(suite.ByTTFT),
			BySpeed:     cloneRankSlice(suite.BySpeed),
			ByCost:      cloneRankSlice(suite.ByCost),
			ByCacheRate: cloneRankSlice(suite.ByCacheRate),
		}
	}
	return out
}

func roundAndClampTTFT(val float64) int64 {
	if math.IsNaN(val) || val <= 0 {
		return 0
	}
	if val >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	r := math.Round(val)
	if r >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(r)
}

func sketchNearestRank(s *ddsketch.DDSketch, q float64) int64 {
	if s == nil || s.IsEmpty() {
		return 0
	}
	n := s.GetCount()
	if n <= 0 {
		return 0
	}
	if n == 1 {
		val, err := s.GetValueAtQuantile(0)
		if err != nil {
			return 0
		}
		return roundAndClampTTFT(val)
	}
	k := math.Ceil(q*n) - 1
	if k < 0 {
		k = 0
	}
	if k >= n {
		k = n - 1
	}
	targetQ := math.Min(1.0, (k+0.5)/(n-1))
	val, err := s.GetValueAtQuantile(targetQ)
	if err != nil {
		return 0
	}
	return roundAndClampTTFT(val)
}

// CallWithCost is a Record with an attached nullable cost and optional speed for /api/analytics/calls.
type CallWithCost struct {
	Record
	Cost  *float64 `json:"cost"`
	Speed *float64 `json:"speed,omitempty"`
}

func callSpeed(r Record) (float64, bool) {
	if r.Failed() {
		return 0, false
	}
	out, w := r.Decode()
	if w <= 0 {
		return 0, false
	}
	return float64(out) / (float64(w) / 1000.0), true
}

func callCost(r Record, priceOf PriceLookup) (float64, bool) {
	if r.Input+r.Output <= 0 {
		return 0, false
	}
	pr := priceOf(r.Provider, r.Model)
	if pr == nil {
		return 0, false
	}
	return pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite), true
}

type statusKind int

const (
	statusOK statusKind = iota
	statusCanceled
	statusRateLimited
	statusServerErr
	statusOtherErr
)

func classifyStatus(r Record) statusKind {
	if r.Status == 499 {
		return statusCanceled
	}
	if r.Status == 429 {
		return statusRateLimited
	}
	if r.Status >= 500 {
		return statusServerErr
	}
	if r.Status >= 400 || r.Failed() {
		return statusOtherErr
	}
	return statusOK
}

func earliestRecordTime(recs []Record) time.Time {
	var first time.Time
	for _, r := range recs {
		if !r.IsRejected() && (first.IsZero() || r.Time.Before(first)) {
			first = r.Time
		}
	}
	return first
}

func matchCallsCandidate(r Record, order int64, chartID string, priceOf PriceLookup) (callsCandidate, bool) {
	var cand callsCandidate
	cand.rec = r
	cand.order = order
	matched := false

	switch chartID {
	case "error_rate":
		if r.Failed() && r.Status != 499 {
			matched = true
		}
	case "ttft":
		if !r.Failed() && r.TTFT > 0 {
			matched = true
		}
	case "speed":
		if spd, ok := callSpeed(r); ok {
			cand.speed = spd
			matched = true
		}
	case "cost":
		matched = true
		if cost, ok := callCost(r, priceOf); ok {
			cand.cost = cost
			cand.costKnown = true
		}
	case "cache_hit_rate":
		matched = true
	}
	return cand, matched
}

// normalizeChartID validates semantic chart identifiers:
// "error_rate", "ttft", "speed", "cost", "cache_hit_rate".
func normalizeChartID(id string) (string, error) {
	switch id {
	case "error_rate", "ttft", "speed", "cost", "cache_hit_rate":
		return id, nil
	default:
		return "", ErrInvalidChart
	}
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

// entityAccumulator aggregates calls for a single dimension entity or summary.
type entityAccumulator struct {
	totals      Totals
	rateLimited int // 429
	serverErr   int // 5xx
	otherErr    int // other HTTP or streamed errors
	canceled    int // 499
	hasUnpriced bool
	decodeCalls int
	sketch      *ddsketch.DDSketch
}

func (ea *entityAccumulator) add(r Record, pr *catalog.Price) {
	ea.totals.add(r, pr)

	if r.Input+r.Output > 0 && pr == nil {
		ea.hasUnpriced = true
	}

	switch classifyStatus(r) {
	case statusCanceled:
		ea.canceled++
	case statusRateLimited:
		ea.rateLimited++
	case statusServerErr:
		ea.serverErr++
	case statusOtherErr:
		ea.otherErr++
	}
	// Streamed TTFT samples and decode call count
	if r.TTFT > 0 && !r.Failed() {
		if ea.sketch == nil {
			ea.sketch, _ = ddsketch.LogCollapsingLowestDenseDDSketch(0.01, 4096)
		}
		if ea.sketch != nil {
			_ = ea.sketch.Add(float64(r.TTFT))
		}
		if _, w := r.Decode(); w > 0 {
			ea.decodeCalls++
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
		DecodeCalls: ea.decodeCalls,
	}

	// Success & Error rates (calls == 0 => null; error_rate excludes 499)
	if ea.totals.Calls > 0 {
		succRate := float64(ea.totals.Calls-ea.totals.Errors) / float64(ea.totals.Calls)
		s.SuccessRate = &succRate
		errCount := ea.rateLimited + ea.serverErr + ea.otherErr
		errRate := float64(errCount) / float64(ea.totals.Calls)
		s.ErrorRate = &errRate
		cRate := float64(ea.canceled) / float64(ea.totals.Calls)
		s.CancelRate = &cRate
	}

	// TTFT quantiles via DDSketch nearest-rank adapter (timed == 0 => null)
	if ea.totals.Timed > 0 && ea.sketch != nil && !ea.sketch.IsEmpty() {
		p50 := sketchNearestRank(ea.sketch, 0.50)
		p95 := sketchNearestRank(ea.sketch, 0.95)
		s.TTFTP50 = &p50
		s.TTFTP95 = &p95
	}

	// Speed aligned with Totals.Speed (decode_calls == 0 or DecodeMs <= 0 => null)
	if ea.decodeCalls > 0 && ea.totals.DecodeMs > 0 {
		spd := ea.totals.Speed()
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

// Analyze analyzes usage records for a period with filters as of now.
func Analyze(p Period, filter AnalyticsFilter) AnalyticsData {
	return analyzeIndexed(p, filter)
}

// RecentCalls retrieves and filters up to limit (max 50) records for a specific chart.
func RecentCalls(p Period, filter AnalyticsFilter, chartID string, limit int) ([]CallWithCost, error) {
	return recentCallsIndexed(p, filter, chartID, limit)
}

// analyzeIndexed implements Analyze using cached snapshot state and incremental visitAfter.
func analyzeIndexed(p Period, filter AnalyticsFilter) AnalyticsData {
	p = canonicalPeriod(p)
	key := analyticsKey{Period: p, Model: filter.Model, Provider: filter.Provider, Agent: filter.Agent}
	stripe := stripeForKey(string(p), filter.Model, filter.Provider, filter.Agent)
	stripe.Lock()
	defer stripe.Unlock()
	now := time.Now()
	meta := dependencySignature(now)
	snapshot := logSnapshotFor(true)

	analyticsCache.Lock()
	old := analyticsCache.states[key]
	analyticsCache.Unlock()
	// 1. Exact Hit: same version, same epoch, same meta -> return cached immediately without reloading blocks
	if old != nil && old.epoch == snapshot.epoch && old.version == snapshot.version && old.meta == meta {
		return old.cloneResponse()
	}

	// Need full blocks for delta append or rebuild
	if snapshot.uncached && len(snapshot.blocks) == 0 {
		snapshot = readLogSnapshot()
	}
	priceOf := defaultPriceLookup()
	renamed := provider.Renamed()

	// If snapshot is still uncached (ledger exceeds requestCacheBytes):
	// We stream accumulate without creating a full ledger slice, and we STILL cache the resulting state
	// so subsequent calls with no new data hit the cache!
	if snapshot.uncached {
		state := buildNewAnalyticsState(p, filter, now, meta, snapshot, priceOf, renamed)
		publishAnalyticsState(key, state, snapshot.version)
		return state.cloneResponse()
	}

	// renamed already initialized above

	// Check if we can do an incremental delta update:
	// Must be same epoch, old.cursor > 0, same meta, and for Period All the timeline layout must not change!
	canAppend := old != nil && old.epoch == snapshot.epoch && old.meta == meta && old.cursor > 0
	if canAppend && p == All {
		// Verify if the earliest timestamp could have moved backward
		newFirst := snapshot.first
		if !newFirst.IsZero() && !old.first.IsZero() && newFirst.Before(old.first) {
			canAppend = false
		} else {
			since, bucket, _ := timeline(p, now, snapshot.first)
			if !since.Equal(old.since) || bucket != old.bucket {
				canAppend = false
			}
		}
	}

	if canAppend {
		// Delta append
		state := copyAnalyticsState(old)
		snapshot.visitAfter(state.cursor, state.since, func(r Record, rowOrder int64) {
			state.add(r, priceOf, renamed, filter, now.Location())
		})

		state.cursor = snapshot.off
		state.version = snapshot.version
		state.first = snapshot.first
		state.response = buildAnalyticsResponse(p, state)
		state.approxBytes = estimateAnalyticsStateBytes(state)

		publishAnalyticsState(key, state, snapshot.version)
		return state.cloneResponse()
	}

	// Full rebuild from snapshot
	state := buildNewAnalyticsState(p, filter, now, meta, snapshot, priceOf, renamed)
	publishAnalyticsState(key, state, snapshot.version)
	return state.cloneResponse()
}

func copyAnalyticsState(src *analyticsState) *analyticsState {
	dst := &analyticsState{
		epoch:      src.epoch,
		version:    src.version,
		cursor:     src.cursor,
		meta:       src.meta,
		since:      src.since,
		bucket:     src.bucket,
		first:      src.first,
		summaryAcc: copyAccumulator(src.summaryAcc),
		modelAccs:  make(map[string]*entityAccumulator, len(src.modelAccs)),
		provAccs:   make(map[string]*entityAccumulator, len(src.provAccs)),
		agentAccs:  make(map[string]*entityAccumulator, len(src.agentAccs)),
		errorTrend: slices.Clone(src.errorTrend),
		modelsSet:  make(map[string]struct{}, len(src.modelsSet)),
		provsSet:   make(map[string]struct{}, len(src.provsSet)),
		agentsSet:  make(map[string]struct{}, len(src.agentsSet)),
	}
	for k, v := range src.modelAccs {
		dst.modelAccs[k] = copyAccumulator(v)
	}
	for k, v := range src.provAccs {
		dst.provAccs[k] = copyAccumulator(v)
	}
	for k, v := range src.agentAccs {
		dst.agentAccs[k] = copyAccumulator(v)
	}
	for k := range src.modelsSet {
		dst.modelsSet[k] = struct{}{}
	}
	for k := range src.provsSet {
		dst.provsSet[k] = struct{}{}
	}
	for k := range src.agentsSet {
		dst.agentsSet[k] = struct{}{}
	}
	return dst
}

func copyAccumulator(ea *entityAccumulator) *entityAccumulator {
	if ea == nil {
		return &entityAccumulator{}
	}
	cp := *ea
	if ea.sketch != nil {
		cp.sketch = ea.sketch.Copy()
	}
	return &cp
}

func buildNewAnalyticsState(p Period, filter AnalyticsFilter, now time.Time, meta string, snapshot *logSnapshot, priceOf PriceLookup, renamed map[string]string) *analyticsState {
	since, bucket, pts := timeline(p, now, snapshot.first)
	state := &analyticsState{
		epoch:      snapshot.epoch,
		version:    snapshot.version,
		meta:       meta,
		since:      since,
		bucket:     bucket,
		first:      snapshot.first,
		summaryAcc: &entityAccumulator{},
		modelAccs:  map[string]*entityAccumulator{},
		provAccs:   map[string]*entityAccumulator{},
		agentAccs:  map[string]*entityAccumulator{},
		errorTrend: make([]TrendPoint, len(pts)),
		modelsSet:  map[string]struct{}{},
		provsSet:   map[string]struct{}{},
		agentsSet:  map[string]struct{}{},
	}
	for i, pt := range pts {
		state.errorTrend[i] = TrendPoint{Label: pt.Label, Time: pt.Time}
	}

	snapshot.visitAfter(0, since, func(r Record, rowOrder int64) {
		state.add(r, priceOf, renamed, filter, now.Location())
	})

	state.cursor = snapshot.off
	state.response = buildAnalyticsResponse(p, state)
	state.approxBytes = estimateAnalyticsStateBytes(state)
	return state
}

func buildAnalyticsResponse(p Period, state *analyticsState) AnalyticsData {
	out := AnalyticsData{
		Period:     p,
		Since:      state.since,
		Bucket:     state.bucket,
		Rankings:   map[string]RankingSuite{},
		ErrorTrend: slices.Clone(state.errorTrend),
		Filters: FilterOptions{
			Model:    make([]string, 0, len(state.modelsSet)),
			Provider: make([]string, 0, len(state.provsSet)),
			Agent:    make([]string, 0, len(state.agentsSet)),
		},
	}
	for m := range state.modelsSet {
		out.Filters.Model = append(out.Filters.Model, m)
	}
	sort.Strings(out.Filters.Model)
	for pr := range state.provsSet {
		out.Filters.Provider = append(out.Filters.Provider, pr)
	}
	sort.Strings(out.Filters.Provider)
	for a := range state.agentsSet {
		out.Filters.Agent = append(out.Filters.Agent, a)
	}
	sort.Strings(out.Filters.Agent)

	out.Summary = state.summaryAcc.toSummary()
	totalCost := out.Summary.Cost

	out.Rankings["model"] = buildRankingSuite(state.modelAccs, totalCost)
	out.Rankings["provider"] = buildRankingSuite(state.provAccs, totalCost)
	out.Rankings["agent"] = buildRankingSuite(state.agentAccs, totalCost)

	return out
}

func estimateAnalyticsStateBytes(s *analyticsState) int64 {
	if s == nil {
		return 0
	}
	// Conservative memory accounting:
	// Base state struct + meta + bucket strings
	var b int64 = 1024 + int64(len(s.meta)+len(s.bucket))

	// Accumulator memory: Totals + DDSketch structures + mappings + bins
	accSize := func(ea *entityAccumulator) int64 {
		if ea == nil {
			return 0
		}
		sz := int64(256)
		if ea.sketch != nil {
			// CollapsingLowestDenseStore with max 4096 bins (each bin is float64 = 8 bytes) -> 32KB bins
			// plus indexMapping, store struct, protobuf builders, heap overhead (~36KB conservative)
			sz += 36864
		}
		return sz
	}

	b += accSize(s.summaryAcc)
	for k, ea := range s.modelAccs {
		b += int64(len(k)+64) + accSize(ea)
	}
	for k, ea := range s.provAccs {
		b += int64(len(k)+64) + accSize(ea)
	}
	for k, ea := range s.agentAccs {
		b += int64(len(k)+64) + accSize(ea)
	}

	// Filter options and sets
	for k := range s.modelsSet {
		b += int64(len(k) + 48)
	}
	for k := range s.provsSet {
		b += int64(len(k) + 48)
	}
	for k := range s.agentsSet {
		b += int64(len(k) + 48)
	}

	// Error trend: TrendPoint slice
	b += int64(cap(s.errorTrend)+cap(s.response.ErrorTrend)) * int64(unsafe.Sizeof(TrendPoint{}))
	for _, points := range [][]TrendPoint{s.errorTrend, s.response.ErrorTrend} {
		for _, point := range points {
			b += int64(len(point.Label))
		}
	}
	b += int64(cap(s.response.Filters.Model)+cap(s.response.Filters.Provider)+cap(s.response.Filters.Agent)) * 16

	// Final response rankings: Summaries map + RankItem structs and strings
	for _, suite := range s.response.Rankings {
		for k, sum := range suite.Summaries {
			b += int64(len(k) + 64)
			b += int64(unsafe.Sizeof(AnalyticsSummary{}))
			if sum.SuccessRate != nil {
				b += 8
			}
			if sum.ErrorRate != nil {
				b += 8
			}
			if sum.TTFTP50 != nil {
				b += 8
			}
			if sum.TTFTP95 != nil {
				b += 8
			}
			if sum.Speed != nil {
				b += 8
			}
			if sum.CacheHitRate != nil {
				b += 8
			}
		}
		rankSize := func(items []RankItem) int64 {
			var total int64 = int64(cap(items)) * int64(unsafe.Sizeof(RankItem{}))
			for _, it := range items {
				total += int64(len(it.Key) + 32)
			}
			return total
		}
		b += rankSize(suite.ByErrorRate)
		b += rankSize(suite.ByTTFT)
		b += rankSize(suite.BySpeed)
		b += rankSize(suite.ByCost)
		b += rankSize(suite.ByCacheRate)
	}
	return b
}

func publishAnalyticsState(key analyticsKey, state *analyticsState, snapshotVersion uint64) {
	analyticsCache.Lock()
	defer analyticsCache.Unlock()
	if analyticsCache.states == nil {
		analyticsCache.states = map[analyticsKey]*analyticsState{}
	}
	if prev := analyticsCache.states[key]; prev != nil && prev.version > snapshotVersion {
		return // Do not let an older slow query overwrite newer state
	}
	analyticsCache.states[key] = state
	enforceAnalyticsBudget()
}

func enforceAnalyticsBudget() {
	limit := analyticsBudget()
	var retained int64
	for _, state := range analyticsCache.states {
		retained += state.approxBytes
	}
	for _, calls := range analyticsCache.calls {
		retained += calls.approxBytes
	}
	for key, state := range analyticsCache.states {
		if retained <= limit && len(analyticsCache.states) <= 16 {
			break
		}
		delete(analyticsCache.states, key)
		retained -= state.approxBytes
	}
	for key, calls := range analyticsCache.calls {
		if retained <= limit && len(analyticsCache.calls) <= 32 {
			break
		}
		delete(analyticsCache.calls, key)
		retained -= calls.approxBytes
	}
}

// resetAnalyticsCache clears the analytics and calls caches (used in tests).
func resetAnalyticsCache() {
	analyticsCache.Lock()
	analyticsCache.states = map[analyticsKey]*analyticsState{}
	analyticsCache.calls = map[callsKey]*callsEntry{}
	analyticsCache.Unlock()
}

func candidateLess(chartID string, a, b callsCandidate) bool {
	// Returns true if a should be popped before b (min-heap root is poorest candidate).
	// Thus, return true if b is BETTER than a.
	// Primary comparison: chart criteria. Tie-breaker: time DESC (newer is better).
	// Stable exact tie-breaker: order ASC (earlier file/input order is preserved by stable sort).
	isBetter := func(better, worse callsCandidate) bool {
		switch chartID {
		case "error_rate":
			if !better.rec.Time.Equal(worse.rec.Time) {
				return better.rec.Time.After(worse.rec.Time)
			}
			return better.order < worse.order
		case "ttft":
			if better.rec.TTFT != worse.rec.TTFT {
				return better.rec.TTFT > worse.rec.TTFT
			}
			if !better.rec.Time.Equal(worse.rec.Time) {
				return better.rec.Time.After(worse.rec.Time)
			}
			return better.order < worse.order
		case "speed":
			// Speed ASC: lower speed is better
			if better.speed != worse.speed {
				return better.speed < worse.speed
			}
			if !better.rec.Time.Equal(worse.rec.Time) {
				return better.rec.Time.After(worse.rec.Time)
			}
			return better.order < worse.order
		case "cost":
			// Cost DESC, known before unknown
			if better.costKnown != worse.costKnown {
				return better.costKnown
			}
			if better.costKnown && better.cost != worse.cost {
				return better.cost > worse.cost
			}
			if !better.rec.Time.Equal(worse.rec.Time) {
				return better.rec.Time.After(worse.rec.Time)
			}
			return better.order < worse.order
		case "cache_hit_rate":
			// Cache hit rate drilldown: Input prompt tokens DESC
			if better.rec.Input != worse.rec.Input {
				return better.rec.Input > worse.rec.Input
			}
			if !better.rec.Time.Equal(worse.rec.Time) {
				return better.rec.Time.After(worse.rec.Time)
			}
			return better.order < worse.order
		default:
			return better.order < worse.order
		}
	}
	return isBetter(b, a)
}

func candidateBetter(chartID string, a, b callsCandidate) bool {
	return candidateLess(chartID, b, a)
}

func recentCallsIndexed(p Period, filter AnalyticsFilter, chartID string, limit int) ([]CallWithCost, error) {
	normID, err := normalizeChartID(chartID)
	if err != nil {
		return nil, err
	}
	chartID = normID
	if limit <= 0 || limit > 50 {
		limit = 50
	}

	p = canonicalPeriod(p)

	key := callsKey{
		Period:   p,
		Model:    filter.Model,
		Provider: filter.Provider,
		Agent:    filter.Agent,
		ChartID:  chartID,
	}
	stripe := stripeForKey(string(p), filter.Model, filter.Provider, filter.Agent, chartID)
	stripe.Lock()
	defer stripe.Unlock()
	now := time.Now()
	meta := dependencySignature(now)
	snapshot := logSnapshotFor(true)

	analyticsCache.Lock()
	old := analyticsCache.calls[key]
	analyticsCache.Unlock()
	if old != nil && old.epoch == snapshot.epoch && old.version == snapshot.version && old.meta == meta {
		return formatCallsResponse(old.top50, limit), nil
	}

	if snapshot.uncached && len(snapshot.blocks) == 0 {
		snapshot = readLogSnapshot()
	}
	priceOf := defaultPriceLookup()

	renamed := provider.Renamed()
	canAppend := old != nil && old.epoch == snapshot.epoch && old.meta == meta && old.cursor > 0
	since, _, _ := timeline(p, now, snapshot.first)
	if canAppend && p == All {
		if !snapshot.first.IsZero() && !old.first.IsZero() && snapshot.first.Before(old.first) {
			canAppend = false
		}
	}

	lessFn := func(a, b callsCandidate) bool {
		return candidateLess(chartID, a, b)
	}

	h := &topKHeap[callsCandidate]{less: lessFn}
	var startOrder int64

	if canAppend {
		for _, c := range old.top50 {
			heap.Push(h, c)
		}
		startOrder = old.cursor
	}

	snapshot.visitAfter(startOrder, since, func(r Record, rowOrder int64) {
		if r.IsRejected() {
			return
		}
		if newP, ok := renamed[r.Provider]; ok {
			r.Provider = newP
		}
		t := r.Time.In(now.Location())
		if t.Before(since) {
			return
		}

		aid := AgentOf(r.Agent)
		if filter.Model != "" && r.Model != filter.Model {
			return
		}
		if filter.Provider != "" && r.Provider != filter.Provider {
			return
		}
		if filter.Agent != "" && aid != filter.Agent {
			return
		}

		cand, matched := matchCallsCandidate(r, rowOrder, chartID, priceOf)
		if matched {
			if h.Len() < 50 {
				heap.Push(h, cand)
			} else if candidateBetter(chartID, cand, h.items[0]) {
				h.items[0] = cand
				heap.Fix(h, 0)
			}
		}
	})

	// Sort the top50 in descending quality order
	finalCandidates := make([]callsCandidate, h.Len())
	copy(finalCandidates, h.items)
	slices.SortFunc(finalCandidates, func(a, b callsCandidate) int {
		if candidateBetter(chartID, a, b) {
			return -1
		}
		if candidateBetter(chartID, b, a) {
			return 1
		}
		return 0
	})
	if chartID != "cost" {
		priceCallCandidates(finalCandidates, priceOf)
	}

	var candBytes int64 = int64(unsafe.Sizeof(callsEntry{})) + int64(len(meta)) + 256
	for _, c := range finalCandidates {
		candBytes += int64(unsafe.Sizeof(callsCandidate{}))
		r := c.rec
		candBytes += int64(len(r.Operation) + len(r.Agent) + len(r.Provider) + len(r.Model) + len(r.Host) + len(r.ProviderKeyID) + len(r.ProviderKeyName) + len(r.ProviderAccount) + len(r.CallerKeyID) + len(r.CallerKeyName) + len(r.SessionProvider) + len(r.SessionAccount) + len(r.Requested) + len(r.Served) + len(r.Effort) + len(r.Error) + len(r.ErrType) + len(r.RequestID) + len(r.ResponseID) + len(r.Endpoint) + len(r.Session) + len(r.NativeSession) + len(r.Kind) + len(r.Via) + len(r.Archive) + len(r.Computer))
	}
	entry := &callsEntry{
		epoch:       snapshot.epoch,
		version:     snapshot.version,
		cursor:      snapshot.off,
		meta:        meta,
		first:       snapshot.first,
		top50:       finalCandidates,
		approxBytes: candBytes,
	}
	publishCallsEntry(key, entry, snapshot.version)
	return formatCallsResponse(finalCandidates, limit), nil
}

func publishCallsEntry(key callsKey, entry *callsEntry, snapshotVersion uint64) {
	analyticsCache.Lock()
	defer analyticsCache.Unlock()
	if analyticsCache.calls == nil {
		analyticsCache.calls = map[callsKey]*callsEntry{}
	}
	if prev := analyticsCache.calls[key]; prev != nil && prev.version > snapshotVersion {
		return
	}
	analyticsCache.calls[key] = entry
	enforceAnalyticsBudget()
}

func priceCallCandidate(c *callsCandidate, priceOf PriceLookup) {
	if cost, ok := callCost(c.rec, priceOf); ok {
		c.cost = cost
		c.costKnown = true
	}
}

func priceCallCandidates(candidates []callsCandidate, priceOf PriceLookup) {
	for i := range candidates {
		priceCallCandidate(&candidates[i], priceOf)
	}
}

func formatCallsResponse(candidates []callsCandidate, limit int) []CallWithCost {
	n := min(limit, len(candidates))
	res := make([]CallWithCost, n)
	for i := range n {
		m := candidates[i]
		r := m.rec
		var costPtr *float64
		if m.costKnown {
			c := m.cost
			costPtr = &c
		}
		var speedPtr *float64
		if spd, ok := callSpeed(r); ok {
			speedPtr = &spd
		}
		res[i] = CallWithCost{
			Record: r,
			Cost:   costPtr,
			Speed:  speedPtr,
		}
	}
	return res
}

// analyzeWith is the internal deterministic engine for Analyze.
func analyzeWith(p Period, filter AnalyticsFilter, now time.Time, recs []Record, priceOf PriceLookup) AnalyticsData {
	first := earliestRecordTime(recs)
	since, bucket, pts := timeline(p, now, first)
	renamed := provider.Renamed()
	state := &analyticsState{
		since:      since,
		bucket:     bucket,
		first:      first,
		summaryAcc: &entityAccumulator{},
		modelAccs:  map[string]*entityAccumulator{},
		provAccs:   map[string]*entityAccumulator{},
		agentAccs:  map[string]*entityAccumulator{},
		errorTrend: make([]TrendPoint, len(pts)),
		modelsSet:  map[string]struct{}{},
		provsSet:   map[string]struct{}{},
		agentsSet:  map[string]struct{}{},
	}
	for i, pt := range pts {
		state.errorTrend[i] = TrendPoint{Label: pt.Label, Time: pt.Time}
	}
	for _, r := range recs {
		state.add(r, priceOf, renamed, filter, now.Location())
	}
	return buildAnalyticsResponse(p, state)
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
		Summaries:   make(map[string]AnalyticsSummary, len(accs)),
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
		suite.Summaries[k] = summary

		share := 0.0
		if totalCost > 0 && summary.Cost > 0 {
			share = summary.Cost / totalCost
		}

		baseItem := RankItem{
			Key: k,
		}

		// 1. ByErrorRate (threshold calls >= 10, error_rate DESC)
		itemErr := baseItem
		if summary.ErrorRate != nil {
			val := *summary.ErrorRate
			itemErr.MetricVal = &val
		}
		itemErr.Insufficient = summary.Calls < 10
		itemErr.Share = share
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
		itemCost.HasUnpriced = ea.hasUnpriced
		itemCost.Share = share
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

// recentCallsWith is the internal deterministic engine for RecentCalls.
func recentCallsWith(p Period, filter AnalyticsFilter, chartID string, limit int, now time.Time, recs []Record, priceOf PriceLookup) ([]CallWithCost, error) {
	normID, err := normalizeChartID(chartID)
	if err != nil {
		return nil, err
	}
	chartID = normID
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	renamed := provider.Renamed()
	first := earliestRecordTime(recs)
	since, _, _ := timeline(p, now, first)
	lessFn := func(a, b callsCandidate) bool {
		return candidateLess(chartID, a, b)
	}
	h := &topKHeap[callsCandidate]{less: lessFn}
	for i, r := range recs {
		if r.IsRejected() {
			continue
		}
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
		cand, matched := matchCallsCandidate(r, int64(i), chartID, priceOf)
		if matched {
			if h.Len() < 50 {
				heap.Push(h, cand)
			} else if candidateBetter(chartID, cand, h.items[0]) {
				h.items[0] = cand
				heap.Fix(h, 0)
			}
		}
	}
	finalCandidates := make([]callsCandidate, h.Len())
	copy(finalCandidates, h.items)
	slices.SortFunc(finalCandidates, func(a, b callsCandidate) int {
		if candidateBetter(chartID, a, b) {
			return -1
		}
		if candidateBetter(chartID, b, a) {
			return 1
		}
		return 0
	})
	if chartID != "cost" {
		priceCallCandidates(finalCandidates, priceOf)
	}
	return formatCallsResponse(finalCandidates, limit), nil
}
