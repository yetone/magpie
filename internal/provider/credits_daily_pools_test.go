package provider

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// TestDailyCreditsPoolSetAside: a pool set aside is counted by the day as
// any other (jiabubu on Discord: WorkBuddy's credits a day said 0 while
// only its Extra credits were spent). WorkBuddy's card is the plugin's
// own for a China-site free account (CN_FREE_SUMMARY in the plugins repo's
// workbuddy/usage.test.mjs): the plan's Credits, and Extra credits set
// aside until the plan's are spent. A breakdown set aside (CodeArts'
// buckets, TestDailyCreditsEveryVendor) still counts nothing of its own.
func TestDailyCreditsPoolSetAside(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local) }
	wb := func(plan, extra float64, spent bool) SubscriptionQuota {
		pa, ea := "", `,"aside":true`
		if spent {
			pa, ea = `,"aside":true`, ""
		}
		return pluginWindowsCard(t, "workbuddy", "jiabubu", `[{"name":"Credits","used":`+jsonNum(100*plan/500)+`,"display":"`+jsonNum(plan)+` / 500","amount":`+jsonNum(plan)+`,"limit":500,"unit":"credits"`+pa+`},`+
			`{"name":"Extra credits","used":`+jsonNum(100*extra/2900)+`,"display":"`+jsonNum(extra)+` / 2900","amount":`+jsonNum(extra)+`,"limit":2900,"unit":"credits"`+ea+`}]`)
	}
	kiro := func(month, bonus float64) SubscriptionQuota {
		return pluginWindowsCard(t, "kiro", "k", `[{"name":"Credits","used":1,"display":"x","amount":`+jsonNum(month)+`,"limit":50,"unit":"credits","span":2592000},`+
			`{"name":"Bonus credits","used":1,"display":"x","amount":`+jsonNum(bonus)+`,"limit":500,"unit":"credits","aside":true}]`)
	}
	read := func(at time.Time, qs ...SubscriptionQuota) { noteDailyCredits(qs, at) }
	read(day(1, 9), wb(0, 281.9999965, false), kiro(50, 0))
	// only the extra credits move: WorkBuddy spends them first
	read(day(1, 12), wb(0, 290.5, false), kiro(50, 12))
	// the first reading of the shown pool alone: nothing yet
	if d := withDailyCredits([]SubscriptionQuota{wb(0, 290.5, false)}, day(1, 13))[0]; d.Daily == nil || len(d.MoreDaily) != 1 {
		t.Fatalf("after the extra credits moved: %+v %+v", d.Daily, d.MoreDaily)
	}
	read(day(2, 9), wb(20, 290.5, false), kiro(50, 30))
	// the plan's spent: the plugin sets its window aside instead
	read(day(2, 12), wb(500, 290.5, true))
	read(day(2, 15), wb(500, 300.5, true))

	got := withDailyCredits([]SubscriptionQuota{wb(500, 300.5, true), kiro(50, 30)}, day(2, 16))
	want := map[string][]*DailyCredits{
		"jiabubu": {
			{Since: "2026-11-01", Days: []DayCredits{{"2026-11-02", 500}}, Window: "Credits"},
			{Since: "2026-11-01", Days: []DayCredits{{"2026-11-01", 8.5}, {"2026-11-02", 10}}, Window: "Extra credits"},
		},
		"k": {
			{Since: "2026-11-01", Days: []DayCredits{}, Window: "Credits"},
			{Since: "2026-11-01", Days: []DayCredits{{"2026-11-01", 12}, {"2026-11-02", 18}}, Window: "Bonus credits"},
		},
	}
	for _, q := range got {
		all := append([]*DailyCredits{q.Daily}, q.MoreDaily...)
		if !reflect.DeepEqual(all, want[q.User]) {
			b, _ := json.Marshal(all)
			w, _ := json.Marshal(want[q.User])
			t.Errorf("%s's days: %s, want %s", q.Provider, b, w)
		}
	}
}

// TestDailyCreditsPoolsOfWhole: WorkBuddy's card since its plugin's
// 0.2.24 sets both pools aside, Credits and Extra credits, and Total
// credits, the two together, is what stops the account
// (packages/workbuddy usage.test.mjs, CN_FREE_SUMMARY: 0 / 500, 282 /
// 2900, 282 / 3400). yapechan on Discord: credits a day read 今天 0 for
// 积分 and 额外积分 and 34.06 for 积分合计, the pools taken for the total's
// breakdown. Each pool counts its own days, and they add up to the
// whole's.
func TestDailyCreditsPoolsOfWhole(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local) }
	win := func(name string, used, limit float64, aside bool) string {
		a := ""
		if aside {
			a = `,"aside":true`
		}
		return `{"name":"` + name + `","used":` + jsonNum(100*used/limit) + `,"display":"` + jsonNum(used) + ` / ` + jsonNum(limit) + `","amount":` + jsonNum(used) + `,"limit":` + jsonNum(limit) + `,"unit":"credits"` + a + `}`
	}
	wb := func(plan, extra float64) SubscriptionQuota {
		return pluginWindowsCard(t, "workbuddy", "yapechan", `[`+win("Credits", plan, 500, true)+`,`+win("Extra credits", extra, 2900, true)+`,`+win("Total credits", plan+extra, 3400, false)+`]`)
	}
	read := func(at time.Time, qs ...SubscriptionQuota) { noteDailyCredits(qs, at) }
	read(day(7, 8), wb(0, 281.9999965))
	// the extra credits move alone, then the plan's alone, then both
	read(day(7, 9), wb(0, 300))
	read(day(7, 10), wb(16.06, 300))
	read(day(7, 11), wb(20, 310))

	got := withDailyCredits([]SubscriptionQuota{wb(20, 310)}, day(7, 12))[0]
	all := append([]*DailyCredits{got.Daily}, got.MoreDaily...)
	sums := map[string]float64{}
	for _, d := range all {
		for _, x := range d.Days {
			sums[d.Window] += x.Used
		}
	}
	if len(all) != 3 || sums["Credits"] != 20 || sums["Extra credits"] != 28 || sums["Total credits"] != 48 {
		b, _ := json.Marshal(all)
		t.Fatalf("days: %s, want Credits 20 + Extra credits 28 = Total credits 48", b)
	}

	// a CodeArts-like bucket that is only part of its whole stays its
	// breakdown (TestDailyCreditsEveryVendor), even beside a second one
	part := func(total, a, b float64) SubscriptionQuota {
		return pluginWindowsCard(t, "codearts", "hw", `[`+win("总积分", total, 30000, false)+`,`+win("基础积分", a, 10000, true)+`,`+win("赠送积分", b, 5000, true)+`]`)
	}
	read(day(7, 8), part(100, 100, 0))
	read(day(7, 9), part(150, 150, 0))
	got = withDailyCredits([]SubscriptionQuota{part(150, 150, 0)}, day(7, 12))[0]
	if got.Daily == nil || got.Daily.Window != "总积分" || len(got.MoreDaily) != 0 {
		b, _ := json.Marshal(append([]*DailyCredits{got.Daily}, got.MoreDaily...))
		t.Fatalf("a partial breakdown's days: %s, want 总积分 alone", b)
	}
}
