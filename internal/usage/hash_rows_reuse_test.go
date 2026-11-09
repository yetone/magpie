package usage

import (
	"crypto/sha256"
	"fmt"
	"math"
	"runtime"
	"testing"
	"time"
)

func hashedRows(c *rowChunk) [sha256.Size]byte {
	h := sha256.New()
	hashRows(h, c)
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// hashRows gave each chunk a new 32 KB buffer and shape map: 522 MB of a
// real history's start, a digest per session file's chunk. It reuses
// them now, and what it writes for a chunk is the same whatever it hashed
// before: a shape another chunk saw first is not taken as seen.
func TestHashRowsReusesItsScratch(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	chunk := func(agent string, n int) *rowChunk {
		c := &rowChunk{}
		for i := range n {
			r := Row{Record: Record{Time: now.Add(time.Duration(i) * time.Second), Agent: agent, Model: fmt.Sprint("m", i%3), Input: i}}
			c.add(r, fmt.Sprint("msg", i), int64(i), false)
		}
		return c
	}
	a, b := chunk("claude", 40), chunk("codex", 2000)
	first := hashedRows(a)
	t.Logf("digest %x", first)
	hashedRows(b)
	if again := hashedRows(a); again != first {
		t.Fatal("a chunk's rows hashed differently after another chunk's")
	}
	// by bytes: a pool may drop what it keeps (-race drops some on
	// purpose). The process's count takes in whatever else runs, so the
	// least of five batches is the one judged.
	h := sha256.New()
	per := uint64(math.MaxUint64)
	for range 5 {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		const n = 20
		for range n {
			h.Reset()
			hashRows(h, b)
		}
		runtime.ReadMemStats(&m1)
		per = min(per, (m1.TotalAlloc-m0.TotalAlloc)/n)
	}
	if per >= 16<<10 {
		t.Fatalf("hashRows allocated %d bytes a chunk", per)
	}
}

// within made its rows by append, growing them up to twice what they
// needed: 245 MB of a real history's start. It counts them first now.
func TestWithinSizesItsRows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := &rowChunk{}
	for i := range 1000 {
		r := Row{Record: Record{Time: now.Add(time.Duration(i) * time.Minute), Agent: "claude", Model: "m", Input: i}}
		c.add(r, fmt.Sprint("msg", i), int64(i), false)
	}
	out := c.within(now.Add(100*time.Minute), now.Add(700*time.Minute))
	if out == nil || len(out.Rows) != 600 || cap(out.Rows) != 600 {
		t.Fatalf("within kept %d rows in room for %d", len(out.Rows), cap(out.Rows))
	}
	if r := out.row(0); r.Input != 100 || out.row(599).Input != 699 {
		t.Fatalf("within kept rows %d to %d", r.Input, out.row(599).Input)
	}
	if c.within(now.Add(-time.Hour), now) != nil {
		t.Fatal("rows outside the span were kept")
	}
}
