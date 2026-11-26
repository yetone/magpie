package settings

import (
	"testing"
	"time"
)

// Settings' time zone (JackyFill on X) is an IANA name or empty for the
// system's; anything else is refused on save, and read from a hand-edited
// file as the system's.
func TestTimeZoneIsANameOrTheSystems(t *testing.T) {
	for _, bad := range []string{"--help", "Mars/Olympus", "UTC+8", "../etc/passwd", "Local", " Asia/Tokyo"} {
		s := Load()
		s.TimeZone = bad
		if err := Save(s); err == nil {
			t.Errorf("%q was taken as a time zone", bad)
		}
		if (Settings{TimeZone: bad}).Location() != time.Local {
			t.Errorf("%q is not read as the system's", bad)
		}
	}
	for _, good := range []string{"Asia/Shanghai", "UTC", "America/Argentina/Buenos_Aires", ""} {
		s := Load()
		s.TimeZone = good
		if err := Save(s); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
		if got := Location(); good != "" && got.String() != good || good == "" && got != time.Local {
			t.Errorf("saved %q, Location() = %v", good, got)
		}
	}
}
