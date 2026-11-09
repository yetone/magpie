package sessions

import (
	"encoding/json"
	"testing"
)

// TestHoursKeepTheCacheShape: a day's hours read from the cache's 24
// numbers are written back as the same 24, an hour of 0ms included, and
// hold only the hours that have some.
func TestHoursKeepTheCacheShape(t *testing.T) {
	for _, src := range []string{
		`{"h":[0,0,0,0,0,0,0,0,0,1500,0,0,0,0,0,0,0,0,0,0,0,0,0,42]}`,
		`{"h":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]}`,
		`{"a":3}`,
	} {
		var d day
		if err := json.Unmarshal([]byte(src), &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Hours) > 2 || cap(d.Hours) > 2 {
			t.Fatalf("%s: %d entries (cap %d)", src, len(d.Hours), cap(d.Hours))
		}
		b, _ := json.Marshal(&d)
		if string(b) != src {
			t.Fatalf("%s came back as %s", src, b)
		}
	}
}

// TestHoursAddInOrder: a day's hours add up per hour, in hour order,
// as the 24 numbers did.
func TestHoursAddInOrder(t *testing.T) {
	var hs hours
	var want [24]int64
	for i, h := range []int{9, 3, 23, 9, 0, 3, 12} {
		ms := int64(i*1000 + 7)
		hs.add(h, ms)
		want[h] += ms
	}
	hs.add(5, 0)
	var got [24]int64
	last := -1
	for _, e := range hs {
		if hourOf(e) <= last {
			t.Fatalf("hours out of order: %v", hs)
		}
		last = hourOf(e)
		got[hourOf(e)] = msOf(e)
	}
	if got != want || len(hs) != 6 {
		t.Fatalf("got %v (%d entries), want %v", got, len(hs), want)
	}
}
