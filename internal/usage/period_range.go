package usage

import (
	"strings"
	"time"
)

// A period can also be days the reader picked (#1492): from one date to
// another, both whole days and both included, in the local time zone,
// written "2026-10-01..2026-10-07". It begins at the first day's midnight and
// ends at the midnight after the last, where the presets end at now.
//
// Either end can also name a minute of its day (blrbrzkj on Discord):
// "2026-10-01T09:30..2026-10-01T18:00" begins at 09:30 and ends at 18:00,
// that minute itself not included. An end without one stays a whole day.
const rangeSep = ".."

// rangeMinute is the layout of an end that names its minute.
const rangeMinute = "2006-01-02T15:04"

// rangeEnd reads one end of a picked range in loc: when it begins and
// whether it named its minute.
func rangeEnd(s string, loc *time.Location) (at time.Time, timed bool, ok bool) {
	if t, err := time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		return t, false, true
	}
	if t, err := time.ParseInLocation(rangeMinute, s, loc); err == nil {
		return t, true, true
	}
	return time.Time{}, false, false
}

// RangeOf is the period from one date (YYYY-MM-DD), or one minute of a date
// (YYYY-MM-DDTHH:MM), to another, in either order; ok is false when either
// is neither, or when the two are the same minute.
func RangeOf(from, to string) (Period, bool) {
	a, ta, ok1 := rangeEnd(from, time.UTC)
	b, tb, ok2 := rangeEnd(to, time.UTC)
	if !ok1 || !ok2 {
		return "", false
	}
	if b.Before(a) {
		a, b, ta, tb = b, a, tb, ta
	}
	name := func(t time.Time, timed bool) string {
		if timed {
			return t.Format(rangeMinute)
		}
		return t.Format(time.DateOnly)
	}
	p := Period(name(a, ta) + rangeSep + name(b, tb))
	if !p.IsRange() {
		return "", false
	}
	return p, true
}

// A period can also be hours or days long, ending now (#2075): a rolling
// window, written "last:24h" or "last:90m", read from LastOf. Unlike the
// presets, which begin at a local midnight, it begins that long ago —
// what `magpie usage --last 24h` and `--days 1` ask for, where a preset
// would give a whole calendar day or week instead.
const lastSep = "last:"

// LastOf is the rolling window of d ending now; ok is false when d is
// not positive.
func LastOf(d time.Duration) (Period, bool) {
	if d <= 0 {
		return "", false
	}
	return Period(lastSep + d.String()), true
}

// Last is the length of a rolling window ("last:24h"), 0 when p is not
// one.
func (p Period) Last() time.Duration {
	s, ok := strings.CutPrefix(string(p), lastSep)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// bounds is when a picked range begins and the moment after it ends, in
// loc: midnight for an end that is a whole day, its minute for one that
// names it. ok is false for anything else, and for a range that ends
// before it begins or where it begins.
func (p Period) bounds(loc *time.Location) (since, until time.Time, ok bool) {
	from, to, found := strings.Cut(string(p), rangeSep)
	if !found {
		return
	}
	a, _, ok1 := rangeEnd(from, loc)
	b, timed, ok2 := rangeEnd(to, loc)
	if !ok1 || !ok2 {
		return
	}
	if !timed {
		b = b.AddDate(0, 0, 1)
	}
	if !a.Before(b) {
		return
	}
	return a, b, true
}

// timed is whether either end of a picked range names its minute.
func (p Period) timed() bool {
	from, to, _ := strings.Cut(string(p), rangeSep)
	return strings.Contains(from, "T") || strings.Contains(to, "T")
}

// IsRange is whether p is days the reader picked rather than a preset.
func (p Period) IsRange() bool {
	_, _, ok := p.bounds(time.UTC)
	return ok
}

// Known is whether p is a period the usage pages answer for: a preset, a
// picked range or a rolling window.
func (p Period) Known() bool {
	switch p {
	case Today, Week, Month, All:
		return true
	}
	return p.IsRange() || p.Last() > 0
}

// Until is when the period ends, as of now: the midnight after a picked
// range's last day, or the minute its end names, zero for a preset, which
// runs up to now.
func (p Period) Until(now time.Time) time.Time {
	if _, until, ok := p.bounds(now.Location()); ok {
		return until
	}
	return time.Time{}
}

// after is whether t falls past the period's end.
func after(until, t time.Time) bool { return !until.IsZero() && !t.Before(until) }

// shown is the period a summary says it is of: the preset or range asked
// for, and All for anything else.
func (p Period) shown() Period {
	if p == Today || p == Week || p == Month || p.IsRange() || p.Last() > 0 {
		return p
	}
	return All
}
