package provider

// What a window's own history says about the cycle it is in: whether what is
// left lasts to the reset, at what hourly rate it is going, and where it
// would end up. Read on the way out of QuotaHistories, from the points
// already kept — nothing here is stored, and the points a reading records,
// and their sync between computers, are untouched.
//
// Two layers answer it, and the tighter one wins: an even burn — what is
// left spread over what is left of the cycle, the only layer a short window
// or a young one can use — and, on a window of two days or more, the shape
// of the cycles already spent (see histVerdictOf). A layer that says the
// window runs out is never overruled by one that says it lasts: the answer
// is the soonest running-out time, and the lowest projection at the reset.

import (
	"math"
	"sort"
	"time"
)

// QuotaForecast is how the current cycle is going. State is "ok" when it is
// forecast, "none" when the cycle is too young or too little is spent to say,
// and "spent" when nothing is left. Source names the layers that answered:
// "even" when only the even burn did, "history" when the past cycles were
// used too. Left, EvenLeft and Ahead compare this cycle's own rate with an
// even one; RatePerHour is the current cycle's average. LastsToReset is the
// conservative verdict, EtaSeconds how long from the query until projected empty when it does not
// last (0 otherwise), LeftAtReset the projected percent left at the reset
// (negative when it runs out), and Headroom the merged zero-crossing over
// the time to the reset: 0 when a layer runs out first; when it lasts, at
// least 1, and a layer that never empties is counted only at the 10-window
// cap (forecastEtaCap). Cycles counts the completed cycles the history layer
// used. AsOf is the latest observation: rates and verdicts stay anchored to
// that reading, never assuming no consumption in an unobserved interval.
// RunsOutAt fixes the projection in time; EtaSeconds counts down from it and
// stays at 0 after it passes, without claiming the observed state is spent.
// All figures are percentages of the window.
type QuotaForecast struct {
	State        string     `json:"state"`
	Source       string     `json:"source"`
	Left         float64    `json:"left"`
	EvenLeft     float64    `json:"evenLeft"`
	Ahead        float64    `json:"ahead"`
	RatePerHour  float64    `json:"ratePerHour"`
	LastsToReset bool       `json:"lastsToReset"`
	EtaSeconds   float64    `json:"etaSeconds"`
	LeftAtReset  float64    `json:"leftAtReset"`
	Headroom     float64    `json:"headroom"`
	Cycles       int        `json:"cycles"`
	CycleStart   time.Time  `json:"cycleStart"`
	ResetsAt     time.Time  `json:"resetsAt"`
	AsOf         time.Time  `json:"asOf"`
	RunsOutAt    *time.Time `json:"runsOutAt,omitempty"`
}

const (
	// a cycle needs more than one reading
	forecastMinPoints = 2
	// Young cycles wait unless enough was consumed after 30 minutes and the
	// even-burn crossing is in the first half of the time left to reset.
	forecastMinElapsed         = 0.08
	forecastEarlyMinElapsed    = 30 * time.Minute
	forecastEarlyMinUsed       = 10
	forecastEarlyMaxResetShare = 0.5
	forecastNothingUsed        = 99.5
	// at or under this, the window is spent
	forecastSpentLeft = 0.5
	// past cycles are used only on a window of two days or more: a 5-hour
	// window's shape says nothing about the next five hours
	forecastHistoryFrom = 48 * time.Hour
	// a past cycle is usable with this many readings, seen this near both its
	// start and its reset, and having spent this much
	forecastHistMinPoints = 6
	forecastHistCoverage  = 24 * time.Hour
	forecastHistMinUsed   = 5
	forecastHistMinCycles = 3
	// the shape is sampled on this many steps of the cycle
	forecastHistGridK = 48
	// a shape this small would project an absurd total: guard the division
	forecastHistMinShape = 0.01
	// a layer that never empties within this many windows counts as that far
	// off and no further, for the headroom
	forecastEtaCap = 10
)

// forecastRound is a figure as the JSON gives it: two decimals, and no NaN or
// infinity (a JSON number can be neither).
func forecastRound(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*100) / 100
}

func (f *QuotaForecast) round() {
	f.Left = forecastRound(f.Left)
	f.EvenLeft = forecastRound(f.EvenLeft)
	f.Ahead = forecastRound(f.Ahead)
	f.RatePerHour = forecastRound(f.RatePerHour)
	f.EtaSeconds = forecastRound(f.EtaSeconds)
	f.LeftAtReset = forecastRound(f.LeftAtReset)
	f.Headroom = forecastRound(f.Headroom)
}

// quotaForecast is the forecast for one window's points, read at now. ok is
// false when there is nothing to say: no cycle, one too young to split, no
// reset ahead, no reading carrying the cycle's start, or no span to place the
// cycle in.
func quotaForecast(name string, points []QuotaPoint, now time.Time) (QuotaForecast, bool) {
	cycles := quotaCycles(points)
	if len(cycles) == 0 {
		return QuotaForecast{}, false
	}
	last := cycles[len(cycles)-1]
	cur := points[last.lo:last.hi]
	if len(cur) < forecastMinPoints {
		return QuotaForecast{}, false
	}
	start, ok := cycleStartOf(cur)
	if !ok {
		return QuotaForecast{}, false
	}
	span := windowSpan(QuotaWindow{Name: name})
	reset, ok := cycleResetOf(cur, start, span)
	if !ok || !reset.After(now) {
		return QuotaForecast{}, false
	}
	dur := reset.Sub(start)
	left := cur[len(cur)-1].Left
	if dur <= 0 || math.IsNaN(left) || math.IsInf(left, 0) {
		return QuotaForecast{}, false
	}
	// A window with no limit is never recorded (pointOf drops it), so a line
	// here is always a finite one.
	asOf := cur[len(cur)-1].At
	if asOf.After(now) || asOf.Before(start) {
		return QuotaForecast{}, false
	}
	elapsed := asOf.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	} else if elapsed > dur {
		elapsed = dur
	}
	toReset := reset.Sub(asOf)
	u := elapsed.Seconds() / dur.Seconds()
	even := evenLayer(left, u, elapsed.Hours(), toReset.Hours())

	f := QuotaForecast{
		State: "ok", Source: "even",
		Left: left, EvenLeft: even.left, Ahead: left - even.left, RatePerHour: even.rate,
		LastsToReset: even.lasts, LeftAtReset: even.leftAtReset,
		CycleStart: start, ResetsAt: reset, AsOf: asOf,
	}
	switch {
	case left <= forecastSpentLeft:
		f.State, f.Source, f.Cycles = "spent", "even", 0
		f.LastsToReset, f.EtaSeconds, f.Headroom = false, 0, 0
	case left >= forecastNothingUsed || (u < forecastMinElapsed &&
		(elapsed < forecastEarlyMinElapsed || 100-left < forecastEarlyMinUsed ||
			even.lasts || even.etaHours > forecastEarlyMaxResetShare*toReset.Hours())):
		// Wait on young cycles, except a clear early run-out. Nothing used
		// yet gives no verdict even with many observations.
		// state carries the "no verdict", so nothing here claims it lasts.
		f.State, f.Source, f.Cycles = "none", "even", 0
		f.LastsToReset, f.EtaSeconds, f.LeftAtReset, f.Headroom = false, 0, 0, 0
	default:
		hist, hasHist := histVerdictOf(points, cycles, span, dur, left, u, asOf)
		if hasHist {
			f.Source, f.Cycles = "history", hist.cycles
			f.LastsToReset = even.lasts && hist.lasts
			f.LeftAtReset = math.Min(even.leftAtReset, hist.leftAtReset)
		}
		// The blend: the answer is the tightest of the layers, so a warning
		// from one is never lost to an optimistic other. The running-out time
		// is the soonest crossing of a layer that runs out.
		for _, eta := range []float64{etaOrZero(even.lasts, even.etaHours), etaOrZero(!hasHist || hist.lasts, hist.etaHours)} {
			if eta > 0 && (f.EtaSeconds == 0 || eta < f.EtaSeconds) {
				f.EtaSeconds = eta
			}
		}
		if f.EtaSeconds > 0 {
			runsOut := asOf.Add(time.Duration(f.EtaSeconds * float64(time.Hour)))
			f.RunsOutAt = &runsOut
			f.EtaSeconds = math.Max(0, runsOut.Sub(now).Seconds())
		}
		// The headroom is the merged projection's own crossing over the time
		// to the reset: the same soonest crossing, a layer that never empties
		// counted only as far off as the cap. It is at least 1 when every layer
		// lasts — above it unless one would empty exactly at the reset — and 0
		// when one runs out first.
		capHours := forecastEtaCap * dur.Hours()
		merged := math.Min(even.etaHours, capHours)
		if hasHist {
			merged = math.Min(merged, math.Min(hist.etaHours, capHours))
		}
		if f.LastsToReset && toReset.Seconds() > 0 {
			f.Headroom = merged * 3600 / toReset.Seconds()
		}
	}
	f.round()
	return f, true
}

// etaOrZero is a layer's hours to empty when it runs out, 0 when it lasts.
func etaOrZero(lasts bool, etaHours float64) float64 {
	if lasts {
		return 0
	}
	return etaHours
}

// cycleRange is one cycle's span in a line's points: [lo, hi). Cycles are
// handed around as ranges so the points are indexed in place, never copied.
type cycleRange struct{ lo, hi int }

// quotaCycles splits points, oldest first, at every window started again
// (see newCycle).
func quotaCycles(points []QuotaPoint) []cycleRange {
	var out []cycleRange
	for i, p := range points {
		if i == 0 || newCycle(points[i-1], p) {
			out = append(out, cycleRange{lo: i, hi: i + 1})
			continue
		}
		out[len(out)-1].hi = i + 1
	}
	return out
}

// cycleStartOf is when a cycle's window began, as its own readings say. ok is
// false when none carries a start: the first observation is not the window's
// start, and taking it for one invents the elapsed time and makes the verdict
// optimistically wrong.
func cycleStartOf(c []QuotaPoint) (time.Time, bool) {
	for _, p := range c {
		if p.Start != nil {
			return *p.Start, true
		}
	}
	return time.Time{}, false
}

// cycleResetOf is when a cycle's window starts again: the last reading's
// ResetsAt, or the start plus the window's span when the vendor didn't say.
func cycleResetOf(c []QuotaPoint, start time.Time, span time.Duration) (time.Time, bool) {
	for i := len(c) - 1; i >= 0; i-- {
		if c[i].ResetsAt != nil {
			return *c[i].ResetsAt, true
		}
	}
	if span > 0 {
		return start.Add(span), true
	}
	return time.Time{}, false
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// evenBurn is what spending the cycle evenly says: what should be left by
// now, at what hourly rate the cycle is being spent, and when it would empty.
type evenBurn struct {
	left        float64 // % left now, had the cycle been spent evenly
	rate        float64 // % of the window per hour, this cycle's own average
	leftAtReset float64 // % left at the reset on that rate (may be negative)
	lasts       bool
	etaHours    float64 // hours to empty at that rate, +Inf when nothing is spent
}

func evenLayer(left, u, elapsedHours, toResetHours float64) evenBurn {
	e := evenBurn{left: 100 * (1 - u), etaHours: math.Inf(1)}
	if elapsedHours > 0 {
		e.rate = (100 - left) / elapsedHours
	}
	if e.rate > 0 {
		e.etaHours = left / e.rate
	}
	e.leftAtReset = left - e.rate*toResetHours
	e.lasts = e.etaHours >= toResetHours
	return e
}

// histVerdict is the history layer's answer for the current cycle.
type histVerdict struct {
	leftAtReset float64 // % left at the reset, projected from the past cycles
	lasts       bool
	etaHours    float64 // hours to empty, +Inf when the cycle does not run out
	cycles      int     // completed cycles used
}

// forecastScratch is the memory one forecast reuses as it looks at a line's
// cycles, so a polled read doesn't allocate per cycle or per crossing sample.
type forecastScratch struct {
	us      []float64 // one past cycle's polyline, as it is built
	vs      []float64 // ...and its used percent at each of those points
	curves  []float64 // every past cycle's grid, back to back
	values  []float64 // one ratio per past cycle, at one crossing sample
	weights []float64 // one weight per past cycle
	order   []int     // weightedMedian's indices, reused
}

// resize gives b exactly n elements, reusing its room where it can.
func resize[T any](b []T, n int) []T {
	if cap(b) < n {
		return make([]T, n)
	}
	return b[:n]
}

// histVerdictOf is the history layer: what the completed cycles of this
// window say about this one. It has nothing to say for a window shorter than
// two days, or when fewer than forecastHistMinCycles past cycles are whole
// enough to use.
func histVerdictOf(points []QuotaPoint, cycles []cycleRange, span, dur time.Duration, left, uNow float64, now time.Time) (histVerdict, bool) {
	if dur < forecastHistoryFrom {
		return histVerdict{}, false
	}
	sc := forecastScratch{}
	type past struct {
		off    int     // where this cycle's grid starts in sc.curves
		end    float64 // the % that cycle spent in all
		weight float64
	}
	pasts := make([]past, 0, len(cycles)-1)
	for _, r := range cycles[:len(cycles)-1] {
		c := points[r.lo:r.hi]
		start, ok := cycleStartOf(c)
		if !ok {
			continue
		}
		reset, ok := cycleResetOf(c, start, span)
		if !ok {
			continue
		}
		off := len(sc.curves)
		end, ok := usedCurve(&sc, c, start, reset)
		if !ok || end < forecastHistMinUsed {
			sc.curves = sc.curves[:off] // unused: hand its room back
			continue
		}
		age := now.Sub(reset).Hours() / dur.Hours()
		if age < 0 {
			age = 0
		}
		pasts = append(pasts, past{off: off, end: end, weight: math.Exp(-age / 3)})
	}
	if len(pasts) < forecastHistMinCycles {
		return histVerdict{}, false
	}
	// Few cycles are one voice, many are the shape: lambda weighs the shared
	// shape against an even burn by how many independent cycles went in.
	sc.weights = resize(sc.weights, len(pasts))
	sw, sw2 := 0.0, 0.0
	for i, p := range pasts {
		sc.weights[i] = p.weight
		sw += p.weight
		sw2 += p.weight * p.weight
	}
	if sw2 == 0 { // every weight underflowed: nothing to weigh a shape with
		return histVerdict{}, false
	}
	lambda := (sw*sw/sw2 - 2) / 6
	lambda = math.Max(0, math.Min(1, lambda))
	k := forecastHistGridK + 1
	sc.values = resize(sc.values, len(pasts))
	sc.order = resize(sc.order, len(pasts))
	shapeAt := func(u float64) float64 {
		for i, p := range pasts {
			sc.values[i] = sampleGrid(sc.curves[p.off:p.off+k], u) / p.end
		}
		return weightedMedian(sc.values, sc.weights, sc.order)
	}
	blend := func(u float64) float64 { return lambda*shapeAt(u) + (1-lambda)*u }
	usedNow := 100 - left
	shape := math.Max(blend(uNow), forecastHistMinShape)
	projected := usedNow / shape
	v := histVerdict{leftAtReset: 100 - projected, lasts: projected <= 100, etaHours: math.Inf(1), cycles: len(pasts)}
	if !v.lasts {
		v.etaHours = histCrossing(blend, usedNow, shape, uNow, dur)
	}
	return v, true
}

// usedCurve is a past cycle's spending on a monotone curve over its own span:
// used percent, never falling, at each step of the cycle, appended to sc's
// grid buffer; its polyline is built in sc's reusable buffers. end is the %
// the cycle spent in all. ok is false when the cycle has too few readings, no
// span, or no reading near both its start and its reset — a shape seen only
// in the middle isn't one.
func usedCurve(sc *forecastScratch, c []QuotaPoint, start, reset time.Time) (end float64, ok bool) {
	d := reset.Sub(start)
	if len(c) < forecastHistMinPoints || d <= 0 {
		return 0, false
	}
	nearStart, nearReset := false, false
	us := append(sc.us[:0], 0)
	vs := append(sc.vs[:0], 0)
	run := 0.0
	push := func(u, v float64) {
		if u <= us[len(us)-1] {
			if v > vs[len(vs)-1] {
				vs[len(vs)-1] = v
			}
			return
		}
		us = append(us, u)
		vs = append(vs, v)
	}
	for _, p := range c {
		if absDur(p.At.Sub(start)) <= forecastHistCoverage {
			nearStart = true
		}
		if absDur(p.At.Sub(reset)) <= forecastHistCoverage {
			nearReset = true
		}
		used := 100 - p.Left
		if used < run {
			used = run
		}
		run = used
		u := p.At.Sub(start).Seconds() / d.Seconds()
		push(math.Max(0, math.Min(1, u)), run)
	}
	if !nearStart || !nearReset {
		return 0, false
	}
	push(1, run) // the cycle's spending is held to its reset
	sc.us, sc.vs = us, vs
	sc.curves = appendCurve(sc.curves, us, vs)
	return run, true
}

// appendCurve appends a past cycle's grid — its used percent at each step of
// the cycle, taken from the polyline (us, vs) — to dst, returning the grown
// buffer.
func appendCurve(dst, us, vs []float64) []float64 {
	j := 0
	for i := 0; i <= forecastHistGridK; i++ {
		u := float64(i) / forecastHistGridK
		for j+1 < len(us) && us[j+1] <= u {
			j++
		}
		if j+1 >= len(us) || us[j+1] == us[j] {
			dst = append(dst, vs[j])
			continue
		}
		dst = append(dst, vs[j]+(u-us[j])/(us[j+1]-us[j])*(vs[j+1]-vs[j]))
	}
	return dst
}

// sampleGrid reads a value at u from a grid over [0,1], between two points.
func sampleGrid(curve []float64, u float64) float64 {
	if u <= 0 {
		return curve[0]
	}
	if u >= 1 {
		return curve[len(curve)-1]
	}
	x := u * forecastHistGridK
	i := int(x)
	t := x - float64(i)
	return curve[i] + t*(curve[i+1]-curve[i])
}

// weightedMedian is the value the middle of the weight falls in. order is
// scratch room for the indices, so a crossing sample doesn't allocate.
func weightedMedian(values, weights []float64, order []int) float64 {
	if len(values) == 0 {
		return 0
	}
	if cap(order) < len(values) {
		order = make([]int, len(values))
	}
	order = order[:len(values)]
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return values[order[a]] < values[order[b]] })
	total := 0.0
	for _, w := range weights {
		total += w
	}
	acc := 0.0
	for _, i := range order {
		acc += weights[i]
		if acc*2 >= total {
			return values[i]
		}
	}
	return values[order[len(order)-1]]
}

// histCrossing walks the blended shape forward from uNow for the point where
// the projected usage reaches 100%, giving the hours to empty.
func histCrossing(shapeAt func(float64) float64, usedNow, shape, uNow float64, dur time.Duration) float64 {
	const steps = 480
	target := 100 * shape / usedNow
	prevU, prevV := uNow, shapeAt(uNow)
	for i := 1; i <= steps; i++ {
		u := uNow + (1-uNow)*float64(i)/steps
		v := shapeAt(u)
		if v >= target {
			if v <= prevV {
				return dur.Hours() * (u - uNow)
			}
			t := (target - prevV) / (v - prevV)
			return dur.Hours() * (prevU + t*(u-prevU) - uNow)
		}
		prevU, prevV = u, v
	}
	return math.Inf(1)
}
