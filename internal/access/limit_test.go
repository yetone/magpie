package access

import (
	"testing"
	"time"
)

func TestLimitWindow(t *testing.T) {
	loc := time.FixedZone("X", 8*3600)
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct{ period, now, start, reset string }{
		{"day", "2026-10-03 23:59", "2026-10-03 00:00", "2026-10-04 00:00"},
		{"day", "2026-12-31 00:00", "2026-12-31 00:00", "2027-01-01 00:00"},
		{"week", "2026-10-03 12:00", "2026-09-28 00:00", "2026-10-05 00:00"}, // a Saturday: from Monday
		{"week", "2026-10-04 23:00", "2026-09-28 00:00", "2026-10-05 00:00"}, // Sunday is the week's last day
		{"week", "2026-10-05 00:00", "2026-10-05 00:00", "2026-10-12 00:00"},
		{"month", "2026-10-31 18:00", "2026-10-01 00:00", "2026-11-01 00:00"},
		{"month", "2026-12-15 08:00", "2026-12-01 00:00", "2027-01-01 00:00"},
	} {
		start, reset := Window(c.period, at(c.now))
		if !start.Equal(at(c.start)) || !reset.Equal(at(c.reset)) {
			t.Errorf("%s at %s: %s – %s", c.period, c.now, start, reset)
		}
	}
}

// Some zones' clocks go forward at midnight, so that day, and a week or month
// that starts on it, begins at 01:00. West of UTC (Santiago, Havana, the
// Azores) time.Date puts the 00:00 they skip at 23:00 on the day before,
// which ended that window an hour early; east of UTC (Beirut) it puts it at
// 01:00, where the day does begin. Samoa skipped 2011-12-30 whole, and
// time.Date put its 00:00 at the 29th's, a day early.
func TestLimitWindowSkippedMidnight(t *testing.T) {
	for _, c := range []struct {
		zone, period string
		begin        time.Time // when a window begins, as the clocks go forward
	}{
		{"America/Santiago", "day", time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{"America/Havana", "day", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)},
		{"Atlantic/Azores", "day", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		{"Asia/Beirut", "day", time.Date(2026, 3, 28, 22, 0, 0, 0, time.UTC)},
		{"America/Sao_Paulo", "week", time.Date(1997, 10, 6, 3, 0, 0, 0, time.UTC)}, // a Monday
		{"America/Asuncion", "month", time.Date(2023, 10, 1, 4, 0, 0, 0, time.UTC)},
		{"Pacific/Apia", "day", time.Date(2011, 12, 30, 10, 0, 0, 0, time.UTC)}, // the 31st's 00:00 there
	} {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		// noon and 23:30 the day before are in the window that ends then
		for _, back := range []time.Duration{12 * time.Hour, 30 * time.Minute} {
			now := c.begin.Add(-back).In(loc)
			if start, reset := Window(c.period, now); !reset.Equal(c.begin) {
				t.Errorf("%s: the %s of %s is %s – %s, not ending at %s", c.zone, c.period, now, start, reset, c.begin.In(loc))
			}
		}
		for _, on := range []time.Duration{0, 12 * time.Hour} {
			now := c.begin.Add(on).In(loc)
			if start, reset := Window(c.period, now); !start.Equal(c.begin) {
				t.Errorf("%s: the %s of %s is %s – %s, not beginning at %s", c.zone, c.period, now, start, reset, c.begin.In(loc))
			}
		}
	}
}

func TestLimitValid(t *testing.T) {
	if l, err := (&Limit{Period: "day"}).Valid(); l != nil || err != nil {
		t.Fatal("no cap is no limit", l, err)
	}
	if l, err := (*Limit)(nil).Valid(); l != nil || err != nil {
		t.Fatal(l, err)
	}
	if l, err := (&Limit{Tokens: 5}).Valid(); err != nil || l.Period != "day" {
		t.Fatal("a period is a day unless said", l, err)
	}
	for _, bad := range []Limit{{Period: "hour", Tokens: 1}, {Period: "day", Tokens: -1}, {Period: "day", Cost: -2}} {
		if _, err := bad.Valid(); err == nil {
			t.Error("accepted", bad)
		}
	}
}
