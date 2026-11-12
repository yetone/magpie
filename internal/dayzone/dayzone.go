// Package dayzone makes a time zone whose midnight falls at a chosen hour
// of the real day, so that every place that cuts days by the zone's
// midnight (the Usage page, a daily cap) cuts them there without knowing:
// with a day that starts at 4, a call at 01:30 belongs to the day before
// (ssharpcc on X: coding past 1am should count toward the day before).
package dayzone

import (
	"encoding/binary"
	"sync"
	"time"
)

// MaxStart is the latest hour a day may start at.
const MaxStart = 6

type key struct {
	loc  *time.Location
	hour int
}

var (
	mu     sync.Mutex
	made   = map[key]*time.Location{}
	shifts sync.Map // *time.Location → time.Duration
)

// Shift is loc moved so that its midnight is the real day's hour:00: the
// same offsets, hour hours less each, with the same changes of daylight
// saving at the same instants. An hour outside 1 to MaxStart is loc itself.
// The same location comes back for the same loc and hour, so a Filter
// holding it is the same cache key each time.
func Shift(loc *time.Location, hour int) *time.Location {
	if hour < 1 || hour > MaxStart {
		return loc
	}
	mu.Lock()
	defer mu.Unlock()
	k := key{loc, hour}
	if l, ok := made[k]; ok {
		return l
	}
	l, err := time.LoadLocationFromTZData(loc.String()+"+"+string(rune('0'+hour)), tzif(loc, hour))
	if err != nil {
		return loc // never: the data is made here
	}
	made[k] = l
	shifts.Store(l, time.Duration(hour)*time.Hour)
	return l
}

// Offset is how far loc's midnight lies after the real midnight: the hour a
// shifted zone's day starts at, 0 for any other.
func Offset(loc *time.Location) time.Duration {
	if d, ok := shifts.Load(loc); ok {
		return d.(time.Duration)
	}
	return 0
}

// The changes of a zone are listed this many years either way from now.
// Past the last one the zone keeps its last offset, as a version 1 zoneinfo
// file does; the span is long against a run of magpie.
const (
	yearsBack  = 4
	yearsAhead = 4
)

// tzif writes loc's offsets, each hour hours less, as a version 1 zoneinfo
// file Go reads back: the instants the offset changed in the span, and the
// offset from each.
func tzif(loc *time.Location, hour int) []byte {
	type kind struct {
		off  int
		dst  bool
		name string
	}
	var kinds []kind
	at := func(t time.Time) int {
		l := t.In(loc)
		name, off := l.Zone()
		k := kind{off - hour*3600, l.IsDST(), name}
		for i, x := range kinds {
			if x == k {
				return i
			}
		}
		kinds = append(kinds, k)
		return len(kinds) - 1
	}
	y := time.Now().Year()
	start := time.Date(y-yearsBack, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(y+yearsAhead, time.January, 1, 0, 0, 0, 0, time.UTC)
	prev := at(start)
	var times []int32
	var idx []byte
	same := func(a, b time.Time) bool { return at(a) == at(b) }
	for t := start.Add(time.Hour); t.Before(end); t = t.Add(time.Hour) {
		if same(t, t.Add(-time.Hour)) {
			continue
		}
		// the change is within the last hour: find its second
		lo, hi := t.Add(-time.Hour).Unix(), t.Unix()
		for hi-lo > 1 {
			mid := (lo + hi) / 2
			if same(time.Unix(mid, 0), t.Add(-time.Hour)) {
				lo = mid
			} else {
				hi = mid
			}
		}
		k := at(time.Unix(hi, 0))
		if k == prev {
			continue
		}
		prev = k
		times = append(times, int32(hi))
		idx = append(idx, byte(k))
	}
	var abbr []byte
	pos := make([]byte, len(kinds))
	for i, k := range kinds {
		pos[i] = byte(len(abbr))
		abbr = append(abbr, k.name...)
		abbr = append(abbr, 0)
	}
	out := []byte("TZif")
	out = append(out, 0)
	out = append(out, make([]byte, 15)...)
	for _, n := range []int{0, 0, 0, len(times), len(kinds), len(abbr)} {
		out = binary.BigEndian.AppendUint32(out, uint32(n))
	}
	for _, t := range times {
		out = binary.BigEndian.AppendUint32(out, uint32(t))
	}
	out = append(out, idx...)
	for i, k := range kinds {
		out = binary.BigEndian.AppendUint32(out, uint32(int32(k.off)))
		d := byte(0)
		if k.dst {
			d = 1
		}
		out = append(out, d, pos[i])
	}
	return append(out, abbr...)
}
