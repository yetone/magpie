package sessions

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func zoneSet(t *testing.T, name string) {
	t.Helper()
	s := settings.Load()
	s.TimeZone = name
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	zone() // read again, as each load of the cache does
}

// A magpie set to a time zone of its own (JackyFill on X) dates its
// sessions' days in it, not in the computer's, and the parses kept for the
// computer's zone are read again. A reply at 03:00 UTC on Sep 30 is Sep
// 29's in Los Angeles and Sep 30's in Tokyo; the zone set is whichever of
// the two dates it apart from the computer's. Run under TZ=UTC,
// TZ=Pacific/Kiritimati and TZ=America/Los_Angeles.
func TestSessionDaysFollowTheTimeZoneSetting(t *testing.T) {
	name := "America/Los_Angeles"
	at := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	mine := at.In(time.Local).Format(time.DateOnly)
	if mine == "2026-09-29" {
		name = "Asia/Tokyo"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip(err)
	}
	theirs := at.In(loc).Format(time.DateOnly)
	t.Cleanup(func() { zoneSet(t, "") })
	replies := func(s *state, date string) int {
		if d := s.Days.of(date); d != nil {
			return d.Replies
		}
		return 0
	}
	parse := func() *state {
		s := &state{}
		claudeLine(s, claudeUsageLine(at, "a", "req-a", "m", Tokens{10, 2, 0, 0, 0}, "", false), true)
		return s
	}
	zoneSet(t, "")
	before := zone()
	if s := parse(); replies(s, mine) != 1 {
		t.Fatalf("unset: the reply is not %s's, the computer's", mine)
	}
	zoneSet(t, name)
	if zone() == before {
		t.Errorf("the cache's zone is %s with %s set, as for the computer's: its parses would be kept", before, name)
	}
	s := parse()
	if replies(s, theirs) != 1 || replies(s, mine) != 0 {
		t.Errorf("set to %s: the reply is not %s's", name, theirs)
	}
	if d := dateOf(at); d != theirs {
		t.Errorf("dateOf = %s, want %s", d, theirs)
	}
	zoneSet(t, "")
	if d := dateOf(at); d != mine || zone() != before {
		t.Errorf("unset again: dateOf = %s, zone %s; want the computer's", d, zone())
	}
}
