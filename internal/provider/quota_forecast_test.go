package provider

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// forecastNow is the clock every case is read at: the engine takes now, so
// none of this sleeps.
var forecastNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// cyclePoints is one cycle's readings, from (fraction of the cycle, percent
// left) pairs, each carrying the cycle's start and reset.
func cyclePoints(start time.Time, dur time.Duration, samples ...[2]float64) []QuotaPoint {
	s, r := start, start.Add(dur)
	pts := make([]QuotaPoint, 0, len(samples))
	for _, sm := range samples {
		pts = append(pts, QuotaPoint{
			At:    start.Add(time.Duration(sm[0] * float64(dur))),
			Left:  sm[1],
			Start: &s, ResetsAt: &r,
		})
	}
	return pts
}

// shapedCycle is a cycle that spends 0 to end percent along shape(u), read at
// each fraction in fracs.
func shapedCycle(start time.Time, dur time.Duration, end float64, shape func(float64) float64, fracs []float64) []QuotaPoint {
	samples := make([][2]float64, 0, len(fracs))
	for _, fr := range fracs {
		samples = append(samples, [2]float64{fr, 100 - shape(fr)*end})
	}
	return cyclePoints(start, dur, samples...)
}

// pastCycles is n whole cycles just before the one starting at curStart.
func pastCycles(curStart time.Time, dur time.Duration, n int, end float64, shape func(float64) float64, fracs []float64) []QuotaPoint {
	var pts []QuotaPoint
	for j := n; j >= 1; j-- {
		pts = append(pts, shapedCycle(curStart.Add(-time.Duration(j)*dur), dur, end, shape, fracs)...)
	}
	return pts
}

func eq2(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 0.005 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func eqi(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}

func eqs(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// weekFracs are readings spread over a weekly cycle, near enough to its start
// and its reset for the history layer to use it.
var weekFracs = []float64{0.02, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.98}

func fracsUpTo(u float64) []float64 {
	var out []float64
	for _, fr := range weekFracs {
		if fr <= u+1e-9 {
			out = append(out, fr)
		}
	}
	return out
}

func linearShape(u float64) float64 { return u }

// TestQuotaForecast is the engine over every state and blend: an even burn
// alone on a short window, the history layer on a weekly one, a cycle that
// ran out, one that only began, one that started again, and the conservative
// blend of a layer that lasts with one that doesn't.
func TestQuotaForecast(t *testing.T) {
	fiveDur := 5 * time.Hour
	fiveStart := forecastNow.Add(-150 * time.Minute) // half of the window
	lagging := cyclePoints(fiveStart, fiveDur,
		[2]float64{0, 100}, [2]float64{0.125, 85}, [2]float64{0.25, 70}, [2]float64{0.375, 55}, [2]float64{0.5, 40})
	ahead := cyclePoints(fiveStart, fiveDur,
		[2]float64{0, 100}, [2]float64{0.125, 92.5}, [2]float64{0.25, 85}, [2]float64{0.375, 77.5}, [2]float64{0.5, 70})
	spent := cyclePoints(fiveStart, fiveDur,
		[2]float64{0.1, 80}, [2]float64{0.2, 60}, [2]float64{0.3, 40}, [2]float64{0.4, 20}, [2]float64{0.5, 0.2})

	// A 5-hour window with four whole cycles behind it: too short for the
	// history layer, whatever they say.
	shortStart := forecastNow.Add(-150 * time.Minute)
	shortWithHistory := pastCycles(shortStart, fiveDur, 4, 80, func(u float64) float64 { return u * u * u * u }, weekFracs)
	shortWithHistory = append(shortWithHistory, cyclePoints(shortStart, fiveDur,
		[2]float64{0, 100}, [2]float64{0.25, 70}, [2]float64{0.5, 40})...)

	// A cycle only begun: under 8% elapsed.
	youngStart := forecastNow.Add(-12 * time.Minute)
	young := cyclePoints(youngStart, fiveDur,
		[2]float64{0.01, 99}, [2]float64{0.02, 95}, [2]float64{0.04, 90})

	// Exactly 8% elapsed: the gate is "under", so this much is already enough.
	atGate := cyclePoints(forecastNow.Add(-24*time.Minute), fiveDur,
		[2]float64{0, 100}, [2]float64{0.04, 90}, [2]float64{0.08, 80})
	// Nothing spent yet: at 99.5% left the readings say nothing.
	untouched := cyclePoints(forecastNow.Add(-150*time.Minute), fiveDur,
		[2]float64{0, 100}, [2]float64{0.5, 99.5})
	// A slow half-cycle: the even burn lasts, but its crossing is past the
	// ten-window cap, so the headroom is the cap over the time to the reset.
	slow := cyclePoints(forecastNow.Add(-150*time.Minute), fiveDur,
		[2]float64{0, 100}, [2]float64{0.25, 98}, [2]float64{0.5, 96})

	// A window that started again mid-list: only the last cycle counts.
	curStart := forecastNow.Add(-time.Hour)
	oldStart := curStart.Add(-fiveDur)
	restarted := cyclePoints(oldStart, fiveDur,
		[2]float64{0.02, 10}, [2]float64{0.5, 5}, [2]float64{0.9, 2}, [2]float64{0.98, 1})
	restarted = append(restarted, cyclePoints(curStart, fiveDur,
		[2]float64{0.02, 98}, [2]float64{0.1, 88}, [2]float64{0.2, 76})...)

	// Weekly: whole cycles behind the current one. Back-loaded history (all
	// the spending late) runs out where an even burn lasts; front-loaded
	// history lasts where an even burn runs out.
	weekDur := 7 * 24 * time.Hour
	weekStart := forecastNow.Add(-weekDur / 2) // u = 0.5
	// weekCurrent is a linear burn: rate percent of the window by u = 1.
	weekCurrent := func(rate float64) []QuotaPoint {
		return shapedCycle(weekStart, weekDur, rate, linearShape, fracsUpTo(0.5))
	}
	backLoaded := func(n int) []QuotaPoint {
		return pastCycles(weekStart, weekDur, n, 60, func(u float64) float64 { return u * u * u * u }, weekFracs)
	}
	frontLoaded := func(n int) []QuotaPoint {
		return pastCycles(weekStart, weekDur, n, 60, func(u float64) float64 { return math.Pow(u, 0.2) }, weekFracs)
	}

	tests := []struct {
		name        string
		window      string
		points      []QuotaPoint
		state       string
		source      string
		cycles      int
		left        float64
		evenLeft    float64
		ahead       float64
		rate        float64
		lasts       bool
		eta         float64
		leftAtReset float64
		headroom    float64
		anyEta      bool // the history shape fixes these, so only their sign is checked
		anyLeft     bool
		check       func(*testing.T, QuotaForecast)
	}{
		{name: "5h lagging behind an even burn", window: "5 hours", points: lagging,
			state: "ok", source: "even", cycles: 0, left: 40, evenLeft: 50, ahead: -10, rate: 24,
			lasts: false, eta: 6000, leftAtReset: -20, headroom: 0},
		{name: "5h ahead of an even burn", window: "5 hours", points: ahead,
			state: "ok", source: "even", cycles: 0, left: 70, evenLeft: 50, ahead: 20, rate: 12,
			lasts: true, eta: 0, leftAtReset: 40, headroom: 2.33},
		{name: "5h with four cycles of history", window: "5 hours", points: shortWithHistory,
			state: "ok", source: "even", cycles: 0, left: 40, evenLeft: 50, ahead: -10, rate: 24,
			lasts: false, eta: 6000, leftAtReset: -20, headroom: 0},
		{name: "spent", window: "5 hours", points: spent,
			state: "spent", source: "even", cycles: 0, left: 0.2, evenLeft: 50, ahead: -49.8, rate: 39.92,
			lasts: false, eta: 0, leftAtReset: -99.6, headroom: 0},
		{name: "just begun", window: "5 hours", points: young,
			state: "none", source: "even", cycles: 0, left: 90, evenLeft: 96, ahead: -6, rate: 50,
			lasts: false, eta: 0, leftAtReset: 0, headroom: 0},
		{name: "8% elapsed is enough", window: "5 hours", points: atGate,
			state: "ok", source: "even", cycles: 0, left: 80, evenLeft: 92, ahead: -12, rate: 50,
			lasts: false, eta: 5760, leftAtReset: -150, headroom: 0},
		{name: "nothing consumed yet", window: "5 hours", points: untouched,
			state: "none", source: "even", cycles: 0, left: 99.5, evenLeft: 50, ahead: 49.5, rate: 0.2,
			lasts: false, eta: 0, leftAtReset: 0, headroom: 0},
		{name: "eta capped at ten windows", window: "5 hours", points: slow,
			state: "ok", source: "even", cycles: 0, left: 96, evenLeft: 50, ahead: 46, rate: 1.6,
			lasts: true, eta: 0, leftAtReset: 92, headroom: 20},
		{name: "started again mid-list", window: "5 hours", points: restarted,
			state: "ok", source: "even", cycles: 0, left: 76, evenLeft: 80, ahead: -4, rate: 24,
			lasts: false, eta: 11400, leftAtReset: -20, headroom: 0,
			check: func(t *testing.T, f QuotaForecast) {
				if !f.CycleStart.Equal(curStart) {
					t.Errorf("cycleStart = %v, want the current cycle's %v", f.CycleStart, curStart)
				}
			}},
		{name: "weekly, back-loaded history runs out where even lasts", window: "Weekly",
			points: append(backLoaded(6), weekCurrent(90)...),
			state:  "ok", source: "history", cycles: 6, left: 55, evenLeft: 50, ahead: 5, rate: 0.54,
			lasts: false, eta: 0, leftAtReset: 0, headroom: 0, anyEta: true, anyLeft: true,
			check: func(t *testing.T, f QuotaForecast) {
				if f.EtaSeconds <= 0 || f.EtaSeconds >= 84*3600 {
					t.Errorf("etaSeconds = %v, want sooner than the reset (84h)", f.EtaSeconds)
				}
				if f.LeftAtReset >= 0 {
					t.Errorf("leftAtReset = %v, want the history layer's negative projection", f.LeftAtReset)
				}
			}},
		{name: "weekly, even runs out where front-loaded history lasts", window: "Weekly",
			points: append(frontLoaded(6), weekCurrent(120)...),
			state:  "ok", source: "history", cycles: 6, left: 40, evenLeft: 50, ahead: -10, rate: 0.71,
			lasts: false, eta: 201600, leftAtReset: -20, headroom: 0},
		{name: "weekly, both last", window: "Weekly",
			points: append(frontLoaded(3), weekCurrent(60)...),
			state:  "ok", source: "history", cycles: 3, left: 70, evenLeft: 50, ahead: 20, rate: 0.36,
			lasts: true, eta: 0, leftAtReset: 40, headroom: 2.33},
		{name: "weekly with only two cycles", window: "Weekly",
			points: append(frontLoaded(2), weekCurrent(60)...),
			state:  "ok", source: "even", cycles: 0, left: 70, evenLeft: 50, ahead: 20, rate: 0.36,
			lasts: true, eta: 0, leftAtReset: 40, headroom: 2.33},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := quotaForecast(tt.window, tt.points, forecastNow)
			if !ok {
				t.Fatal("no forecast")
			}
			eqs(t, f.State, tt.state, "state")
			eqs(t, f.Source, tt.source, "source")
			eqi(t, f.Cycles, tt.cycles, "cycles")
			eq2(t, f.Left, tt.left, "left")
			eq2(t, f.EvenLeft, tt.evenLeft, "evenLeft")
			eq2(t, f.Ahead, tt.ahead, "ahead")
			eq2(t, f.RatePerHour, tt.rate, "ratePerHour")
			if f.LastsToReset != tt.lasts {
				t.Errorf("lastsToReset = %v, want %v", f.LastsToReset, tt.lasts)
			}
			if !tt.anyEta {
				eq2(t, f.EtaSeconds, tt.eta, "etaSeconds")
			}
			if !tt.anyLeft {
				eq2(t, f.LeftAtReset, tt.leftAtReset, "leftAtReset")
			}
			eq2(t, f.Headroom, tt.headroom, "headroom")
			if f.State == "ok" && (f.Headroom > 1) != f.LastsToReset {
				t.Errorf("headroom = %v with lastsToReset = %v: the two must agree", f.Headroom, f.LastsToReset)
			}
			if _, err := json.Marshal(f); err != nil {
				t.Errorf("forecast does not marshal: %v", err)
			}
			if tt.check != nil {
				tt.check(t, f)
			}
		})
	}
}

// TestQuotaForecastBails: nothing is said where there is nothing to say — a
// single reading, a reset already past, a window with no known span, and a
// NaN reading.
func TestQuotaForecastBails(t *testing.T) {
	fiveDur := 5 * time.Hour
	if _, ok := quotaForecast("5 hours", cyclePoints(forecastNow, fiveDur, [2]float64{0, 100}), forecastNow); ok {
		t.Error("a single reading was forecast")
	}
	resetPast := cyclePoints(forecastNow.Add(-6*time.Hour), fiveDur, [2]float64{0, 100}, [2]float64{0.5, 60})
	if _, ok := quotaForecast("5 hours", resetPast, forecastNow); ok {
		t.Error("a cycle whose reset has passed was forecast")
	}
	noSpan := []QuotaPoint{
		{At: forecastNow.Add(-2 * time.Hour), Left: 60},
		{At: forecastNow.Add(-time.Hour), Left: 55},
	}
	if _, ok := quotaForecast("Mystery window", noSpan, forecastNow); ok {
		t.Error("a window with no span and no reset was forecast")
	}
	nan := cyclePoints(forecastNow.Add(-150*time.Minute), fiveDur, [2]float64{0, 100}, [2]float64{0.5, math.NaN()})
	if _, ok := quotaForecast("5 hours", nan, forecastNow); ok {
		t.Error("a NaN reading was forecast")
	}
}

// TestQuotaForecastStaysOutOfSync: the sync between computers reads points
// only, so a forecast on a line never reaches it; and a line carries the
// field only when it has one.
func TestQuotaForecastStaysOutOfSync(t *testing.T) {
	plain := `{"p|u":{"Weekly":[{"at":"2026-10-05T12:00:00Z","left":50}]}}`
	extra := `{"p|u":{"Weekly":[{"at":"2026-10-05T12:00:00Z","left":50,"forecast":{"state":"ok","left":50}}]}}`
	a, err := parseQuotaHist([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseQuotaHist([]byte(extra))
	if err != nil {
		t.Fatal(err)
	}
	if mergeQuotaHist(a, b) {
		t.Error("merging the same point with a forecast on it changed the history")
	}
	pts := a["p|u"]["Weekly"]
	if len(pts) != 1 || !samePoint(pts[0], QuotaPoint{At: forecastNow, Left: 50}) {
		t.Fatalf("points = %+v, want the plain one", pts)
	}

	line := QuotaLine{Name: "Weekly", Points: []QuotaPoint{{At: forecastNow, Left: 50}},
		Forecast: &QuotaForecast{State: "ok", Source: "even", Left: 50}}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"forecast":{"state":"ok",`) {
		t.Errorf("line = %s, want it to carry the forecast", raw)
	}
	bare, err := json.Marshal(QuotaLine{Name: "Weekly"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "forecast") {
		t.Errorf("line = %s, want no forecast field when there is none", bare)
	}
}

// TestQuotaForecastShapeJSON logs realistic forecasts, the shape the
// Usage page and the LAN endpoint serialize.
func TestQuotaForecastShapeJSON(t *testing.T) {
	five := cyclePoints(forecastNow.Add(-150*time.Minute), 5*time.Hour,
		[2]float64{0, 100}, [2]float64{0.125, 85}, [2]float64{0.25, 70}, [2]float64{0.375, 55}, [2]float64{0.5, 40})
	if f, ok := quotaForecast("5 hours", five, forecastNow); ok {
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("5-hour forecast:\n%s", b)
	} else {
		t.Fatal("no 5-hour forecast")
	}

	dur := 7 * 24 * time.Hour
	start := forecastNow.Add(-3 * 24 * time.Hour) // u = 3/7
	pts := pastCycles(start, dur, 6, 60, func(u float64) float64 { return u * u }, weekFracs)
	pts = append(pts, shapedCycle(start, dur, 50, linearShape, fracsUpTo(3.0/7.0))...)
	f, ok := quotaForecast("Weekly", pts, forecastNow)
	if !ok {
		t.Fatal("no forecast")
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("weekly forecast:\n%s", b)
}

// TestQuotaHistoriesForecast is the read path end to end: a window recorded
// as it is read gets its forecast, and the file the sync shares does not.
func TestQuotaHistoriesForecast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(150 * time.Minute)
	card := func(used float64) []SubscriptionQuota {
		return []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "u", Windows: []QuotaWindow{
			{Name: "5 hours", Used: used, ResetsAt: &reset, Span: 5 * time.Hour},
		}}}
	}
	// Half a window gone at a steady 28%/h: 30 minutes apart, the last now.
	for i, used := range []float64{60, 62, 64, 66, 68, 70} {
		noteQuotaHistory(card(used), now.Add(time.Duration(i-5)*30*time.Minute))
	}
	hs := QuotaHistories(now.Add(-time.Hour), "codex", "")
	if len(hs) != 1 || len(hs[0].Lines) != 1 {
		t.Fatalf("histories = %+v", hs)
	}
	f := hs[0].Lines[0].Forecast
	if f == nil {
		t.Fatal("the read path left the forecast off the line")
	}
	if f.State != "ok" || f.Source != "even" || f.Cycles != 0 {
		t.Errorf("forecast = %+v, want an ok even one with no history", f)
	}
	if f.Left != 30 || math.Abs(f.EvenLeft-50) > 0.05 || math.Abs(f.RatePerHour-28) > 0.01 {
		t.Errorf("forecast = %+v, want left 30 at 28%%/h against an even 50", f)
	}
	if f.LastsToReset {
		t.Errorf("forecast = %+v, want it to run out before the reset", f)
	}
	if strings.Contains(string(QuotaHistoryData()), "forecast") {
		t.Error("the stored history carries a forecast: the sync path must not see it")
	}
}

// TestQuotaForecastNoCycleStart: a window whose readings carry a reset but no
// start has no elapsed time to go on — "Month" does not parse into a span, so
// its stored points carry no start — and the forecast must stand down rather
// than invent one from the first observation.
func TestQuotaForecastNoCycleStart(t *testing.T) {
	reset := forecastNow.Add(2 * time.Hour)
	points := []QuotaPoint{
		{At: forecastNow.Add(-48 * time.Hour), Left: 80, ResetsAt: &reset},
		{At: forecastNow.Add(-24 * time.Hour), Left: 60, ResetsAt: &reset},
		{At: forecastNow, Left: 40, ResetsAt: &reset},
	}
	if _, ok := quotaForecast("Month", points, forecastNow); ok {
		t.Error("a cycle whose readings carry no start was forecast")
	}
}

// TestQuotaForecastHistoryGates: a past cycle counts only when it has enough
// readings, a reading near both its start and its reset, and it spent
// something.
func TestQuotaForecastHistoryGates(t *testing.T) {
	weekDur := 7 * 24 * time.Hour
	weekStart := forecastNow.Add(-weekDur / 2)
	current := shapedCycle(weekStart, weekDur, 60, linearShape, fracsUpTo(0.5))
	fracs5 := []float64{0.05, 0.3, 0.5, 0.7, 0.95}
	fracs6 := []float64{0.05, 0.2, 0.35, 0.5, 0.7, 0.95}
	fracsMid := []float64{0.2, 0.3, 0.4, 0.5, 0.6, 0.7}
	fracsNoReset := []float64{0.05, 0.2, 0.3, 0.4, 0.5, 0.7}
	square := func(u float64) float64 { return u * u }
	history := func(end float64, shape func(float64) float64, fracs []float64) []QuotaPoint {
		return append(pastCycles(weekStart, weekDur, 3, end, shape, fracs), current...)
	}
	tests := []struct {
		name   string
		points []QuotaPoint
		source string
		cycles int
	}{
		{"five readings are too few", history(60, square, fracs5), "even", 0},
		{"six readings are enough", history(60, square, fracs6), "history", 3},
		{"no reading near the start", history(60, square, fracsMid), "even", 0},
		{"no reading near the reset", history(60, square, fracsNoReset), "even", 0},
		{"a cycle that spent nothing", history(3, linearShape, fracs6), "even", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := quotaForecast("Weekly", tt.points, forecastNow)
			if !ok {
				t.Fatal("no forecast")
			}
			eqs(t, f.Source, tt.source, "source")
			eqi(t, f.Cycles, tt.cycles, "cycles")
		})
	}
}

// TestQuotaForecastHistoryBlendPinned pins the blend numerically: three
// identical back-loaded cycles are weighted by their ages, so a wrong lambda
// or recency weight moves the projection. The figures are worked out from the
// blend, so a broken lambda fails here rather than passing an anyLeft check.
func TestQuotaForecastHistoryBlendPinned(t *testing.T) {
	weekDur := 7 * 24 * time.Hour
	weekStart := forecastNow.Add(-weekDur / 2) // u = 1/2
	history := pastCycles(weekStart, weekDur, 3, 60, func(u float64) float64 { return u * u * u * u }, weekFracs)
	at := func(rate float64) []QuotaPoint {
		return append(append([]QuotaPoint{}, history...), shapedCycle(weekStart, weekDur, rate, linearShape, fracsUpTo(0.5))...)
	}

	lasts, ok := quotaForecast("Weekly", at(50), forecastNow)
	if !ok {
		t.Fatal("no forecast")
	}
	eqs(t, lasts.Source, "history", "source")
	eqi(t, lasts.Cycles, 3, "cycles")
	if !lasts.LastsToReset || lasts.EtaSeconds != 0 {
		t.Errorf("rate 50: lastsToReset = %v, etaSeconds = %v, want it to last", lasts.LastsToReset, lasts.EtaSeconds)
	}
	eq2(t, lasts.LeftAtReset, 43.5, "leftAtReset at rate 50")
	eq2(t, lasts.Headroom, 3, "headroom at rate 50")

	runsOut, ok := quotaForecast("Weekly", at(90), forecastNow)
	if !ok {
		t.Fatal("no forecast")
	}
	if runsOut.LastsToReset || runsOut.EtaSeconds <= 0 {
		t.Errorf("rate 90: lastsToReset = %v, etaSeconds = %v, want it to run out", runsOut.LastsToReset, runsOut.EtaSeconds)
	}
	eq2(t, runsOut.LeftAtReset, -1.7, "leftAtReset at rate 90")
	if math.Abs(runsOut.EtaSeconds-291007.43) > 50 {
		t.Errorf("etaSeconds at rate 90 = %v, want 291007.43", runsOut.EtaSeconds)
	}
}

// TestQuotaHistoriesForecastFullSeries: the verdict is the whole stored
// series', so a client asking for a short range gets the same source and
// cycle count as one asking for the lot — only its points are sliced.
func TestQuotaHistoriesForecastFullSeries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	dur := 7 * 24 * time.Hour
	start := now.Add(-dur / 2)
	pts := pastCycles(start, dur, 6, 60, func(u float64) float64 { return u * u * u * u }, weekFracs)
	pts = append(pts, shapedCycle(start, dur, 50, linearShape, fracsUpTo(0.5))...)
	writeQuotaHist(quotaHist{"codex|u": {"Weekly": pts}})

	hs := QuotaHistories(now.Add(-3*time.Hour), "codex", "u")
	if len(hs) != 1 || len(hs[0].Lines) != 1 {
		t.Fatalf("histories = %+v", hs)
	}
	line := hs[0].Lines[0]
	if len(line.Points) >= len(pts) {
		t.Errorf("points = %d, want the series sliced to the asked range", len(line.Points))
	}
	if line.Forecast == nil {
		t.Fatal("the line has no forecast")
	}
	if line.Forecast.Source != "history" || line.Forecast.Cycles != 6 {
		t.Errorf("forecast = %+v, want the full series' history verdict", line.Forecast)
	}
}

// The display range trims points, never the cycles that produce a forecast.
func TestQuotaHistoriesForecastIgnoresDisplayDays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	dur := 7 * 24 * time.Hour
	start := now.Add(-dur / 2)
	pts := pastCycles(start, dur, 4, 80, linearShape, weekFracs)
	pts = append(pts, shapedCycle(start, dur, 70, linearShape, fracsUpTo(0.5))...)
	writeQuotaHist(quotaHist{"codex|u": {"Weekly": pts}})
	var first *QuotaForecast
	for _, days := range []string{"1", "2", "30", "all"} {
		hs := QuotaHistories(QuotaHistorySince(days, now), "codex", "u")
		if len(hs) != 1 || len(hs[0].Lines) != 1 || hs[0].Lines[0].Forecast == nil {
			t.Fatalf("days=%s: no forecast: %+v", days, hs)
		}
		f := hs[0].Lines[0].Forecast
		if first == nil {
			first = f
			continue
		}
		if f.State != first.State || f.Source != first.Source || f.Cycles != first.Cycles || f.LastsToReset != first.LastsToReset || f.Left != first.Left {
			t.Fatalf("days=%s: verdict changed: %+v vs %+v", days, f, first)
		}
		eq2(t, f.LeftAtReset, first.LeftAtReset, "left at reset")
		eq2(t, f.Headroom, first.Headroom, "headroom")
	}
	if first.Source != "history" || first.Cycles != 4 {
		t.Fatalf("fixture did not exercise history: %+v", first)
	}
	if err := os.Remove(quotaHistPath()); err != nil {
		t.Fatal(err)
	}
	if got := QuotaHistories(now.Add(-dur), "", ""); len(got) != 0 {
		t.Fatalf("missing history: %+v", got)
	}
}

func TestQuotaForecastSpentInYoungCycle(t *testing.T) {
	pts := cyclePoints(forecastNow.Add(-10*time.Minute), 5*time.Hour, [2]float64{0, 100}, [2]float64{1.0 / 30, 0})
	f, ok := quotaForecast("5 hours", pts, forecastNow)
	if !ok || f.State != "spent" || f.EtaSeconds != 0 || f.LastsToReset {
		t.Fatalf("depleted young cycle: %+v, %v", f, ok)
	}
}

func TestQuotaForecastDoesNotMutateHistory(t *testing.T) {
	pts := pastCycles(forecastNow.Add(-84*time.Hour), 7*24*time.Hour, 4, 80, linearShape, weekFracs)
	pts = append(pts, shapedCycle(forecastNow.Add(-84*time.Hour), 7*24*time.Hour, 70, linearShape, fracsUpTo(0.5))...)
	original := slices.Clone(pts)
	quotaForecast("Weekly", pts, forecastNow)
	if !slices.Equal(pts, original) {
		t.Fatal("forecast changed persisted history")
	}
}

func BenchmarkQuotaForecast(b *testing.B) {
	dur := 7 * 24 * time.Hour
	start := forecastNow.Add(-dur / 2)
	pts := pastCycles(start, dur, 5, 90, linearShape, weekFracs)
	pts = append(pts, shapedCycle(start, dur, 85, linearShape, fracsUpTo(0.5))...)
	b.ReportAllocs()
	for b.Loop() {
		quotaForecast("Weekly", pts, forecastNow)
	}
}

// Unobserved time is not evidence of zero consumption. The same reading
// keeps its verdict and crossing; only the ETA counts down as the clock moves.
func TestQuotaForecastOldReading(t *testing.T) {
	start := forecastNow.Add(-150 * time.Minute)
	pts := cyclePoints(start, 5*time.Hour, [2]float64{0, 100}, [2]float64{0.5, 40})
	first, ok := quotaForecast("5 hours", pts, forecastNow)
	if !ok || first.LastsToReset || first.RunsOutAt == nil {
		t.Fatalf("initial forecast: %+v %v", first, ok)
	}
	for _, age := range []time.Duration{90 * time.Minute, 110 * time.Minute} {
		f, ok := quotaForecast("5 hours", pts, forecastNow.Add(age))
		if !ok || f.State != "ok" || f.LastsToReset {
			t.Fatalf("old reading became optimistic or actually spent at %v: %+v %v", age, f, ok)
		}
		eq2(t, f.RatePerHour, 24, "rate anchored to observation")
		eq2(t, f.Ahead, first.Ahead, "ahead anchored to observation")
		eq2(t, f.LeftAtReset, first.LeftAtReset, "reset projection")
		if !f.AsOf.Equal(forecastNow) || f.RunsOutAt == nil || !f.RunsOutAt.Equal(*first.RunsOutAt) {
			t.Fatalf("projection moved at %v: %+v", age, f)
		}
		eq2(t, f.EtaSeconds, math.Max(0, 6000-age.Seconds()), "countdown")
	}
	if _, ok := quotaForecast("5 hours", pts, start.Add(5*time.Hour)); ok {
		t.Fatal("old cycle still forecast after its reset")
	}
	nextStart := start.Add(5 * time.Hour)
	newPoints := append(slices.Clone(pts), cyclePoints(nextStart, 5*time.Hour, [2]float64{0, 100}, [2]float64{0.01, 99})...)
	f, ok := quotaForecast("5 hours", newPoints, nextStart.Add(3*time.Minute))
	if !ok || f.State != "none" || !f.CycleStart.Equal(nextStart) || f.RunsOutAt != nil {
		t.Fatalf("new cycle reused old verdict: %+v %v", f, ok)
	}
}

// Kiro's monthly credits were 67% left about 6% into its cycle. A clear
// early burn must warn without waiting for 8%; a near-even burn still waits.
func TestQuotaForecastEarlyBurn(t *testing.T) {
	dur := 30 * 24 * time.Hour
	for _, tt := range []struct {
		name    string
		elapsed time.Duration
		left    float64
		state   string
	}{
		{"Kiro monthly 6% elapsed, 67% left", dur * 6 / 100, 67, "ok"},
		{"slightly early, 6% elapsed, 90% left", dur * 6 / 100, 90, "none"},
		{"crossing just after halfway to reset", dur * 6 / 100, 88.68, "none"},
		{"crossing just before halfway to reset", dur * 6 / 100, 88.67, "ok"},
		{"little consumed, 6% elapsed, 97% left", dur * 6 / 100, 97, "none"},
		{"half percent used after 30 minutes", 30 * time.Minute, 99.5, "none"},
		{"one percent used after 30 minutes", 30 * time.Minute, 99, "none"},
		{"five percent used after 30 minutes", 30 * time.Minute, 95, "none"},
		{"just under ten percent used after 30 minutes", 30 * time.Minute, 90.01, "none"},
		{"ten percent used after 30 minutes", 30 * time.Minute, 90, "ok"},
		{"heavy burn before 30 minutes", 29 * time.Minute, 67, "none"},
		{"heavy burn at 30 minutes", 30 * time.Minute, 67, "ok"},
		{"heavy burn after 30 minutes", 31 * time.Minute, 67, "ok"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start := forecastNow.Add(-tt.elapsed)
			points := cyclePoints(start, dur, [2]float64{0, 100}, [2]float64{float64(tt.elapsed) / float64(dur), tt.left})
			f, ok := quotaForecast("Monthly", points, forecastNow)
			if !ok || f.State != tt.state {
				t.Fatalf("forecast = %+v, %v; want %s", f, ok, tt.state)
			}
			if tt.state == "none" {
				if f.RunsOutAt != nil || f.EtaSeconds != 0 {
					t.Fatalf("young near-even cycle projected empty: %+v", f)
				}
				return
			}
			if f.LastsToReset || f.RunsOutAt == nil || f.EtaSeconds <= 0 || f.LeftAtReset >= 0 {
				t.Fatalf("early burn did not warn: %+v", f)
			}
			if tt.left == 67 && tt.elapsed == dur*6/100 {
				eq2(t, f.Left, 67, "left")
				eq2(t, f.EvenLeft, 94, "even left")
			}
			// The short display history must not invent a rate from one reading.
			if _, ok := quotaForecast("Monthly", points[1:], forecastNow); ok {
				t.Fatal("one reading got a forecast")
			}
		})
	}
}
