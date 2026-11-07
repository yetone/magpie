package provider

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// A Volcengine Agent Plan's month (20,000) is ranked by its week (7,000)
// once the weeks still to come before the month renews can't carry what
// the month has left: what the week leaves at its reset is lost then
// (#5639, louisgreen0726's reading on v0.1.3260: 1,628 used of the week
// and of the month, the week renewing in 23.6 hours, the month in 696).
// While they can, the month is the real cap (#1778) and ranks it.
func TestMonthOutOfReachRanksByWeek(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	day, week := 24*time.Hour, 7*24*time.Hour
	weekRenews := now.Add(23*time.Hour + 36*time.Minute)
	// the Agent Plan's GetAFPUsage, its resets in milliseconds
	plan := func(weekUsed, monthUsed float64, month time.Time) Allowance {
		t.Helper()
		ws, _ := volcReadAFP(json.RawMessage(fmt.Sprintf(`{"Tier":"Pro",
			"AFPFiveHour":{"Quota":2000,"Used":0},
			"AFPWeekly":{"Quota":7000,"Used":%v,"ResetTimestamp":%d},
			"AFPMonthly":{"Quota":20000,"Used":%v,"ResetTimestamp":%d}}`,
			weekUsed, weekRenews.UnixMilli(), monthUsed, month.UnixMilli())))
		if len(ws) != 3 {
			t.Fatalf("windows: %+v", ws)
		}
		return allowanceOf(ws, now)
	}
	for _, c := range []struct {
		name  string
		a     Allowance
		want  int
		first time.Time // the renewal Smart ranks it by
	}{
		// the reading as reported: four weeks to go hold 28,017, more
		// than the 18,372 left, so the month still decides
		{"as reported", plan(1628, 1628, now.Add(696*time.Hour)), 0, now.Add(696 * time.Hour)},
		// three weeks to go: 21,017 still carry 18,372
		{"three weeks to go", plan(0, 1628, weekRenews.Add(3*week)), 0, weekRenews.Add(3 * week)},
		// two weeks and a bit to go: 14,017 can't, so what this week
		// leaves is lost at its reset, and the week ranks it
		{"two weeks to go", plan(0, 1628, weekRenews.Add(2*week+24*time.Minute)), 1, weekRenews},
		// the last week: whatever is left past 7,000 is lost anyway,
		// and the week ranks it all the more
		{"the last week", plan(1628, 1628, weekRenews.Add(day)), 1, weekRenews},
		// a month holding less than its weeks add up to, with less left
		// than the weeks to come carry (#1778): the month, as today
		{"month within reach", plan(0, 11000, weekRenews.Add(2*week)), 0, weekRenews.Add(2 * week)},
	} {
		got := c.a.Unreached("doubao-seed-2.0-pro", now)
		if got != c.want {
			t.Errorf("%s: unreached %d, want %d", c.name, got, c.want)
		}
		if r := c.a.Renewal("doubao-seed-2.0-pro", now); len(r) != 3 || !r[got].Equal(c.first) {
			t.Errorf("%s: ranked by %v, want %v", c.name, r, c.first)
		}
	}

	// nothing else changes: a window alone, windows that tell only a
	// share, and counts in different units are never compared
	month := now.Add(2*week + day)
	for name, a := range map[string]Allowance{
		"one window": {{Used: 10, Resets: month, Span: 30 * day, Amount: 2000, Of: 20000}},
		"shares only": {
			{Used: 10, Resets: month, Span: 30 * day},
			{Used: 0, Resets: weekRenews, Span: week},
		},
		"other units": {
			{Used: 10, Resets: month, Span: 30 * day, Amount: 2000, Of: 20000, Unit: "credits"},
			{Used: 0, Resets: weekRenews, Span: week, Of: 7000, Unit: "USD"},
		},
		// the next window a rate cap: what five hours leave isn't lost
		"five hours under a week": {
			{Used: 10, Resets: now.Add(6 * time.Hour), Span: week, Amount: 700, Of: 7000},
			{Used: 0, Span: 5 * time.Hour, Of: 2000},
		},
	} {
		if got := a.Unreached("m", now); got != 0 {
			t.Errorf("%s: unreached %d", name, got)
		}
	}
}
