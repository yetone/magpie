package provider

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// wbCard is a WorkBuddy card as the built-in and its plugin both make it:
// one Credits window, "used / granted".
func wbCard(provider, user string, used, total float64) SubscriptionQuota {
	return SubscriptionQuota{Provider: provider, Name: "WorkBuddy", User: user, Plan: "Free",
		Windows: []QuotaWindow{{Name: "Credits", Used: 100 * used / total, Display: compactNumber(used) + " / " + compactNumber(total)}}}
}

func dailyOf(t *testing.T, qs []SubscriptionQuota, user string) *DailyCredits {
	t.Helper()
	for _, q := range qs {
		if q.User == user {
			return q.Daily
		}
	}
	t.Fatalf("no card of %s", user)
	return nil
}

// TestDailyCredits: each reading counts what was used since the one
// before on its own day; a cycle started again, a pack gone or added,
// an error and a reading kept from before count nothing, and the days
// come back with the card, oldest first (#568).
func TestDailyCredits(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.Local) }
	steps := []struct {
		at   time.Time
		used float64
		tot  float64
	}{
		{day(1, 9), 100, 5600},    // the first reading: counting starts here
		{day(1, 10), 112.5, 5600}, // 12.5
		{day(1, 23), 130.25, 5600},
		{day(2, 8), 130.25, 5600}, // nothing used
		{day(2, 9), 160.25, 5650}, // 30, and a check-in's 50 granted
		{day(3, 1), 4, 5600},      // the cycle started again: not counted
		{day(3, 2), 10, 5600},     // 6
		{day(5, 12), 70, 5600},    // 60 while magpie wasn't reading: on the 5th
	}
	for _, s := range steps {
		noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy", "李雷", s.used, s.tot)}, s.at)
	}
	// another account, the AI build's through its plugin; an error and a
	// card kept from before count nothing; another vendor isn't counted
	noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy-ai", "138****5678", 10, 3000)}, day(4, 9))
	noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy-ai", "138****5678", 15, 3000)}, day(4, 10))
	errd := wbCard("workbuddy-ai", "138****5678", 999, 3000)
	errd.Error = "boom"
	asOf := day(4, 11)
	kept := wbCard("workbuddy-ai", "138****5678", 900, 3000)
	kept.AsOf = &asOf
	noteDailyCredits([]SubscriptionQuota{errd, kept, wbCard("cursor", "x", 5, 10)}, day(4, 12))
	noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy-ai", "138****5678", 18, 3000)}, day(4, 13))

	cards := []SubscriptionQuota{wbCard("workbuddy", "李雷", 70, 5600), wbCard("workbuddy-ai", "138****5678", 18, 3000), wbCard("cursor", "x", 5, 10)}
	got := withDailyCredits(cards, day(5, 13))
	want := &DailyCredits{Since: "2026-10-01", Days: []DayCredits{{"2026-10-01", 30.25}, {"2026-10-02", 30}, {"2026-10-03", 6}, {"2026-10-05", 60}}}
	if d := dailyOf(t, got, "李雷"); !reflect.DeepEqual(d, want) {
		t.Errorf("WorkBuddy's days: %+v, want %+v", d, want)
	}
	want = &DailyCredits{Since: "2026-10-04", Days: []DayCredits{{"2026-10-04", 8}}}
	if d := dailyOf(t, got, "138****5678"); !reflect.DeepEqual(d, want) {
		t.Errorf("WorkBuddy AI's days: %+v, want %+v", d, want)
	}
	if d := dailyOf(t, got, "x"); d != nil {
		t.Errorf("another vendor's card has days: %+v", d)
	}
	if cards[0].Daily != nil {
		t.Error("the cards given were changed")
	}
	// what is kept: two figures a reading and the days' sums, no more
	b, err := os.ReadFile(creditsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 2000 {
		t.Errorf("the file is %d bytes", len(b))
	}
	// days long gone are dropped from the file at the next reading, and a
	// card isn't told of days older than creditsShown
	noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy", "李雷", 71, 5600)}, day(5, 14).AddDate(1, 2, 0))
	got = withDailyCredits(cards[:1], day(5, 15).AddDate(1, 2, 0))
	if d := dailyOf(t, got, "李雷"); len(d.Days) != 1 || d.Days[0].Used != 1 || d.Since != "2026-10-01" {
		t.Errorf("a year on: %+v", d)
	}
}
