package usage

import (
	"testing"
	"time"
)

// A point of the chart tells each part's bytes up and down apart, as it
// does tokens, for Requests' Traffic chart to stack them (songlairui on
// X): from the rows (LedgerSeries, Part.add) and from the request page's
// cells (Part.plus) alike, and the point's own sums have them all.
func TestSeriesPartsKeepBytes(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	records := []Record{
		{Time: now, Provider: "a", Agent: "codex", Model: "m", Input: 10, UpBytes: 1000, DownBytes: 300, Status: 200},
		{Time: now, Provider: "a", Agent: "codex", Model: "m", Input: 10, UpBytes: 2000, DownBytes: 400, Status: 200},
		{Time: now, Provider: "b", Agent: "claude", Model: "n", Input: 10, UpBytes: 50, DownBytes: 7, Status: 500},
	}
	gateway := &rowChunk{}
	all := Ledgered{}
	for i, r := range records {
		row := Row{Record: r}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
	}
	for name, page := range map[string]RequestPage{
		"compact": buildRequestPage(Today, Filter{}, 0, 100, gateway, nil),
		"ledger":  pageFromLedger(Today, Filter{}, 0, 100, all),
	} {
		t.Run(name, func(t *testing.T) {
			var up, down int64
			parts := map[string]Part{}
			for _, pt := range page.Series {
				up += pt.UpBytes
				down += pt.DownBytes
				for id, p := range pt.By["provider"] {
					q := parts[id]
					q.UpBytes += p.UpBytes
					q.DownBytes += p.DownBytes
					parts[id] = q
				}
			}
			if up != 3050 || down != 707 {
				t.Fatalf("the points' own sums: up %d, down %d", up, down)
			}
			if a, b := parts["a"], parts["b"]; a.UpBytes != 3000 || a.DownBytes != 700 || b.UpBytes != 50 || b.DownBytes != 7 {
				t.Fatalf("the providers' parts: %+v", parts)
			}
		})
	}
}
