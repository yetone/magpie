package dayzone

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no zone data for %s: %v", name, err)
	}
	return l
}

// The day starts at the hour: a call a minute before it belongs to the day
// before, the hour itself to the new day, in a zone without daylight saving
// and in ones that have it (north and south), across each of their changes.
func TestDayStartsAtTheHour(t *testing.T) {
	for _, name := range []string{"Asia/Shanghai", "America/New_York", "Australia/Sydney", "Pacific/Kiritimati", "UTC"} {
		loc := mustLoad(t, name)
		for _, hour := range []int{1, 4, 6} {
			sh := Shift(loc, hour)
			if Shift(loc, hour) != sh {
				t.Fatalf("%s %d: not the same location twice", name, hour)
			}
			if got := Offset(sh); got != time.Duration(hour)*time.Hour {
				t.Fatalf("%s %d: Offset = %v", name, hour, got)
			}
			y := time.Now().Year()
			for d := time.Date(y-1, 1, 1, 12, 0, 0, 0, loc); d.Year() <= y+1; d = d.AddDate(0, 0, 1) {
				start := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
				if start.Hour() != hour {
					continue // that wall hour is skipped on this date
				}
				day := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.UTC)
				before, after := start.Add(-time.Minute).In(sh), start.In(sh)
				if before.Format(time.DateOnly) != day.AddDate(0, 0, -1).Format(time.DateOnly) {
					t.Fatalf("%s h%d: %v is in %s, want the day before %s", name, hour, start.Add(-time.Minute), before.Format(time.DateOnly), day.Format(time.DateOnly))
				}
				if after.Format(time.DateOnly) != day.Format(time.DateOnly) || after.Hour() != 0 || after.Minute() != 0 {
					t.Fatalf("%s h%d: day start %v reads %v in the shifted zone", name, hour, start, after)
				}
			}
		}
	}
}

func TestNoShiftIsTheZoneItself(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	for _, h := range []int{-1, 0, MaxStart + 1} {
		if Shift(loc, h) != loc {
			t.Fatalf("hour %d shifted", h)
		}
	}
	if Offset(loc) != 0 {
		t.Fatal("a plain zone has an offset")
	}
}
