package usage

import (
	"math"
	"testing"
	"time"
)

// The model speed ranking is the median of each model's fair requests: the
// speed of a stalled or a burst one never moves it, and a failed, a short, an
// untimed or a rejected request is not in it (@flying_crp on X asked how
// slow gpt really is). Both ways the page is built rank the same.
// offBin is true when got is more than 1.5% from want, the histogram's bins.
func offBin(got, want float64) bool { return math.Abs(got-want) > want*0.015 }

func TestModelSpeedRankIsTheMedianOfFairRequests(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local))
	at := now.Add(-time.Hour)
	call := func(provider, model string, out int, ttft, ms int64, mod func(*Record)) Record {
		r := Record{Time: at, Provider: provider, Agent: "codex", Model: model, Input: 100, Output: out, TTFT: ttft, Millis: ms, Status: 200}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	recs := []Record{
		// gpt: 40, 50, 60 tok/s and one burst of 5,000; first tokens 2, 3, 4, 1 s
		call("a", "gpt", 500, 2000, 14500, nil),
		call("a", "gpt", 500, 3000, 13000, nil),
		call("a", "gpt", 500, 4000, 12333, nil),
		call("a", "gpt", 2000, 1000, 1400, nil),
		// not counted: failed, short, never timed, rejected
		call("a", "gpt", 500, 100, 200, func(r *Record) { r.Status, r.Error = 500, "boom" }),
		call("a", "gpt", 99, 100, 20000, nil),
		call("a", "gpt", 500, 0, 20000, nil),
		call("a", "gpt", 500, 100, 20000, func(r *Record) { r.Rejected = true }),
		// a model whose replies came in a burst: a first token, no speed
		call("b", "burst", 200, 500, 550, nil),
		// the fastest
		call("b", "quick", 800, 300, 3300, nil),
	}
	gateway := &rowChunk{}
	all := Ledgered{Agents: []string{"codex"}, Providers: []string{"a", "b"}}
	for i, r := range recs {
		row := Row{Record: r}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
		if !r.IsRejected() {
			all.Sum.addRow(row)
		}
	}
	for name, query := range map[string]func(Filter) RequestPage{
		"compact": func(f Filter) RequestPage { return buildRequestPage(Today, f, 0, 1, gateway, nil) },
		"ledger":  func(f Filter) RequestPage { return pageFromLedger(Today, f, 0, 1, all) },
	} {
		t.Run(name, func(t *testing.T) {
			got := query(Filter{}).Speeds
			if len(got) != 3 || got[0].Model != "quick" || got[1].Model != "gpt" || got[2].Model != "burst" {
				t.Fatalf("ranked %+v, want quick, gpt, burst", got)
			}
			if q := got[0]; offBin(q.Speed, 266.7) || offBin(float64(q.TTFT), 300) || q.Requests != 1 {
				t.Fatalf("quick: %+v", q)
			}
			// median of 40, 50, 60.002 and 5,000; of 1, 2, 3 and 4 s
			if g := got[1]; offBin(g.Speed, 55) || offBin(float64(g.TTFT), 2500) || g.Requests != 4 || g.SpeedN != 4 {
				t.Fatalf("gpt: %+v", g)
			}
			if b := got[2]; b.Speed != 0 || b.SpeedN != 0 || offBin(float64(b.TTFT), 500) || b.Requests != 1 {
				t.Fatalf("burst: %+v", b)
			}
			// the filter narrows what is ranked
			if only := query(Filter{Provider: "b"}).Speeds; len(only) != 2 || only[0].Model != "quick" {
				t.Fatalf("provider b: %+v", only)
			}
			if none := query(Filter{Provider: "zzz"}).Speeds; len(none) != 0 {
				t.Fatalf("no calls: %+v", none)
			}
		})
	}
}

// A ledger of a million requests ranks in one pass that keeps two numbers for
// each request counted: the page that is asked for again and again costs one
// sort per model, not per request.
func TestModelSpeedRankAtTheLargestLedger(t *testing.T) {
	rank := speedRank{}
	row := Row{Record: Record{Provider: "a", Model: "gpt", Output: 500, TTFT: 1000, Millis: 11000, Status: 200}}
	start := time.Now()
	for i := 0; i < 1_000_000; i++ {
		row.TTFT = int64(500 + i%1000)
		rank.add(&row)
	}
	got := rank.result()
	if len(got) != 1 || got[0].Requests != 1_000_000 || got[0].TTFT < 970 || got[0].TTFT > 1030 {
		t.Fatalf("%+v", got)
	}
	t.Logf("a million requests ranked in %v", time.Since(start))
}

// The counts of two chunks, and of a chunk written to the file and read
// back, rank as one: a settled stretch of history keeps its place.
func TestModelSpeedRankMergesAndSurvivesTheFile(t *testing.T) {
	row := func(out int, ttft, ms int64) *Row {
		return &Row{Record: Record{Model: "gpt", Output: out, TTFT: ttft, Millis: ms, Status: 200}}
	}
	a, b := speedRank{}, speedRank{}
	a.add(row(500, 1000, 11000)) // 50 tok/s
	b.add(row(500, 3000, 13000)) // 50 tok/s, a slower start
	b.add(row(500, 2000, 7000))  // 100 tok/s
	part := addCells(&chunkPart{speeds: a}, &chunkPart{speeds: b})
	disk := pageDiskPart(pageDiskEntry{Speeds: part.speeds})
	for name, rank := range map[string]speedRank{"merged": part.speeds, "file": disk.speeds} {
		got := rank.result()
		if len(got) != 1 || got[0].Requests != 3 || got[0].SpeedN != 3 || offBin(got[0].Speed, 50) || offBin(float64(got[0].TTFT), 2000) {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}
