package usage

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestRequestPageDay(t *testing.T) {
	pageHome(t)
	start := Today.Since(holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))).AddDate(0, 0, -1)
	end := start.AddDate(0, 0, 1)
	day := start.Format(time.DateOnly)
	gateway := &rowChunk{}
	all := Ledgered{Agents: []string{"codex"}, Providers: []string{"a", "b"}}
	for i, r := range []Record{
		{Time: start.Add(-time.Nanosecond), Provider: "b", Input: 100},
		{Time: start, Provider: "a", Input: 10},
		{Time: start.Add(12 * time.Hour).UTC(), Provider: "a", Input: 20, Status: 429},
		{Time: end.Add(-time.Nanosecond), Provider: "b", Input: 30},
		{Time: end, Provider: "b", Input: 200},
		{Time: start, Provider: "a", Input: 999, Rejected: true},
	} {
		r.Agent, r.Model = "codex", "m"
		row := Row{Record: r, Cost: float64(r.Input) / 100, Priced: true}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
		if !r.IsRejected() {
			all.Sum.addRow(row)
		}
	}
	for name, query := range map[string]func(Filter, int) RequestPage{
		"compact": func(f Filter, offset int) RequestPage { return buildRequestPage(Week, f, offset, 1, gateway, nil) },
		"ledger":  func(f Filter, offset int) RequestPage { return pageFromLedger(Week, f, offset, 1, all) },
	} {
		t.Run(name, func(t *testing.T) {
			whole := query(Filter{}, 0)
			picked := query(Filter{Day: day}, 1)
			if picked.Total != 4 || len(picked.Rows) != 1 || picked.Sum.Calls != 3 || picked.Sum.Input != 60 || picked.Sum.Errors != 1 || math.Abs(picked.Sum.Cost-0.6) > 1e-9 {
				t.Fatalf("day's rows and totals: %+v", picked)
			}
			if !reflect.DeepEqual(picked.Series, whole.Series) || !reflect.DeepEqual(picked.ChartBy, whole.By) {
				t.Fatal("selecting a day changed the chart or its legend")
			}
			if by := picked.By["provider"]; len(by) != 2 || by[0].ID != "a" || by[0].Calls != 2 || by[1].ID != "b" || by[1].Calls != 1 {
				t.Fatalf("day's ranking: %+v", by)
			}
			filtered := query(Filter{Day: day, Provider: "a", Failed: true}, 0)
			if filtered.Total != 1 || filtered.Sum.Input != 20 || filtered.Rows[0].Status != 429 {
				t.Fatalf("day with other filters: %+v", filtered)
			}
			empty := query(Filter{Day: "1900-01-01"}, 0)
			if empty.Total != 0 || empty.Sum.Calls != 0 || len(empty.By["provider"]) != 0 || !reflect.DeepEqual(empty.Series, whole.Series) {
				t.Fatal("an empty day must keep the whole chart and clear the details")
			}
		})
	}
}

func TestDayFilterDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	testenv.Zone(t, loc)
	for _, day := range []string{"2026-03-08", "2026-11-01"} {
		start, err := time.ParseInLocation(time.DateOnly, day, loc)
		if err != nil {
			t.Fatal(err)
		}
		end := start.AddDate(0, 0, 1)
		f := Filter{Day: day}
		for _, at := range []time.Time{start, start.Add(time.Hour), end.Add(-time.Nanosecond)} {
			if !f.keeps(Record{Time: at.UTC()}) {
				t.Fatalf("%s lost %v", day, at)
			}
		}
		if f.keeps(Record{Time: start.Add(-time.Nanosecond)}) || f.keeps(Record{Time: end}) {
			t.Fatalf("%s included another day", day)
		}
	}
}
