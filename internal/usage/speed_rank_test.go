package usage

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"reflect"
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

// One model on several providers is told apart by provider: each one's
// speed and first token over its own requests, and how its tries went,
// failed (handed on or not) and answered by another model counted apart
// (@skingstyle48487 on X, comparing a dozen relays). A canceled or a
// rejected try, or a probe, is in none of it, and the model's own place in
// the ranking is every provider's requests together, as before.
func TestModelSpeedRankByProvider(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local))
	at := now.Add(-time.Hour)
	call := func(provider string, ttft, ms int64, mod func(*Row)) Row {
		r := Row{Record: Record{Time: at, Provider: provider, Agent: "codex", Model: "gpt", Input: 100, Output: 500, TTFT: ttft, Millis: ms, Status: 200}}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	rows := []Row{
		// fast: 100 tok/s, first token 1 s, twice
		call("fast", 1000, 6000, nil),
		call("fast", 1000, 6000, nil),
		// slow: 25 tok/s, first token 4 s; one answer from another model,
		// one failed, one handed on, one canceled, one rejected
		call("slow", 4000, 24000, nil),
		call("slow", 4000, 24000, func(r *Row) { r.Served, r.Swapped = "claude-haiku-9", true }),
		call("slow", 100, 200, func(r *Row) { r.Status, r.Error = 502, "bad gateway" }),
		call("slow", 100, 200, func(r *Row) { r.Status, r.Error, r.HandedOn = 429, "busy", true }),
		call("slow", 100, 200, func(r *Row) { r.Status, r.ErrType = StatusCanceled, CanceledErrType }),
		call("slow", 4000, 24000, func(r *Row) { r.Rejected = true }),
		// a model only ever failing, on a provider of its own
		call("down", 0, 300, func(r *Row) { r.Model, r.Status, r.Error = "gone", 500, "boom" }),
	}
	gateway := &rowChunk{}
	all := Ledgered{Agents: []string{"codex"}, Providers: []string{"fast", "slow", "down"}}
	for i, row := range rows {
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
		if !row.IsRejected() {
			all.Sum.addRow(row)
		}
	}
	for name, query := range map[string]func(Filter) RequestPage{
		"compact": func(f Filter) RequestPage { return buildRequestPage(Today, f, 0, 1, gateway, nil) },
		"ledger":  func(f Filter) RequestPage { return pageFromLedger(Today, f, 0, 1, all) },
	} {
		t.Run(name, func(t *testing.T) {
			page := query(Filter{})
			if len(page.Speeds) != 1 || page.Speeds[0].Model != "gpt" || page.Speeds[0].Requests != 4 {
				t.Fatalf("by model: %+v, want gpt alone, of its 4 answered requests", page.Speeds)
			}
			got := page.ProviderSpeeds
			if len(got) != 3 {
				t.Fatalf("by provider: %+v", got)
			}
			f, s, d := got[0], got[1], got[2]
			if f.Provider != "fast" || f.Model != "gpt" || f.Tries != 2 || f.Failed != 0 || f.Swapped != 0 || f.Requests != 2 || offBin(f.Speed, 100) || offBin(float64(f.TTFT), 1000) {
				t.Fatalf("fast first: %+v", f)
			}
			if s.Provider != "slow" || s.Tries != 4 || s.Failed != 2 || s.Swapped != 1 || s.Requests != 2 || offBin(s.Speed, 25) || offBin(float64(s.TTFT), 4000) {
				t.Fatalf("slow: %+v", s)
			}
			if d.Provider != "down" || d.Model != "gone" || d.Tries != 1 || d.Failed != 1 || d.Requests != 0 || d.Speed != 0 {
				t.Fatalf("a model that only failed, last: %+v", d)
			}
			if only := query(Filter{Provider: "fast"}).ProviderSpeeds; len(only) != 1 || only[0].Provider != "fast" {
				t.Fatalf("provider filter: %+v", only)
			}
		})
	}
	// the counts survive the chunks' merge and the file
	a, b := speedRank{}, speedRank{}
	a.add(&rows[3])
	b.add(&rows[4])
	part := addCells(&chunkPart{speeds: a}, &chunkPart{speeds: b})
	disk := pageDiskPart(pageDiskEntry{Speeds: part.speeds})
	for name, rank := range map[string]speedRank{"merged": part.speeds, "file": disk.speeds} {
		if got := rank.providers(); len(got) != 1 || got[0].Tries != 2 || got[0].Failed != 1 || got[0].Swapped != 1 || got[0].Requests != 1 {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

// A histogram kept as bins in order counts, merges and takes its median
// as the map of bin to count it replaced did, and reads the page file that
// map wrote: the same JSON object.
func TestSpeedHistIsTheMap(t *testing.T) {
	median := func(m map[uint16]uint32) float64 {
		var h speedHist
		for b, n := range m {
			h.inc(b, n)
		}
		return h.median()
	}
	r := rand.New(rand.NewPCG(1, 2))
	for round := range 200 {
		ma, mb := map[uint16]uint32{}, map[uint16]uint32{}
		var a, b speedHist
		for range r.IntN(300) {
			bin, n := uint16(r.IntN(40*(round%5+1))), uint32(r.IntN(3)+1)
			ma[bin] += n
			a.inc(bin, n)
		}
		for range r.IntN(300) {
			bin := uint16(r.IntN(200))
			mb[bin]++
			b.inc(bin, 1)
		}
		sum := map[uint16]uint32{}
		for _, m := range []map[uint16]uint32{ma, mb} {
			for bin, n := range m {
				sum[bin] += n
			}
		}
		got := a.plus(b)
		if got.total() != a.total()+b.total() || got.median() != median(sum) || len(got) != len(sum) {
			t.Fatalf("round %d: merged %d values, median %v over %d bins; want %d, %v, %d", round, got.total(), got.median(), len(got), a.total()+b.total(), median(sum), len(sum))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].bin >= got[i].bin {
				t.Fatalf("round %d: bins out of order at %d", round, i)
			}
		}
		old, err := json.Marshal(sum)
		if err != nil {
			t.Fatal(err)
		}
		var read speedHist
		if err := json.Unmarshal(old, &read); err != nil || !reflect.DeepEqual(read, got) && len(got) > 0 {
			t.Fatalf("round %d: the map's JSON read back as %v, %v; want %v", round, read, err, got)
		}
		mine, _ := json.Marshal(got)
		var back map[uint16]uint32
		if err := json.Unmarshal(mine, &back); err != nil || !reflect.DeepEqual(back, sum) {
			t.Fatalf("round %d: written %s, read by the map as %v, %v", round, mine, back, err)
		}
	}
	var e struct {
		H speedHist `json:"h,omitempty"`
	}
	if b, _ := json.Marshal(e); string(b) != "{}" {
		t.Fatalf("an empty histogram is written: %s", b)
	}
}
