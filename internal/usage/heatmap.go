package usage

// The heatmap (#1369): the last 53 weeks a day each, as a calendar of
// contributions lays them out, Monday on top. It is the request page's own
// index read over that span — QueryPage's cached chunks, priced once — and
// not another pass over the logs, so a page asking for it again reads only
// what changed. It counts what the Requests tab's chart counts: the rows its
// filters keep, but for the day picked, and not the calls the gateway turned
// away itself.

import (
	"slices"
	"time"
)

// HeatmapWeeks is how many weeks the heatmap shows, this one the last.
const HeatmapWeeks = 53

// heatmapPeriod is the span the heatmap is read over: QueryPage's period for
// it, from the Monday HeatmapWeeks-1 weeks before this week's. Not one the
// page's period switch offers.
const heatmapPeriod Period = "heatmap"

// heatmapSince is the Monday the heatmap starts on, as of now.
func heatmapSince(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7-7*(HeatmapWeeks-1))
}

// Day is one day's calls, by the local calendar day they fell on.
type Day struct {
	Date   string  `json:"date"` // YYYY-MM-DD
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"` // in, out and through the cache, as the chart counts them
	Cost   float64 `json:"cost"`   // of the priced calls, USD
	// Unpriced: calls with tokens but no known price, not in Cost
	Unpriced int `json:"unpriced,omitempty"`
}

// Heatmap is the days from From to To (today), both YYYY-MM-DD in the local
// time zone: only those with calls are listed, oldest first.
type Heatmap struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days []Day  `json:"days"`
}

// HeatmapOf is the last HeatmapWeeks weeks of the calls the filter keeps, a
// day each. The filter's Day is not one of its dimensions here: the heatmap
// is every day's.
func HeatmapOf(f Filter) Heatmap {
	f.Day = ""
	if h := QueryPage(heatmapPeriod, f, 0, 1).Heat; h != nil {
		return *h
	}
	return Heatmap{Days: []Day{}}
}

// heatmapOf sums the rows visit gives it by their local day, from since to
// now's day; a row outside those days is left out.
func heatmapOf(since, now time.Time, visit func(add func(Row))) *Heatmap {
	h := &Heatmap{From: since.Format(time.DateOnly), To: now.In(time.Local).Format(time.DateOnly), Days: []Day{}}
	at := map[string]*Day{}
	visit(func(r Row) {
		if r.IsRejected() || r.Time.Before(since) {
			return
		}
		d := r.Time.In(time.Local).Format(time.DateOnly)
		if d > h.To {
			return
		}
		x := at[d]
		if x == nil {
			x = &Day{Date: d}
			at[d] = x
		}
		x.Calls++
		x.Tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
		switch {
		case r.Priced:
			x.Cost += r.Cost
		case r.Input+r.Output > 0:
			x.Unpriced++
		}
	})
	for _, x := range at {
		h.Days = append(h.Days, *x)
	}
	slices.SortFunc(h.Days, func(a, b Day) int {
		if a.Date < b.Date {
			return -1
		}
		if a.Date > b.Date {
			return 1
		}
		return 0
	})
	return h
}
