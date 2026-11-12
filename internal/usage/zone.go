package usage

import (
	"sync"
	"time"

	"github.com/yetone/magpie/internal/dayzone"
	"github.com/yetone/magpie/internal/settings"
)

// The Usage page buckets its calls by the reader's days and hours, not the
// computer's: `magpie web` read from across an ocean showed a call at 9:00
// in UTC+8 under 1:00 (Outvi V on Discord). The page names its browser's
// zone, and Zone is that zone here.

var zones sync.Map // name → *time.Location, one for each name

// Zone is the time zone a browser names (an IANA name such as
// "Asia/Shanghai"), the same *time.Location for every ask of one name, so
// that a Filter holding it is the same cache key each time. A zone that
// keeps this computer's clock, the page on this computer, is time.Local,
// whose answers the tray and the TUI share. An empty name or one this
// computer doesn't know is time.Local: the computer's days, as before.
//
// With a day that starts after midnight (Settings' dayStart, ssharpcc on
// X), the zone is that zone moved so its midnight is that hour (dayzone):
// every day and hour the page cuts by it starts there.
func Zone(name string) *time.Location {
	return dayzone.Shift(zoneNamed(name), settings.Shared().DayStart)
}

func zoneNamed(name string) *time.Location {
	if name == "" || name == "Local" {
		return time.Local
	}
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local // not kept: any text can be asked for
	}
	if sameClock(loc, time.Local) {
		loc = time.Local
	}
	l, _ := zones.LoadOrStore(name, loc)
	return l.(*time.Location)
}

// sameClock is whether a and b tell the same offset every twelve hours
// from three years back to a year on: the span the Usage page's days reach
// (its heatmap is a year), and either side of every daylight-saving change
// in it. Worked out once for each name.
func sameClock(a, b *time.Location) bool {
	y := time.Now().Year()
	end := time.Date(y+2, time.January, 1, 0, 0, 0, 0, time.UTC)
	for t := time.Date(y-3, time.January, 1, 0, 0, 0, 0, time.UTC); t.Before(end); t = t.Add(12 * time.Hour) {
		_, oa := t.In(a).Zone()
		_, ob := t.In(b).Zone()
		if oa != ob {
			return false
		}
	}
	return true
}

// loc is the zone f reads its days in: the reader's, else this computer's.
func (f Filter) loc() *time.Location {
	if f.Zone != nil {
		return f.Zone
	}
	return Zone("")
}

// localNow is now in this computer's days (Zone("")): where nothing names a
// reader's zone, the TUI, the CLI and the tray.
func localNow() time.Time { return Clock().In(Zone("")) }
