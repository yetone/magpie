package provider

import (
	"fmt"
	"testing"
	"time"
)

// 0xBCD18E on X: several Kimi Code keys whose 5h and 7d windows reset
// together, and a month that differs. Routing weighs a key by every
// window it reads, so the month ranks it (Renewal, biggest first) and
// sets its pace; a key told no month is ranked by its week as before.
func TestKimiMonthRanksKeysWhoseShortWindowsTie(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	five, week := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	key := func(monthUsed float64, monthResets time.Time) Allowance {
		t.Helper()
		month := ""
		if monthUsed >= 0 {
			month = fmt.Sprintf(`,"limit_month_total":{"used_ratio":%v,"reset_time":%q}`, monthUsed, monthResets.UTC().Format(time.RFC3339))
		}
		_, ws, err := readKimiCode([]byte(fmt.Sprintf(`{"usages":{"limit_5h":{"used_ratio":0.2,"reset_time":%q},
			"limit_7d":{"used_ratio":0.3,"reset_time":%q}%s}}`,
			five.UTC().Format(time.RFC3339), week.UTC().Format(time.RFC3339), month)))
		if err != nil {
			t.Fatal(err)
		}
		return allowanceOf(ws, now)
	}
	early, late := key(0.5, now.Add(10*24*time.Hour)), key(0.5, now.Add(20*24*time.Hour))
	none := key(-1, time.Time{})
	if r := early.Renewal("kimi-for-coding", now); len(r) != 3 || !r[0].Equal(now.Add(10*24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("month does not rank the key: %v", r)
	}
	if e, l := early.Renewal("kimi-for-coding", now)[0], late.Renewal("kimi-for-coding", now)[0]; !e.Before(l) {
		t.Fatalf("renewals %v %v", e, l)
	}
	// the same share left of the month, spent over fewer days: more to lose per hour
	pe, _ := early.Pace("kimi-for-coding", now)
	pl, _ := late.Pace("kimi-for-coding", now)
	if pe <= pl {
		t.Fatalf("pace early %v late %v", pe, pl)
	}
	// a month used far more than the week: the month binds
	heavy := key(0.95, now.Add(10*24*time.Hour))
	if p, _ := heavy.Pace("kimi-for-coding", now); p >= pe {
		t.Fatalf("heavy month pace %v, not below %v", p, pe)
	}
	// no month told: two windows, as before
	if r := none.Renewal("kimi-for-coding", now); len(r) != 2 {
		t.Fatalf("no month: %v", r)
	}
}
