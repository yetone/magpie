package provider

import (
	"context"
	"slices"
	"testing"
	"time"
)

// pluginCheckinFails is a plugin provider whose one account's check-in
// answers failed, with until (zero for none) after the ask, counting asks.
func pluginCheckinFails(id string, until func(time.Time) time.Time, now *time.Time, calls *int) pluginCheckinProvider {
	return pluginCheckinProvider{ID: id, accounts: []pluginCheckinAcct{{User: "ann", On: true, key: id + "|ann", card: id, do: func(context.Context) WorkBuddyCheckin {
		*calls++
		r := WorkBuddyCheckin{Outcome: CheckinFailed, Msg: "gift plans to claim: Too Many Requests"}
		if until != nil {
			r.ValidUntil = until(*now)
		}
		return r
	}}}}
}

// plugins#503 (kongbai01203), plugins#793: ZCode rate limits a claim read,
// and the plugin answers failed with an until hours away. magpie asked it
// again 30 minutes on all the same, which kept the limit on. A failed with
// an until later than its retry is not asked by the loop before the until,
// across the Beijing day's turn too; at most 24 hours after the failure; a
// press still asks; one with no until is asked again in 30 minutes.
func TestPluginCheckinFailedWaitsForUntil(t *testing.T) {
	for _, id := range []string{"zcode", "fakeco"} { // rechecked or not
		t.Run(id, func(t *testing.T) {
			claudeHome(t)
			// 22:00 Beijing: a 6h until is past the day's turn
			start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
			now, calls := start, 0
			sixHours := func(at time.Time) time.Time { return at.Add(6 * time.Hour) }
			p := pluginCheckinFails(id, sixHours, &now, &calls)
			c := p.checkiner(func() time.Time { return now })
			ask := func(press bool) WorkBuddyCheckin {
				t.Helper()
				got := c.checkinNow(context.Background(), press)
				if len(got) != 1 || got[0].Outcome != CheckinFailed {
					t.Fatalf("at %s: %+v", now, got)
				}
				return got[0]
			}
			ask(false)
			for _, at := range []time.Duration{31 * time.Minute, 2 * time.Hour, 3 * time.Hour, 6*time.Hour - time.Minute} {
				now = start.Add(at)
				if r := ask(false); r.Asked || calls != 1 {
					t.Fatalf("loop at +%v: asked again (%d asks), until %v", at, calls, r.ValidUntil)
				}
			}
			now = start.Add(6 * time.Hour)
			if r := ask(false); !r.Asked || calls != 2 {
				t.Fatalf("loop at the until: not asked (%d asks)", calls)
			}

			// a press asks while held
			now = start.Add(6*time.Hour + 31*time.Minute)
			if r := ask(true); !r.Asked || calls != 3 {
				t.Fatalf("press while held: not asked (%d asks)", calls)
			}

			// the loop's wake time is the until, not 30 minutes on
			var l wbCheckinLoop
			now = start.AddDate(0, 0, 2)
			calls = 0
			var asked []time.Duration
			for m := time.Duration(0); m <= 7*time.Hour; m += time.Minute {
				now = start.AddDate(0, 0, 2).Add(m)
				for _, r := range l.tick(context.Background(), c) {
					if r.Asked {
						asked = append(asked, m)
					}
				}
				if m == 0 && !l.again.Equal(sixHours(now)) {
					t.Fatalf("the loop wakes at %v, want the until %v", l.again, sixHours(now))
				}
			}
			if want := []time.Duration{0, 6 * time.Hour}; !slices.Equal(asked, want) {
				t.Fatalf("the loop asked at %v, want %v", asked, want)
			}
		})
	}
}

// An until a day or more away holds a failure 24 hours, no longer; a
// failure with no until, or one sooner than its retry, is asked as before.
func TestPluginCheckinFailedUntilBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		until func(time.Time) time.Time
		quiet []time.Duration // not asked again then
		again time.Duration   // asked again then
	}{
		{"a far until", func(at time.Time) time.Time { return at.AddDate(1, 0, 0) }, []time.Duration{31 * time.Minute, 23*time.Hour + 59*time.Minute}, 24 * time.Hour},
		{"no until", nil, []time.Duration{29 * time.Minute}, 30 * time.Minute},
		{"a sooner until", func(at time.Time) time.Time { return at.Add(2 * time.Minute) }, []time.Duration{time.Minute}, 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claudeHome(t)
			start := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC) // 14:00 Beijing
			now, calls := start, 0
			c := pluginCheckinFails("zcode", tc.until, &now, &calls).checkiner(func() time.Time { return now })
			c.checkinNow(context.Background(), false)
			for _, at := range tc.quiet {
				now = start.Add(at)
				if c.checkinNow(context.Background(), false); calls != 1 {
					t.Fatalf("at +%v: asked again", at)
				}
			}
			now = start.Add(tc.again)
			if got := c.checkinNow(context.Background(), false); calls != 2 || len(got) != 1 || !got[0].Asked {
				t.Fatalf("at +%v: not asked again (%d asks)", tc.again, calls)
			}
		})
	}
}
