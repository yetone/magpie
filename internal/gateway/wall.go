package gateway

import "time"

// onWall gives t on the wall clock alone, for a deadline magpie keeps for
// minutes to days: a rest, a hold, a window's reset. A time from
// time.Now() also carries the monotonic clock, and Go compares two such
// times by it; on macOS it stops while the computer sleeps, so a rest set
// before a night's sleep ran that night longer than the time it shows
// (plugins#1126: a rest to 00:40 still held at 09:57). On the wall clock
// alone, Before, After, Sub and time.Until read the time it says.
func onWall(t time.Time) time.Time { return t.Round(0) }
