package usage

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// The heatmap (#1369) is the last 53 weeks from a Monday, a day each by the
// local calendar: the rows the chart's filter keeps, the day picked set
// aside, without the gateway's own rejections, the days before its first
// Monday or after today. The streaming page and the ledger's agree.
func TestHeatmapDays(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local)) // a Friday
	from := time.Date(2025, 10, 6, 0, 0, 0, 0, time.Local)               // the Monday 52 weeks before this week's
	if got := heatmapSince(now); !got.Equal(from) || got.Weekday() != time.Monday {
		t.Fatalf("heatmap starts %v, want %v", got, from)
	}
	today := time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local)
	gateway := &rowChunk{}
	all := Ledgered{Agents: []string{"codex"}, Providers: []string{"a", "b"}}
	for i, r := range []Record{
		{Time: from.Add(-time.Nanosecond), Provider: "a", Input: 1000},             // the Sunday before: not shown
		{Time: from, Provider: "a", Input: 10, Output: 5, CacheRead: 100},          // the first day
		{Time: from.Add(23 * time.Hour).UTC(), Provider: "b", Input: 20},           // the same local day, stamped in UTC
		{Time: today.Add(-time.Nanosecond), Provider: "a", Input: 30, Status: 500}, // yesterday, failed: still a call
		{Time: today.Add(time.Hour), Provider: "a", Input: 40},
		{Time: today.Add(2 * time.Hour), Provider: "a", Input: 999, Rejected: true},
		{Time: today.Add(time.Hour), Provider: "a", Input: 7, Output: 1}, // no known price
	} {
		r.Agent, r.Model = "codex", "m"
		row := Row{Record: r, Cost: float64(r.Input) / 100, Priced: i != 6}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
		if !r.IsRejected() {
			all.Sum.addRow(row)
		}
	}
	for name, query := range map[string]func(Filter) RequestPage{
		"compact": func(f Filter) RequestPage { return buildRequestPage(heatmapPeriod, f, 0, 1, gateway, nil) },
		"ledger":  func(f Filter) RequestPage { return pageFromLedger(heatmapPeriod, f, 0, 1, all) },
	} {
		t.Run(name, func(t *testing.T) {
			h := query(Filter{}).Heat
			if h == nil {
				t.Fatal("no heatmap")
			}
			if h.From != "2025-10-06" || h.To != "2026-10-09" {
				t.Fatalf("span: %s to %s", h.From, h.To)
			}
			want := []Day{
				{Date: "2025-10-06", Calls: 2, Tokens: 135, Cost: 0.3},
				{Date: "2026-10-08", Calls: 1, Tokens: 30, Cost: 0.3},
				{Date: "2026-10-09", Calls: 2, Tokens: 48, Cost: 0.4, Unpriced: 1},
			}
			for i := range h.Days {
				h.Days[i].Cost = math.Round(h.Days[i].Cost*1e9) / 1e9
			}
			if !reflect.DeepEqual(h.Days, want) {
				t.Fatalf("days:\n got %+v\nwant %+v", h.Days, want)
			}
			// a provider picked: only its rows; a day picked: still every day
			if h := query(Filter{Provider: "b", Day: "2026-10-09"}).Heat; len(h.Days) != 1 || h.Days[0].Date != "2025-10-06" || h.Days[0].Tokens != 20 {
				t.Fatalf("provider b's days: %+v", h.Days)
			}
			if p := query(Filter{}); p.Heat == nil {
				t.Fatal("heatmap dropped")
			}
		})
	}
	if p := buildRequestPage(Month, Filter{}, 0, 1, gateway, nil); p.Heat != nil {
		t.Fatal("a page of another period has no heatmap")
	}
}

// HeatmapOf reads the log itself: a call appended today is today's, and the
// filter given narrows it.
func TestHeatmapOf(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local))
	for i, at := range []time.Time{now.AddDate(0, 0, -400), now.AddDate(0, -3, 0), now.Add(-time.Hour), now.Add(-time.Minute)} {
		Append(Record{Time: at, Agent: "claude", Provider: "openai", Model: "m", Input: 10 + i, Status: 200})
	}
	Append(Record{Time: now.Add(-time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100, Status: 200})
	h := HeatmapOf(Filter{Agent: "claude", Day: "2026-10-09"})
	want := []Day{{Date: now.AddDate(0, -3, 0).Format(time.DateOnly), Calls: 1, Tokens: 11}, {Date: "2026-10-09", Calls: 2, Tokens: 25}}
	for i := range h.Days {
		h.Days[i].Cost = 0
	}
	if h.From != "2025-10-06" || h.To != "2026-10-09" || !reflect.DeepEqual(h.Days, want) {
		t.Fatalf("heatmap: %+v", h)
	}
}
