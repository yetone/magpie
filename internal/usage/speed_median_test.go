package usage

import (
	"math"
	"testing"
	"time"
)

// within is a 2% histogram bin's room: a median is the middle of its bin.
func within(got, want float64) bool { return math.Abs(got-want) <= 0.02*want }

// A sum's speed and floor are the median of its replies' own, not their
// tokens over their summed time (FallMonkey on X: fable-5-2's ranking row
// read ≥31 tok/s over 1,034 replies whose own rows mostly read ≥80-105; a
// few replies held for many minutes before their first token, by a rate
// limit or a closed lid, filled the summed time). Measured speeds the same:
// a few replies stalled mid-stream don't pull hundreds that streamed. The
// summary, ledger sums, Breakdown, the ranking's merge and a chart's parts
// tell the same figure.
func TestGroupSpeedIsTheMedianOfItsReplies(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 30, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	var rows []Row
	// the reporter's shape: 1,000 floors of 80-105 tok/s from Claude Code's
	// session files, 2,000 tokens each, and 34 of the same 2,000 tokens over
	// 30 minutes
	for i := 0; i < 1000; i++ {
		rate := 80 + float64(i%26)
		rows = append(rows, Row{Source: "log", Record: Record{Time: at, Agent: "claude", Provider: "anthropic", Model: "fable-5-2", Output: 2000, Millis: int64(2000 * 1000 / rate)}})
	}
	for i := 0; i < 34; i++ {
		rows = append(rows, Row{Source: "log", Record: Record{Time: at, Agent: "claude", Provider: "anthropic", Model: "fable-5-2", Output: 2000, Millis: 30 * 60 * 1000}})
	}
	// and through the gateway: 500 replies of 1,000 tokens over 10 s after
	// their first, 100 tok/s, and 20 that stalled mid-stream for 10 minutes
	var recs []Record
	for i := 0; i < 520; i++ {
		ms := int64(10000)
		if i >= 500 {
			ms = 600000
		}
		recs = append(recs, Record{Time: at, Agent: "claude", Provider: "anthropic", Model: "fable-5-2", Output: 1000, Millis: 1000 + ms, TTFT: 1000, Status: 200})
	}
	for _, r := range recs {
		rows = append(rows, Row{Record: r})
	}
	var sum Totals
	for _, r := range rows {
		sum.addRow(r)
	}
	if sum.FloorN != 1034 || sum.DecodeN != 520 {
		t.Fatalf("%d floors, %d measured; want 1034, 520", sum.FloorN, sum.DecodeN)
	}
	// the old sums read 25 and 24 tok/s
	if f := sum.Floor(); f < 80 || f > 105 {
		t.Errorf("floor %v tok/s, want the rows' 80-105", f)
	}
	if s := sum.Speed(); !within(s, 100) {
		t.Errorf("speed %v tok/s, want 100", s)
	}
	m := summarize(Today, now, recs).Models[0]
	if s := m.Speed(); !within(s, 100) {
		t.Errorf("summary: %v tok/s, want 100", s)
	}
	g := Breakdown(rows, "model")[0]
	if g.Floor() != sum.Floor() || g.Speed() != sum.Speed() {
		t.Errorf("breakdown: %v/%v, sums %v/%v", g.Speed(), g.Floor(), sum.Speed(), sum.Floor())
	}
	// two halves added up are the median of all their replies, and adding
	// leaves each half as it was
	var a, b Totals
	for i, r := range rows {
		if i%2 == 0 {
			a.addRow(r)
		} else {
			b.addRow(r)
		}
	}
	af, bf := a.Floor(), b.Floor()
	ab := a
	ab.plus(&b)
	if ab.Floor() != sum.Floor() || ab.Speed() != sum.Speed() {
		t.Errorf("halves added: %v/%v, want %v/%v", ab.Speed(), ab.Floor(), sum.Speed(), sum.Floor())
	}
	ab.addRow(rows[len(rows)-1])
	if a.Floor() != af || b.Floor() != bf || a.Rates.Speed.total()+b.Rates.Speed.total() != 520 {
		t.Errorf("adding changed a half: %v→%v, %v→%v", af, a.Floor(), bf, b.Floor())
	}
	// a chart's point tells the same speed as the row
	_, pts := ledgerSeriesAt(Today, rows, now)
	var p Part
	for _, pt := range pts {
		if q, ok := pt.By["model"]["fable-5-2"]; ok {
			p = q
		}
	}
	if !within(p.TPS, 100) || p.speeds != nil {
		t.Errorf("chart part: %v tok/s, want 100", p.TPS)
	}
	// none counted tells none
	if (Totals{}).Speed() != 0 || (Totals{}).Floor() != 0 {
		t.Error("nothing timed")
	}
}
