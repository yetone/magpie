package provider

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// checkinAtSet saves at as the check-in's time of day.
func checkinAtSet(t *testing.T, at string) {
	t.Helper()
	s := settings.Load()
	s.CheckinAt = at
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// pluginCheckinClaims is a plugin provider whose one account's check-in
// claims at each ask, with until (none when nil) after it, counting asks.
func pluginCheckinClaims(id string, until func(time.Time) time.Time, now *time.Time, calls *int) pluginCheckinProvider {
	return pluginCheckinProvider{ID: id, accounts: []pluginCheckinAcct{{User: "ann", On: true, key: id + "|ann", card: id, do: func(context.Context) WorkBuddyCheckin {
		*calls++
		r := WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: 1}
		if until != nil {
			r.ValidUntil = until(*now)
		}
		return r
	}}}}
}

// loopAsks runs the daily loop a minute at a time for days from start and
// says when it asked, as the held clock read then.
func loopAsks(t *testing.T, c checkiner, now *time.Time, start time.Time, days int) []time.Time {
	t.Helper()
	var l wbCheckinLoop
	var asked []time.Time
	for m := time.Duration(0); m < time.Duration(days)*24*time.Hour; m += time.Minute {
		*now = start.Add(m)
		for _, r := range l.tick(context.Background(), c) {
			if r.Asked {
				asked = append(asked, *now)
			}
		}
	}
	return asked
}

// times_ang on X: 可以调整自动签到的时间吗. With a time of day set, the
// loop asks for each Beijing day at that time on the computer's clock,
// once, every day, in a zone ahead of Beijing's day and one behind it; a
// press asks before it; none set asks as the Beijing day turns.
func TestCheckinAtTimeOfDay(t *testing.T) {
	for _, tc := range []struct {
		name string
		zone *time.Location
		at   string
		want string // the Beijing time of each ask
		// catchUp: the first look asks, its Beijing day's time gone by
		catchUp bool
	}{
		{"Beijing at 09:00", time.FixedZone("UTC+8", 8*3600), "09:00", "09:00", false},
		// 23:30 New York (winter) is 12:30 the next Beijing day; at 03:00
		// New York, Beijing's 11-05 is past its 23:30 (11-04 New York)
		{"New York at 23:30", time.FixedZone("UTC-5", -5*3600), "23:30", "12:30", true},
		{"New York unset", time.FixedZone("UTC-5", -5*3600), "", "00:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claudeHome(t)
			checkinAtSet(t, tc.at)
			// 03:00 local on 11-05, the loop's first look
			start := time.Date(2026, 11, 5, 3, 0, 0, 0, tc.zone)
			now, calls := start, 0
			c := pluginCheckinClaims("fakeco", nil, &now, &calls).checkiner(func() time.Time { return now })
			asked := loopAsks(t, c, &now, start, 4)
			if len(asked) == 0 || asked[0].Equal(start) != tc.catchUp {
				t.Fatalf("asked at %v; at the first look, want %v", asked, tc.catchUp)
			}
			days := map[string]bool{}
			for i, a := range asked {
				if i == 0 && tc.catchUp {
					days[wbCheckinDay(a)] = true
					continue
				}
				if got := a.In(beijing).Format("15:04"); got != tc.want {
					t.Fatalf("asked at %s (Beijing %s), want Beijing %s; all: %v", a.Format(time.RFC3339), got, tc.want, asked)
				}
				if tc.at != "" && a.Format("15:04") != tc.at {
					t.Fatalf("asked at %s on the computer's clock, want %s", a.Format("15:04"), tc.at)
				}
				day := wbCheckinDay(a)
				if days[day] {
					t.Fatalf("asked twice on Beijing %s: %v", day, asked)
				}
				days[day] = true
			}
			// every Beijing day from the first ask's to the last's
			first, last := asked[0].In(beijing), asked[len(asked)-1].In(beijing)
			for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
				if !days[wbCheckinDay(d)] {
					t.Fatalf("Beijing %s skipped: %v", wbCheckinDay(d), asked)
				}
			}
			if len(days) < 3 {
				t.Fatalf("asked on %d days in 4: %v", len(days), asked)
			}
		})
	}

	t.Run("a press before it", func(t *testing.T) {
		claudeHome(t)
		checkinAtSet(t, "21:00")
		now := time.Date(2026, 11, 5, 8, 0, 0, 0, beijing)
		calls := 0
		c := pluginCheckinClaims("fakeco", nil, &now, &calls).checkiner(func() time.Time { return now })
		if rs := c.checkinNow(context.Background(), false); len(rs) != 0 || calls != 0 {
			t.Fatalf("the loop at 08:00 asked for 21:00: %+v", rs)
		}
		if rs := c.checkinNow(context.Background(), true); len(rs) != 1 || !rs[0].Asked || calls != 1 {
			t.Fatalf("a press at 08:00 didn't ask: %+v", rs)
		}
		// asked today, the loop leaves the claim be at 21:00
		now = now.Add(13 * time.Hour)
		if c.checkinNow(context.Background(), false); calls != 1 {
			t.Fatalf("asked again after a press's claim")
		}
	})
}

// A vendor whose day turns at 10:00 Beijing (a plugin's until, Qoder's)
// gets one ask in each of its days with the time set at 20:00: its turn
// falls before the time, and the claim at 20:00 is that day's.
func TestCheckinAtKeepsVendorDays(t *testing.T) {
	claudeHome(t)
	checkinAtSet(t, "20:00")
	tenNext := func(at time.Time) time.Time {
		b := at.In(beijing)
		u := time.Date(b.Year(), b.Month(), b.Day(), 10, 0, 0, 0, beijing)
		if !u.After(at) {
			u = u.AddDate(0, 0, 1)
		}
		return u
	}
	start := time.Date(2026, 11, 5, 3, 0, 0, 0, beijing)
	now, calls := start, 0
	c := pluginCheckinClaims("fakeco", tenNext, &now, &calls).checkiner(func() time.Time { return now })
	asked := loopAsks(t, c, &now, start, 4)
	var want []time.Time
	for d := range 4 {
		want = append(want, time.Date(2026, 11, 5+d, 20, 0, 0, 0, beijing))
	}
	if !slices.EqualFunc(asked, want, time.Time.Equal) {
		t.Fatalf("asked at %v, want %v", asked, want)
	}
	// each vendor day [10:00, 10:00) holds one
	for i := 1; i < len(asked); i++ {
		if asked[i].Sub(asked[i-1]) != 24*time.Hour {
			t.Fatalf("asks %v apart", asked[i].Sub(asked[i-1]))
		}
	}
}

// checkinGate is the time on the Beijing day, wherever the computer's
// clock is, and the day's start when none is set or a bad one is.
func TestCheckinGate(t *testing.T) {
	claudeHome(t)
	ny := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 11, 5, 1, 0, 0, 0, beijing) // 12:00 11-04 in New York
	for _, tc := range []struct {
		at   string
		now  time.Time
		want time.Time
	}{
		{"", now, time.Date(2026, 11, 5, 0, 0, 0, 0, beijing)},
		{"07:30", now, time.Date(2026, 11, 5, 7, 30, 0, 0, beijing)},
		{"7:30", now, time.Date(2026, 11, 5, 7, 30, 0, 0, beijing)},
		// the Beijing day 11-05 runs 11:00 11-04 to 11:00 11-05 in New York
		{"10:59", now.In(ny), time.Date(2026, 11, 5, 10, 59, 0, 0, ny)},
		{"11:00", now.In(ny), time.Date(2026, 11, 4, 11, 0, 0, 0, ny)},
	} {
		checkinAtSet(t, tc.at)
		if got := checkinGate(tc.now); !got.Equal(tc.want) {
			t.Errorf("%q at %v: %v, want %v", tc.at, tc.now, got, tc.want)
		}
	}
	s := settings.Load()
	s.CheckinAt = "25:00"
	if err := settings.Save(s); err == nil {
		t.Fatal("saved 25:00")
	}
}
