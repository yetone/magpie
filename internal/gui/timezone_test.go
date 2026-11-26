package gui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

// Settings' time zone (JackyFill on X) is saved from the page, kept by a
// save that doesn't send it, refused when it isn't a zone, and carried to
// the page by boot.js with tzshim.js, which puts the page's Date and Intl
// in it; with none set, boot.js is as before.
func TestTimeZoneSetting(t *testing.T) {
	srv := appearanceHome(t)
	boot := func() string { return lookServe(srv, "GET", "/boot.js", "").Body.String() }
	if b := boot(); strings.Contains(b, "timeZone") || strings.Contains(b, "ZDate") {
		t.Fatalf("boot.js with no zone set names one: %s", b)
	}
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"dark","lang":"en","timeZone":"America/Los_Angeles"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().TimeZone; got != "America/Los_Angeles" {
		t.Fatalf("saved zone %q", got)
	}
	b := boot()
	at := strings.Index(b, `"timeZone":"America/Los_Angeles"`)
	shim := strings.Index(b, "window.Date = ZDate")
	if at < 0 || shim < at {
		t.Fatalf("boot.js doesn't carry the zone and then the shim: %s", b)
	}
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en"}`); rec.Code != http.StatusOK || settings.Load().TimeZone != "America/Los_Angeles" {
		t.Fatalf("a save without the zone changed it: %d %q", rec.Code, settings.Load().TimeZone)
	}
	for _, bad := range []string{"Mars/Olympus", "UTC+8", "</script>", "Local"} {
		body := `{"theme":"light","lang":"en","timeZone":"` + strings.ReplaceAll(bad, `"`, `\"`) + `"}`
		if rec := lookServe(srv, "POST", "/api/settings", body); rec.Code == http.StatusOK || settings.Load().TimeZone != "America/Los_Angeles" {
			t.Fatalf("zone %q was taken: %d", bad, rec.Code)
		}
	}
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en","timeZone":""}`); rec.Code != http.StatusOK || settings.Load().TimeZone != "" {
		t.Fatalf("zone not reset: %d %q", rec.Code, settings.Load().TimeZone)
	}
	if b := boot(); strings.Contains(b, "timeZone") || strings.Contains(b, "ZDate") {
		t.Fatalf("boot.js still names a zone: %s", b)
	}
}

// Vectorpan on X keeps the computer in Los Angeles' time, the zone of a
// VPS, and wants magpie alone in China's: with the setting at
// Asia/Shanghai, the menu bar's "as of", a quota alert's reset clock and
// the day the menu bar's Today total is for are Shanghai's. The computer's
// zone is held at Los Angeles, whatever TZ the run has.
func TestTrayTimesInTheZoneSet(t *testing.T) {
	la, err1 := time.LoadLocation("America/Los_Angeles")
	sh, err2 := time.LoadLocation("Asia/Shanghai")
	if err1 != nil || err2 != nil {
		t.Skip(err1, err2)
	}
	appearanceHome(t)
	testenv.Zone(t, la)
	zone := func(name string) {
		s := settings.Load()
		s.TimeZone = name
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { zone("") })
	read := time.Date(2026, 9, 27, 1, 5, 0, 0, time.UTC)        // Sep 27 09:05 in Shanghai, Sep 26 18:05 in LA
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)        // 23:00 in Shanghai, 08:00 in LA
	reset := time.Date(2026, 9, 28, 17, 30, 0, 0, time.UTC)     // 01:30 tomorrow in Shanghai, 10:30 today in LA
	midnight := time.Date(2026, 9, 28, 15, 59, 30, 0, time.UTC) // 30s before Shanghai's midnight
	asOf := func() string { return trayAsOf("en", provider.SubscriptionQuota{AsOf: &read}) }
	clocks := func() []string {
		return []string{provider.ResetClock(reset, now), alertClockZh(reset, now), alertClockJa(reset, now), alertClockDe(reset, now)}
	}
	summed := func() int {
		var n int
		old := trayQuerySum
		trayQuerySum = func(usage.Period) usage.Totals { n++; return usage.Totals{} }
		defer func() { trayQuerySum = old }()
		var m trayTotalMemo
		m.sum(usage.Today, midnight)
		m.sum(usage.Today, midnight.Add(40*time.Second))
		return n
	}

	zone("Asia/Shanghai")
	if settings.Location().String() != sh.String() {
		t.Fatalf("zone %v", settings.Location())
	}
	if got := asOf(); got != "\nas of Sep 27 09:05, couldn't be read just now" {
		t.Errorf("Shanghai: as of %q", got)
	}
	if got, want := clocks(), []string{"tomorrow 01:30", "明天 01:30", "明日 01:30", "morgen um 01:30"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Shanghai: resets %q, want %q", got, want)
	}
	if n := summed(); n != 2 {
		t.Errorf("Shanghai: Today summed %d times across its midnight, want 2", n)
	}

	zone("")
	if got := asOf(); got != "\nas of Sep 26 18:05, couldn't be read just now" {
		t.Errorf("system: as of %q", got)
	}
	if got, want := clocks(), []string{"10:30", "10:30", "10:30", "um 10:30"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("system: resets %q, want %q", got, want)
	}
	if n := summed(); n != 1 {
		t.Errorf("system: Today summed %d times within LA's day, want 1", n)
	}
}
