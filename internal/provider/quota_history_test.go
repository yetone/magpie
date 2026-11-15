package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func histCard(user string, used5h float64, reset5h time.Time, usedWeek float64, resetWeek time.Time) SubscriptionQuota {
	return SubscriptionQuota{Provider: "codex", Name: "Codex", User: user, Windows: []QuotaWindow{
		{Name: "5 hours", Used: used5h, ResetsAt: &reset5h},
		{Name: "Weekly", Used: usedWeek, ResetsAt: &resetWeek, Span: 7 * 24 * time.Hour},
		{Name: "Opus", Used: 1, Family: "Claude"},
		{Name: "Reviews", Unlimited: true},
	}}
}

func lineOf(t *testing.T, hs []QuotaHistory, user, name string) []QuotaPoint {
	t.Helper()
	for _, h := range hs {
		if h.User != user {
			continue
		}
		for _, l := range h.Lines {
			if l.Name == name {
				return l.Points
			}
		}
	}
	t.Fatalf("no %s line for %s in %+v", name, user, hs)
	return nil
}

// TestQuotaHistoryRecords: each reading of an account's windows is a point
// of the percent left, with when its window began and starts again; a
// window of a family, an unlimited one, a failed reading and one kept from
// before aren't kept; readings a minute apart within five are one point,
// and a run of the same figure its first and last (#651).
func TestQuotaHistoryRecords(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	r5, rw := t0.Add(3*time.Hour), t0.Add(4*24*time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 10, r5, 40, rw)}, at(0))
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 12, r5, 40, rw)}, at(1)) // within 5 min of the first: one more point
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 13, r5, 40, rw)}, at(2)) // moves it
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 20, r5, 40, rw)}, at(10))
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 20, r5, 40, rw)}, at(20)) // the same: the run's end moves
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 20, r5, 40, rw)}, at(30))
	bad := histCard("A@x.com", 90, r5, 90, rw)
	bad.Error = "401"
	stale := histCard("A@x.com", 90, r5, 90, rw)
	stale.AsOf = &t0
	noteQuotaHistory([]SubscriptionQuota{bad, stale}, at(40))

	hs := QuotaHistories(t0.Add(-time.Hour), "", "")
	if len(hs) != 1 || hs[0].Provider != "codex" || hs[0].User != "a@x.com" || len(hs[0].Lines) != 2 {
		t.Fatalf("histories = %+v", hs)
	}
	five := lineOf(t, hs, "a@x.com", "5 hours")
	want := []struct {
		m    int
		left float64
	}{{0, 90}, {2, 87}, {10, 80}, {30, 80}}
	if len(five) != len(want) {
		t.Fatalf("5 hours points = %+v", five)
	}
	for i, w := range want {
		if !five[i].At.Equal(at(w.m)) || five[i].Left != w.left {
			t.Errorf("point %d = %v %v, want %v %v", i, five[i].At, five[i].Left, at(w.m), w.left)
		}
	}
	if five[0].ResetsAt == nil || !five[0].ResetsAt.Equal(r5) || five[0].Start == nil || !five[0].Start.Equal(r5.Add(-5*time.Hour)) {
		t.Errorf("5 hours window = %v .. %v", five[0].Start, five[0].ResetsAt)
	}
	week := lineOf(t, hs, "a@x.com", "Weekly")
	if len(week) != 2 || week[0].Left != 60 || !week[0].Start.Equal(rw.Add(-7*24*time.Hour)) {
		t.Errorf("weekly = %+v", week)
	}
}

// TestQuotaHistoryCycles: a window started again is a new cycle, never
// folded into the one before, a rolling window's reset moving with the
// clock isn't one, and points older than 45 days go.
func TestQuotaHistoryCycles(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	week := t0.Add(7 * 24 * time.Hour)
	card := func(used float64, r5 time.Time) []SubscriptionQuota {
		return []SubscriptionQuota{histCard("", used, r5, 0, week)}
	}
	// a flat run, then the 5-hour window starts again at the same figure
	noteQuotaHistory(card(30, t0.Add(time.Hour)), t0)
	noteQuotaHistory(card(30, t0.Add(time.Hour)), t0.Add(20*time.Minute))
	noteQuotaHistory(card(30, t0.Add(time.Hour)), t0.Add(50*time.Minute))
	noteQuotaHistory(card(30, t0.Add(6*time.Hour)), t0.Add(70*time.Minute))
	noteQuotaHistory(card(30, t0.Add(6*time.Hour)), t0.Add(90*time.Minute))
	pts := lineOf(t, QuotaHistories(time.Time{}, "codex", ""), "", "5 hours")
	if len(pts) != 4 || !pts[1].At.Equal(t0.Add(50*time.Minute)) || !pts[2].At.Equal(t0.Add(70*time.Minute)) {
		t.Fatalf("cycles folded: %+v", pts)
	}
	if !newCycle(pts[1], pts[2]) || newCycle(pts[0], pts[1]) {
		t.Errorf("newCycle wrong at the reset")
	}
	// a rolling window: its reset 5 hours from each reading
	a := QuotaPoint{At: t0, ResetsAt: new(t0.Add(5 * time.Hour))}
	b := QuotaPoint{At: t0.Add(30 * time.Minute), ResetsAt: new(t0.Add(5*time.Hour + 30*time.Minute))}
	if newCycle(a, b) {
		t.Error("a rolling window's reset moving with the clock read as a new cycle")
	}

	// 46 days on, the old points go
	later := t0.Add(46 * 24 * time.Hour)
	noteQuotaHistory([]SubscriptionQuota{histCard("", 5, later.Add(time.Hour), 0, later.Add(24*time.Hour))}, later)
	pts = lineOf(t, QuotaHistories(time.Time{}, "", ""), "", "5 hours")
	if len(pts) != 1 || !pts[0].At.Equal(later) {
		t.Errorf("after 46 days: %+v", pts)
	}
}

// A refill can leave the deadline alone, advance it or bring it forward.
// Preserve both readings even when they are too close for normal sampling.
func TestQuotaHistoryUnscheduledResets(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reset := at.Add(7 * 24 * time.Hour)
	a := QuotaPoint{At: at, Left: 20, ResetsAt: &reset}
	for _, tc := range []struct {
		name       string
		gap, moved time.Duration
		left       float64
		want       bool
	}{
		{"refill with unchanged deadline", time.Minute, 0, 100, true},
		{"refill with rounded deadline", 10 * time.Minute, time.Minute, 100, true},
		{"manual restart", time.Minute, 24 * time.Hour, 100, true},
		{"official earlier restart", time.Minute, -24 * time.Hour, 100, true},
		{"ordinary reset", 7 * 24 * time.Hour, 7 * 24 * time.Hour, 100, true},
		{"small correction", time.Minute, 0, 24, false},
		{"deadline rounding", time.Minute, time.Minute, 20, false},
		{"rolling recovery", time.Hour, time.Hour, 80, false},
		{"fast rolling recovery", time.Minute, time.Minute, 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := reset.Add(tc.moved)
			b := QuotaPoint{At: at.Add(tc.gap), Left: tc.left, ResetsAt: &r}
			if got := newCycle(a, b); got != tc.want {
				t.Fatalf("cycle break = %v, want %v", got, tc.want)
			}
			if tc.want && len(addPoint([]QuotaPoint{a}, b)) != 2 {
				t.Fatal("reset folded into previous cycle")
			}
		})
	}
}

// TestMergeQuotaHistory: another computer's points join this one's: the
// union, sorted, two within half a minute of each other the newer one,
// and merging the same again changes nothing.
func TestMergeQuotaHistory(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	r := t0.Add(4 * time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	noteQuotaHistory([]SubscriptionQuota{histCard("a@x.com", 10, r, 0, r)}, at(0))
	noteQuotaHistory([]SubscriptionQuota{histCard("a@x.com", 30, r, 0, r)}, at(20))

	other := quotaHist{
		"codex|a@x.com": {"5 hours": {
			{At: at(10), Left: 80, ResetsAt: &r},
			{At: at(20).Add(10 * time.Second), Left: 69, ResetsAt: &r}, // near this one's: the newer stays
			{At: at(40), Left: 60, ResetsAt: &r},
		}},
		"claude|b@y.com": {"Weekly": {{At: at(5), Left: 50}}},
	}
	data, _ := json.Marshal(other)
	if err := MergeQuotaHistory(data, at(41)); err != nil {
		t.Fatal(err)
	}
	five := lineOf(t, QuotaHistories(time.Time{}, "", ""), "a@x.com", "5 hours")
	lefts := []float64{}
	for _, p := range five {
		lefts = append(lefts, p.Left)
	}
	if len(lefts) != 4 || lefts[0] != 90 || lefts[1] != 80 || lefts[2] != 69 || lefts[3] != 60 {
		t.Fatalf("merged = %v", lefts)
	}
	lineOf(t, QuotaHistories(time.Time{}, "", ""), "b@y.com", "Weekly")

	before := string(QuotaHistoryData())
	if err := MergeQuotaHistory(data, at(41)); err != nil {
		t.Fatal(err)
	}
	if string(QuotaHistoryData()) != before {
		t.Error("merging the same points again changed the file")
	}
	if err := MergeQuotaHistory([]byte("{nope"), at(41)); err == nil {
		t.Error("a broken file merged")
	}
}

// max2020 on Discord: an Antigravity card had no curve and no "runs out
// at this pace" line, as its rows are its pools' windows, which are aside
// for routing, and its models' own windows are a family's. Each pool's
// window is kept under its pool's name, as the card's line looks it up;
// the models' windows still aren't. Another measured window set aside for
// routing keeps its own line too (chowyuan1314, #4612).
func TestQuotaHistoryKeepsAntigravityPools(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	r5, rw := t0.Add(3*time.Hour), t0.Add(4*24*time.Hour)
	card := func(gemini float64) []SubscriptionQuota {
		return []SubscriptionQuota{{Provider: "antigravity", Name: "Antigravity", User: "m@x.com", Windows: []QuotaWindow{
			{Name: "Gemini 3.8 Flash (Medium)", Model: "gemini-3.8-flash-medium", Family: "Gemini", Pool: "Gemini", Used: gemini, ResetsAt: &r5},
			{Name: "Claude Sonnet 4.6 (Thinking)", Model: "claude-sonnet-4-6-thinking", Family: "Claude", Pool: "Claude & GPT", Used: 40, ResetsAt: &r5},
			{Name: "5 hours", Pool: "Gemini", Span: 5 * time.Hour, Aside: true, Used: gemini, ResetsAt: &r5},
			{Name: "7 days", Pool: "Gemini", Span: 7 * 24 * time.Hour, Aside: true, Used: gemini / 2, ResetsAt: &rw},
			{Name: "5 hours", Pool: "Claude & GPT", Span: 5 * time.Hour, Aside: true, Used: 40, ResetsAt: &r5},
			{Name: "Extra", Aside: true, Used: 10, ResetsAt: &rw},
		}}}
	}
	noteQuotaHistory(card(10), t0)
	noteQuotaHistory(card(30), t0.Add(time.Hour))
	hs := QuotaHistories(t0.Add(-time.Hour), "", "")
	if len(hs) != 1 || len(hs[0].Lines) != 4 {
		t.Fatalf("histories = %+v, want the three pool windows' lines and Extra", hs)
	}
	five := lineOf(t, hs, "m@x.com", "Gemini · 5 hours")
	if len(five) != 2 || five[0].Left != 90 || five[1].Left != 70 || five[0].Start == nil || !five[0].Start.Equal(r5.Add(-5*time.Hour)) {
		t.Errorf("Gemini · 5 hours = %+v", five)
	}
	if week := lineOf(t, hs, "m@x.com", "Gemini · 7 days"); len(week) != 2 || week[1].Left != 85 || !week[0].Start.Equal(rw.Add(-7*24*time.Hour)) {
		t.Errorf("Gemini · 7 days = %+v", week)
	}
	if cl := lineOf(t, hs, "m@x.com", "Claude & GPT · 5 hours"); len(cl) != 2 || cl[0].Left != 60 {
		t.Errorf("Claude & GPT · 5 hours = %+v", cl)
	}
	if extra := lineOf(t, hs, "m@x.com", "Extra"); len(extra) != 2 || extra[0].Left != 90 || extra[1].Left != 90 {
		t.Errorf("Extra = %+v", extra)
	}
}
