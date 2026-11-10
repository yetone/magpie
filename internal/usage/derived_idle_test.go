package usage

import (
	"testing"
	"time"
)

// What is kept of each chunk by its serial stays while it was read within
// derivedIdle, is let go after, and is read again from the chunk with the
// same answer.
func TestDerivedLetGoWhenIdle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cut := now.Add(-48 * time.Hour)
	var set []*rowChunk
	for k := range 3 {
		c := &rowChunk{serial: nextChunkSerial()}
		for i := range 30 {
			r := Record{Time: now.Add(-time.Duration(k*15+i) * 3 * time.Hour), Agent: "claude", Provider: "relay", Input: 1 + i, Output: k}
			c.add(Row{Record: r}, "", int64(i), false)
		}
		set = append(set, c)
	}
	sets := [3][]*rowChunk{set, nil, nil}
	var opened int
	heatBeforeOpening = func(*rowChunk) { opened++ }
	t.Cleanup(func() { heatBeforeOpening = nil })
	derivedKept.off = false
	t.Cleanup(func() { derivedKept.off = true })

	before := heatBefore(cut, sets)
	firstAt := chunkFirsts(sets)(set[0])
	if opened != 3 || before.n == 0 || firstAt.IsZero() {
		t.Fatalf("first read unpacked %d, %+v, first %v", opened, before, firstAt)
	}
	derivedIdleCheck() // read just now: kept
	opened = 0
	if heatBefore(cut, sets); opened != 0 {
		t.Fatalf("read within derivedIdle unpacked %d chunks; want them kept", opened)
	}

	derivedKept.last.Store(time.Now().Add(-2 * derivedIdle).UnixNano())
	derivedIdleCheck()
	heatBefores.Lock()
	hb := len(heatBefores.by)
	heatBefores.Unlock()
	firsts.Lock()
	fb := len(firsts.by)
	firsts.Unlock()
	if hb != 0 || fb != 0 {
		t.Fatalf("idle: %d heat sums and %d firsts kept", hb, fb)
	}
	if derivedKept.armed.Load() {
		t.Fatal("still armed after letting go with no read since")
	}

	opened = 0
	if got := heatBefore(cut, sets); got != before || opened != 3 {
		t.Fatalf("after the let-go: %+v unpacking %d, want %+v unpacking 3", got, opened, before)
	}
	if got := chunkFirsts(sets)(set[0]); !got.Equal(firstAt) {
		t.Fatalf("first after the let-go = %v, want %v", got, firstAt)
	}
	if !derivedKept.armed.Load() {
		t.Fatal("a read after the let-go did not arm the check again")
	}
}
