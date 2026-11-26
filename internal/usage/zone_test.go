package usage

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// viewerZone is a zone whose clock this computer's doesn't keep: the
// browser of `magpie web` read from elsewhere.
func viewerZone(t *testing.T) *time.Location {
	t.Helper()
	for _, name := range []string{"Asia/Shanghai", "America/Los_Angeles"} {
		if z := Zone(name); z != time.Local {
			return z
		}
	}
	t.Fatal("no zone apart from this computer's")
	return nil
}

// zoneApart is a moment now and a call before it that fall on one day in
// the reader's zone v and on two days in this computer's: the call is
// today for the reader and not for the computer.
func zoneApart(t *testing.T, v *time.Location) (now, call time.Time) {
	t.Helper()
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	day := func(at time.Time, l *time.Location) string { return at.In(l).Format(time.DateOnly) }
	for n := 0; n < 48; n++ {
		now = base.Add(time.Duration(n) * time.Hour)
		for h := 1; h < 24; h++ {
			call = now.Add(-time.Duration(h) * time.Hour)
			if day(call, v) == day(now, v) && day(call, time.Local) != day(now, time.Local) {
				return now, call
			}
		}
	}
	t.Fatalf("no moment where %v and %v keep different days", v, time.Local)
	return
}

// The Usage page's days and hours are the reader's: a call at 9:00 in
// UTC+8 read from a browser there shows at 09 and on that day, whatever
// zone the computer serving the page keeps (Outvi V on Discord). Without a
// zone, the computer's days, as before. Run under TZ=UTC and
// TZ=Asia/Shanghai.
func TestUsageReadsTheViewersZone(t *testing.T) {
	pageHome(t)
	v := viewerZone(t)
	now, call := zoneApart(t, v)
	holdClock(t, call)
	Append(Record{Agent: "claude", Provider: "openai", Model: "m", Input: 10, Status: 200}) // stamped at call
	holdClock(t, now.In(time.Local))

	hour, date := call.In(v).Format("15"), call.In(v).Format(time.DateOnly)
	labelOf := func(pts []Point, calls func(int) int) string {
		for i, p := range pts {
			if calls(i) > 0 {
				return p.Label
			}
		}
		return ""
	}
	t.Logf("computer %v, reader %v: call %v, now %v", time.Local, v, call.In(v), now.In(v))

	s := SummarizeTopIn(Today, 10, v)
	if s.Calls != 1 {
		t.Errorf("Summary of the reader's today: %d calls, want 1", s.Calls)
	}
	if got := labelOf(s.Series, func(i int) int { return s.Series[i].Calls }); got != hour {
		t.Errorf("Summary's hour: %q, want the reader's %q", got, hour)
	}
	if s := SummarizeTopIn(Today, 10, nil); s.Calls != 0 {
		t.Errorf("Summary of the computer's today: %d calls, want 0", s.Calls)
	}

	f := Filter{Zone: v}
	p := QueryPage(Today, f, 0, 10)
	if p.Total != 1 {
		t.Errorf("Requests in the reader's today: %d, want 1", p.Total)
	}
	pts := make([]Point, len(p.Series))
	for i := range p.Series {
		pts[i] = p.Series[i].Point
	}
	if got := labelOf(pts, func(i int) int { return p.Series[i].Calls }); got != hour {
		t.Errorf("Requests chart's hour: %q, want the reader's %q", got, hour)
	}
	if p := QueryPage(Today, Filter{}, 0, 10); p.Total != 0 {
		t.Errorf("Requests in the computer's today: %d, want 0", p.Total)
	}
	if l := LedgerOf(Today, f); len(l.Rows) != 1 {
		t.Errorf("ledger of the reader's today: %d rows, want 1", len(l.Rows))
	}
	if p := QueryPage(All, Filter{Zone: v, Day: date}, 0, 10); p.Total != 1 {
		t.Errorf("Requests on the reader's day %s: %d, want 1", date, p.Total)
	}

	// a week's chart is a day a point: the call's is the reader's day
	dayLabel := call.In(v).Format("Jan 2")
	w := SummarizeTopIn(Week, 10, v)
	if got := labelOf(w.Series, func(i int) int { return w.Series[i].Calls }); got != dayLabel {
		t.Errorf("Summary's day in the week: %q, want the reader's %q", got, dayLabel)
	}
	wp := QueryPage(Week, f, 0, 10)
	wpts := make([]Point, len(wp.Series))
	for i := range wp.Series {
		wpts[i] = wp.Series[i].Point
	}
	if got := labelOf(wpts, func(i int) int { return wp.Series[i].Calls }); got != dayLabel {
		t.Errorf("Requests chart's day in the week: %q, want the reader's %q", got, dayLabel)
	}

	h := HeatmapOf(f)
	if h.To != now.In(v).Format(time.DateOnly) || len(h.Days) != 1 || h.Days[0].Date != date {
		t.Errorf("heatmap: to %s, days %+v; want to %s and the call on %s", h.To, h.Days, now.In(v).Format(time.DateOnly), date)
	}
}

// Zone is one *time.Location for each name, so a Filter holding it is one
// cache key; a name this computer doesn't know, or one keeping its clock,
// is this computer's own zone.
func TestZoneNames(t *testing.T) {
	for _, name := range []string{"", "Local", "Not/AZone", "../../etc/passwd"} {
		if Zone(name) != time.Local {
			t.Errorf("Zone(%q) = %v, want this computer's", name, Zone(name))
		}
	}
	v := viewerZone(t)
	if Zone(v.String()) != v {
		t.Errorf("Zone(%q) twice gave two locations", v.String())
	}
	if tz := os.Getenv("TZ"); tz != "" {
		if Zone(tz) != time.Local {
			t.Errorf("Zone(%q), this computer's own zone, is not time.Local", tz)
		}
	}
}

// A name read while a test held time.Local in another zone is judged
// again under the time.Local after: Shanghai, read while the computer was
// held in Los Angeles' time, stayed apart from a time.Local in Shanghai's
// for every later reader, and a test that took it for another clock found
// no day between them (#7847).
func TestZoneJudgedUnderTheLocalOfTheRead(t *testing.T) {
	sh, err1 := time.LoadLocation("Asia/Shanghai")
	la, err2 := time.LoadLocation("America/Los_Angeles")
	if err1 != nil || err2 != nil {
		t.Skip(err1, err2)
	}
	testenv.Zone(t, sh)
	t.Run("held in Los Angeles", func(t *testing.T) {
		testenv.Zone(t, la)
		if Zone("Asia/Shanghai") == time.Local {
			t.Fatal("Shanghai is the computer's zone while it keeps Los Angeles' time")
		}
	})
	if Zone("Asia/Shanghai") != time.Local {
		t.Fatal("Shanghai, read under Los Angeles, is still apart from a computer in Shanghai's time")
	}
	if Zone("Asia/Shanghai") != Zone("Asia/Shanghai") {
		t.Fatal("two locations for one name")
	}
}
