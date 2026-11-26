package settings

import (
	"sync"
	"time"

	_ "time/tzdata" // every zone, Windows' too, which has no database of its own
)

var timeZones sync.Map // name → *time.Location, each loaded once

// zoneNamed is the zone an IANA name names, and whether this magpie knows
// it. "Local" is not a name: it is whatever the system is.
func zoneNamed(name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		return nil, false
	}
	if l, ok := timeZones.Load(name); ok {
		return l.(*time.Location), true
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, false // not kept: a file's text can be anything
	}
	l2, _ := timeZones.LoadOrStore(name, l)
	return l2.(*time.Location), true
}

// ValidTimeZone says whether name is a zone TimeZone can be: an IANA name
// this magpie knows ("Asia/Shanghai", "UTC").
func ValidTimeZone(name string) bool {
	_, ok := zoneNamed(name)
	return ok
}

// Location is the zone this magpie's days and times are in: TimeZone when
// it is set, else the system's (time.Local). Every place that cuts a day,
// a week or a month for the user asks it (the Usage and Sessions pages,
// the gateway keys' daily and calendar caps), so a magpie on a server and
// one on the desk set to one zone count the same days. A zone this magpie
// doesn't know (settings.json edited by hand) is the system's, as unset.
func (s Settings) Location() *time.Location {
	if l, ok := zoneNamed(s.TimeZone); ok {
		return l
	}
	return time.Local
}

// Location is Shared().Location(): the zone of this magpie's days now.
func Location() *time.Location { return Shared().Location() }

// OwnZone is the zone TimeZone names, nil when it names none and the days
// are the system's: for a reader that keeps it, so that the system's is
// still time.Local as it is when used.
func OwnZone() *time.Location {
	l, _ := zoneNamed(Shared().TimeZone)
	return l
}
