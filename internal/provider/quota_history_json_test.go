package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

// TestQuotaHistWrittenAsJSONMarshal holds the history's own writer to
// what json.Marshal wrote, byte for byte, for every shape the file can
// hold: another computer's points merged in keep their zone and their
// figures as sent.
func TestQuotaHistWrittenAsJSONMarshal(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	east := time.FixedZone("x", 8*3600+30*60)
	lefts := []float64{0, 100, 37.25, 0.01, 1e-7, -3, 1e21, 123456789.5, 5e-324, math.MaxFloat64, -0.0000012}
	at := func() time.Time {
		t := time.Date(2026, 1+time.Month(r.Intn(12)), 1+r.Intn(28), r.Intn(24), r.Intn(60), r.Intn(60), 0, time.UTC)
		switch r.Intn(4) {
		case 0:
			return t.In(east)
		case 1:
			return t.Add(time.Duration(r.Intn(1e9)))
		}
		return t
	}
	h := quotaHist{
		"codex|a@b.c":      {"5 hours": nil, "Weekly": {}},
		"claude|<x>&\"y\"": nil,
	}
	for i := range 30 {
		ws := map[string][]QuotaPoint{}
		for j := range 1 + r.Intn(4) {
			var pts []QuotaPoint
			for range r.Intn(40) {
				p := QuotaPoint{At: at(), Left: lefts[r.Intn(len(lefts))]}
				if r.Intn(2) == 0 {
					p.Left = math.Round(r.Float64()*10000) / 100
				}
				if r.Intn(3) > 0 {
					s := at()
					p.ResetsAt = &s
				}
				if r.Intn(2) == 0 {
					s := at()
					p.Start = &s
				}
				pts = append(pts, p)
			}
			ws[fmt.Sprintf("window \u2028%d<%d>", j, i)] = pts
		}
		h[fmt.Sprintf("p%d|user%d@x.y", i, i)] = ws
	}
	want, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := marshalQuotaHist(h)
	if err != nil || !bytes.Equal(got, want) {
		i := 0
		for i < min(len(got), len(want)) && got[i] == want[i] {
			i++
		}
		t.Fatalf("err %v; differs at %d:\n got %.120s\nwant %.120s", err, i, got[i:], want[i:])
	}
	for _, n := range []quotaHist{nil, {}} {
		want, _ := json.Marshal(n)
		if got, _ := marshalQuotaHist(n); !bytes.Equal(got, want) {
			t.Fatalf("%v: got %s, want %s", n, got, want)
		}
	}
	// what json.Marshal refuses or writes its own way it is left to: the
	// same bytes, or the same refusal
	far := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	odd := time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("s", 30))
	for _, bad := range []QuotaPoint{{At: far}, {At: odd}, {At: time.Now(), Left: math.NaN()}, {At: time.Now(), ResetsAt: &far}} {
		b := quotaHist{"a|b": {"w": {bad}}}
		want, werr := json.Marshal(b)
		got, err := marshalQuotaHist(b)
		if (werr == nil) != (err == nil) || !bytes.Equal(got, want) {
			t.Fatalf("%v: json.Marshal %s %v, own %s %v", bad, want, werr, got, err)
		}
	}
}

// TestQuotaHistWriteAllocations: writing a full history again makes no
// value for each of its points; json.Marshal made three times' text and
// the whole file each write.
func TestQuotaHistWriteAllocations(t *testing.T) {
	h := quotaHist{}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for a := range 12 {
		ws := map[string][]QuotaPoint{}
		for _, name := range []string{"5 hours", "Weekly"} {
			var pts []QuotaPoint
			for i := range 250 {
				p := QuotaPoint{At: base.Add(time.Duration(i) * 5 * time.Minute), Left: float64(i%100) + 0.25}
				r := p.At.Add(5 * time.Hour)
				s := p.At
				p.ResetsAt, p.Start = &r, &s
				pts = append(pts, p)
			}
			ws[name] = pts
		}
		h[fmt.Sprintf("codex|user%d@example.com", a)] = ws
	}
	if _, err := marshalQuotaHist(h); err != nil {
		t.Fatal(err)
	}
	// 6000 points: a dozen accounts' and windows' names and their sorting
	// are what is left
	n := testing.AllocsPerRun(5, func() { marshalQuotaHist(h) })
	if n > 100 {
		t.Fatalf("a write of 6000 points made %v allocations, want at most 100", n)
	}
}
