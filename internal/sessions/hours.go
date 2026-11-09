package sessions

import (
	"encoding/json"
	"slices"
	"strconv"
)

// hours is a day's Active by the local hour it was put on, kept as only
// the hours that have some: of the owner's 19,191 days, 13,406 had hours,
// and 19,107 hours had any, so 24 int64s a day (192 bytes) were ~2.6 MB of
// zeros held for good. Each entry is hour<<hourShift | ms, in hour order.
// In the cache it is the 24 numbers it always was. A day that counted a
// gap of no whole millisecond keeps its hour's entry at 0, so it is still
// a day with hours, as its 24 zeros were.
type hours []uint64

const hourShift = 58

func hourOf(e uint64) int              { return int(e >> hourShift) }
func msOf(e uint64) int64              { return int64(e & (1<<hourShift - 1)) }
func hourEntry(h int, ms int64) uint64 { return uint64(h)<<hourShift | uint64(ms) }

// add puts ms (never below 0) on hour h, 0 to 23.
func (hs *hours) add(h int, ms int64) {
	for i, e := range *hs {
		switch eh := hourOf(e); {
		case eh == h:
			(*hs)[i] = hourEntry(h, msOf(e)+ms)
			return
		case eh > h:
			*hs = slices.Insert(*hs, i, hourEntry(h, ms))
			return
		}
	}
	*hs = append(*hs, hourEntry(h, ms))
}

func (hs hours) MarshalJSON() ([]byte, error) {
	var all [24]int64
	for _, e := range hs {
		all[hourOf(e)] = msOf(e)
	}
	b := make([]byte, 0, 2+24*2)
	b = append(b, '[')
	for i, ms := range all {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, ms, 10)
	}
	return append(b, ']'), nil
}

func (hs *hours) UnmarshalJSON(b []byte) error {
	var all []int64
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	if all == nil {
		*hs = nil
		return nil
	}
	n := 0
	for _, ms := range all[:min(len(all), 24)] {
		if ms > 0 {
			n++
		}
	}
	out := make(hours, 0, max(n, 1))
	for h, ms := range all[:min(len(all), 24)] {
		if ms > 0 {
			out = append(out, hourEntry(h, ms))
		}
	}
	if len(out) == 0 {
		out = append(out, hourEntry(0, 0))
	}
	*hs = out
	return nil
}
