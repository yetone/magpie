package usage

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

func TestGatewayCorrelation(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	r := Record{Time: at, Provider: "relay", Agent: "codex", Session: "override", NativeSession: "native", Input: 10, Output: 5, Millis: 1000, Status: 200}
	c := sessions.Call{Time: at.Add(time.Second), Agent: "codex", Session: "native", Tokens: sessions.Tokens{Input: 10, Output: 5}}
	cases := []struct {
		name string
		recs []Record
		logs []sessions.Call
		want int
	}{
		{"native header despite override", []Record{r}, []sessions.Call{c}, 1},
		{"later direct call stays", []Record{r}, []sessions.Call{c, {Time: at.Add(20 * time.Second), Agent: "codex", Session: "native", Tokens: c.Tokens}}, 1},
		{"different tokens stay", []Record{r}, []sessions.Call{{Time: c.Time, Agent: c.Agent, Session: c.Session, Tokens: sessions.Tokens{Input: 11, Output: 5}}}, 0},
		{"ambiguous file calls stay", []Record{r}, []sessions.Call{c, c}, 0},
		{"ambiguous gateway records stay", []Record{r, r}, []sessions.Call{c}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(gatewayMatches(tc.recs, tc.logs)); got != tc.want {
				t.Fatalf("matched %d, want %d", got, tc.want)
			}
		})
	}
	r.RequestID, c.RequestID = "req_same", "req_same"
	c.Session, c.Time = "other", at.Add(10*time.Second)
	if !gatewayMatches([]Record{r}, []sessions.Call{c})[0] {
		t.Fatal("matching request ID must work independently of session/time")
	}
	c.RequestID = "req_direct"
	if len(gatewayMatches([]Record{r}, []sessions.Call{c})) != 0 {
		t.Fatal("distinct request IDs must stay")
	}
}

func TestRejectedOnlyInRequestRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	recs := []Record{{Time: now, Agent: "codex", Provider: "relay", Model: "m", Status: 200, Input: 2}, {Time: now, Agent: "codex", Status: 404, Error: "unknown model", Rejected: true}, {Time: now, Agent: "codex", Status: 400}}
	rows, sum, _ := ledger(time.Time{}, Filter{}, recs)
	if len(rows) != 3 || sum.Calls != 1 || sum.Errors != 0 {
		t.Fatalf("rows %d, totals %+v", len(rows), sum)
	}
	if s := summarize(Today, now, recs); s.Calls != 1 || s.Errors != 0 || len(s.Models) != 1 {
		t.Fatalf("overview %+v", s)
	}
	_, series := LedgerSeries(Today, rows)
	calls := 0
	for _, p := range series {
		calls += p.Calls
	}
	if calls != 1 {
		t.Fatalf("chart counted %d", calls)
	}
	if by := Breakdown(rows, "provider"); len(by) != 1 || by[0].ID != "relay" {
		t.Fatalf("breakdown %+v", by)
	}
}

func TestCalendarBucketsAcrossDST(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []struct {
		month      time.Month
		day, hours int
	}{{time.March, 8, 23}, {time.November, 1, 25}} {
		now := time.Date(2026, date.month, date.day, 23, 30, 0, 0, loc)
		since, bucket, pts := timeline(Today, now, time.Time{})
		if len(pts) != date.hours || bucketIndex(bucket, since, now) != len(pts)-1 {
			t.Fatalf("hourly DST: %d points, bucket %d", len(pts), bucketIndex(bucket, since, now))
		}
		next := now.AddDate(0, 0, 1)
		start, bucket, pts := timeline(Week, next, time.Time{})
		r := Record{Time: next, Provider: "p", Input: 1, Status: 200}
		if bucketIndex(bucket, start, next) != 6 {
			t.Fatal("next calendar day is in the wrong bucket")
		}
		s := summarize(Week, next, []Record{r})
		if s.Series[len(pts)-1].Calls != 1 {
			t.Fatal("overview bucket lost the call")
		}
		first := now.AddDate(0, 0, -90)
		start, bucket, pts = timeline(All, next, first)
		if bucket != "week" || bucketIndex(bucket, start, next) != len(pts)-1 {
			t.Fatal("weekly calendar bucket mismatch")
		}
	}
}

// Where the clocks go forward at midnight, that day begins at 01:00 and has
// 23 hours. West of UTC (Santiago, Havana, the Azores) time.Date puts its
// 00:00 at 23:00 the day before; east of UTC (Beirut, Cairo) it puts it at
// 01:00, and AddDate from there kept 01:00 for the days around it. Each
// period, and each hour or day of its chart, begins when that day or hour
// does, and a call is counted and charted in the one it was made in.
func TestCalendarSkippedMidnight(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	const hour, day = time.Hour, 24 * time.Hour
	for _, c := range []struct {
		zone  string
		begin time.Time // when the day whose 00:00 the clocks skip begins
	}{
		{"America/Santiago", time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{"America/Havana", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)},
		{"Atlantic/Azores", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		{"Asia/Beirut", time.Date(2026, 3, 28, 22, 0, 0, 0, time.UTC)},
		{"Africa/Cairo", time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC)},
	} {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		begin := c.begin.In(loc)
		y, m, d := begin.Date()
		days := func(from, n int) (labels []string) {
			for i := range n {
				labels = append(labels, time.Date(y, m, d+from+i, 12, 0, 0, 0, time.UTC).Format("Jan 2"))
			}
			return labels
		}
		weeks := func(from, n int) (labels []string) {
			for i := range n {
				labels = append(labels, days(from+7*i, 1)...)
			}
			return labels
		}
		hours := func(from int) (labels []string) {
			for h := from; h < 24; h++ {
				labels = append(labels, fmt.Sprintf("%02d", h))
			}
			return labels
		}
		// a long All goes by week, from the Monday of the day's week
		monday := -time.Duration((int(begin.Weekday())+6)%7) * day
		// now, since and last are from begin: when the period is asked
		// for, when it begins, and a call in its chart's last point; on is
		// the point of a call 12 hours into the day, -1 for a period without
		// it
		for _, k := range []struct {
			what             string
			p                Period
			now, since, last time.Duration
			labels           []string
			on               int
		}{
			{"today", Today, 12 * hour, 0, 22*hour + 30*time.Minute, hours(1), 12},
			{"the day before", Today, -12 * hour, -day, -30 * time.Minute, hours(0), -1},
			{"7 days to the day", Week, 12 * hour, -6 * day, 12 * hour, days(-6, 7), 6},
			{"7 days across the day", Week, 3*day + 12*hour, -3 * day, 3*day + 12*hour, days(-3, 7), 3},
			{"7 days from the day", Week, 6*day + 12*hour, 0, 6*day + 12*hour, days(0, 7), 0},
			{"30 days to the day", Month, 12 * hour, -29 * day, 12 * hour, days(-29, 30), 29},
			{"30 days from the day", Month, 29*day + 12*hour, 0, 29*day + 12*hour, days(0, 30), 0},
			{"all from the day", All, 6*day + 12*hour, 0, 6*day + 12*hour, days(0, 7), 0},
			{"all by week from the day", All, 70*day + 12*hour, monday, 70*day + 12*hour, weeks(int(monday/day), 11), 0},
		} {
			now, since := begin.Add(k.now), begin.Add(k.since)
			call := func(at time.Time) Record { return Record{Time: at, Provider: "p", Input: 1, Status: 200} }
			// a call as the period begins (All's first, on the day), one
			// in its last point, one 12 hours into the day and, but for All,
			// which begins with the first call, one just before the
			// period, which isn't counted
			first := since
			if k.p == All {
				first = begin
			}
			recs := []Record{call(first.Add(30 * time.Minute)), call(begin.Add(k.last))}
			want := make([]int, len(k.labels))
			want[0]++
			want[len(want)-1]++
			if k.on >= 0 {
				recs = append(recs, call(begin.Add(12*hour)))
				want[k.on]++
			}
			counted := len(recs)
			if k.p != All {
				recs = append(recs, call(since.Add(-30*time.Minute)))
				if got := k.p.Since(now); !got.Equal(since) {
					t.Errorf("%s, %s (%s): the period begins at %s, want %s", c.zone, k.what, now, got, since)
				}
			}
			s := summarize(k.p, now, recs)
			var labels []string
			var charted []int
			for _, pt := range s.Series {
				labels = append(labels, pt.Label)
				charted = append(charted, pt.Calls)
			}
			if !s.Since.Equal(since) || !slices.Equal(labels, k.labels) {
				t.Errorf("%s, %s (%s): the chart begins at %s with points %v, want %s with %v", c.zone, k.what, now, s.Since, labels, since, k.labels)
			}
			if s.Calls != counted || !slices.Equal(charted, want) {
				t.Errorf("%s, %s (%s): %d calls counted, charted %v, want %d, charted %v", c.zone, k.what, now, s.Calls, charted, counted, want)
			}
		}
	}
}

func TestUsageLogIncrementalAndReplacement(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	Append(Record{Model: "first"})
	if len(Load(time.Time{})) != 1 {
		t.Fatal("initial read")
	}
	f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"model":"second"`)
	f.Close()
	if len(Load(time.Time{})) != 1 {
		t.Fatal("partial line was consumed")
	}
	f, _ = os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("}\n")
	f.Close()
	if rs := Load(time.Time{}); len(rs) != 2 || rs[1].Model != "second" {
		t.Fatalf("completed line %+v", rs)
	}
	replacement := filepath.Join(dir, "replacement")
	os.WriteFile(replacement, []byte("{\"model\":\"replacement\"}\n"), 0600)
	os.Rename(replacement, Path())
	if rs := Load(time.Time{}); len(rs) != 1 || rs[0].Model != "replacement" {
		t.Fatalf("replacement %+v", rs)
	}
}

func TestZeroTokenFailuresDeduplicateAndRejectedStayVisible(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	at := time.Now()
	r := Record{Time: at, Agent: "claude", Provider: "relay", Model: "m", Session: "s", Status: 429, Error: "rate limited", Millis: 1000}
	c := sessions.Call{Time: at.Add(time.Second), Agent: "claude", Session: "s", Error: "rate_limit"}
	if len(gatewayMatches([]Record{r}, []sessions.Call{c})) != 1 {
		t.Fatal("zero-token failure counted twice")
	}
	if len(gatewayMatches([]Record{r, r}, []sessions.Call{c})) != 0 {
		t.Fatal("ambiguous retries guessed")
	}
	r.Status, r.Error = 200, ""
	if len(gatewayMatches([]Record{r}, []sessions.Call{c})) != 0 {
		t.Fatal("failure matched an empty success")
	}
	r.Status, r.Provider, r.Rejected = 404, "", true
	if len(gatewayMatches([]Record{r}, []sessions.Call{c})) != 0 {
		t.Fatal("local rejection consumed a real API call")
	}
	Append(r)
	if len(Vias(time.Time{})) != 0 {
		t.Fatal("rejected request appears as a session provider")
	}
	Append(Record{Time: at, Agent: "claude", Provider: "relay", Model: "m", Session: "s", Status: 200})
	Append(Record{Time: at, Agent: "claude", Model: "m", Session: "s", Status: 400}) // legacy rejection
	if vias := Vias(time.Time{}); len(vias) != 1 || len(vias["claude|s"]) != 1 || vias["claude|s"][0].Calls != 1 {
		t.Fatalf("session via includes a local rejection: %+v", vias)
	}
	var out bytes.Buffer
	if err := WriteCSV(&out, []Row{{Record: r}}); err != nil {
		t.Fatal(err)
	}
	csvRows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if col := slices.Index(csvRows[0], "rejected"); col < 0 || csvRows[1][col] != "true" {
		t.Fatal("CSV omits rejection")
	}
}
