package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func startAt(t *testing.T, hour int) {
	t.Helper()
	s := settings.Load()
	s.DayStart = hour
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// With a day that starts at 4 (ssharpcc on X, coding past 1am), a call at
// 01:30 is the day before's: not in Today, on the day before in the week's
// chart, the heatmap and a picked day, while one at 04:30 is today's, at
// hour 04. Without it, both are today's. Every clock is held, in a zone
// whatever this computer keeps; run under TZ=UTC and TZ=Pacific/Kiritimati.
func TestDayStartsAfterMidnight(t *testing.T) {
	pageHome(t)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip(err)
	}
	late := time.Date(2026, 9, 30, 1, 30, 0, 0, loc)
	early := time.Date(2026, 9, 30, 4, 30, 0, 0, loc)
	now := time.Date(2026, 9, 30, 5, 0, 0, 0, loc)
	holdClock(t, late)
	Append(Record{Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200})
	holdClock(t, early)
	Append(Record{Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200})
	holdClock(t, now)

	run := func(hour int) {
		startAt(t, hour)
		v := Zone("Asia/Shanghai")
		f := Filter{Zone: v}
		wantToday := 2
		if hour == 4 {
			wantToday = 1
		}
		if got := SummarizeTopIn(Today, 10, v).Calls; got != wantToday {
			t.Errorf("start %d: Summary Today has %d calls, want %d", hour, got, wantToday)
		}
		if got := QueryPage(Today, f, 0, 10).Total; got != wantToday {
			t.Errorf("start %d: Requests Today has %d, want %d", hour, got, wantToday)
		}
		if got := len(LedgerOf(Today, f).Rows); got != wantToday {
			t.Errorf("start %d: ledger Today has %d rows, want %d", hour, got, wantToday)
		}
		days := map[string]int{}
		for _, d := range HeatmapOf(f).Days {
			days[d.Date] = d.Calls
		}
		wantDays := map[string]int{"2026-09-30": 2}
		if hour == 4 {
			wantDays = map[string]int{"2026-09-29": 1, "2026-09-30": 1}
		}
		if len(days) != len(wantDays) || days["2026-09-29"] != wantDays["2026-09-29"] || days["2026-09-30"] != wantDays["2026-09-30"] {
			t.Errorf("start %d: heatmap days %v, want %v", hour, days, wantDays)
		}
		for date, want := range wantDays {
			if got := QueryPage(All, Filter{Zone: v, Day: date}, 0, 10).Total; got != want {
				t.Errorf("start %d: day %s has %d calls, want %d", hour, date, got, want)
			}
		}
		w := SummarizeTopIn(Week, 10, v)
		byLabel := map[string]int{}
		for _, p := range w.Series {
			if p.Calls > 0 {
				byLabel[p.Label] = p.Calls
			}
		}
		wantWeek := map[string]int{"Sep 30": 2}
		if hour == 4 {
			wantWeek = map[string]int{"Sep 29": 1, "Sep 30": 1}
		}
		if len(byLabel) != len(wantWeek) || byLabel["Sep 29"] != wantWeek["Sep 29"] || byLabel["Sep 30"] != wantWeek["Sep 30"] {
			t.Errorf("start %d: the week's chart has %v, want %v", hour, byLabel, wantWeek)
		}
		// Today's chart is a point an hour, labelled by the real hour of the day
		var hours []string
		for _, p := range w.Series {
			_ = p
		}
		for _, p := range SummarizeTopIn(Today, 10, v).Series {
			if p.Calls > 0 {
				hours = append(hours, p.Label)
			}
		}
		if hour == 4 && (len(hours) != 1 || hours[0] != "04") {
			t.Errorf("start 4: Today's chart hours %v, want [04]", hours)
		}
		if hour == 4 {
			if first := SummarizeTopIn(Today, 10, v).Series[0].Label; first != "04" {
				t.Errorf("start 4: Today's chart begins at %q, want 04", first)
			}
		}
	}
	run(0)
	run(4)
	run(0)
}
