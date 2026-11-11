package access

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLimitWindow(t *testing.T) {
	loc := time.FixedZone("X", 8*3600)
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct{ period, now, start, reset string }{
		{"day", "2026-10-03 23:59", "2026-10-03 00:00", "2026-10-04 00:00"},
		{"day", "2026-12-31 00:00", "2026-12-31 00:00", "2027-01-01 00:00"},
		{"week", "2026-10-03 12:00", "2026-09-28 00:00", "2026-10-05 00:00"}, // a Saturday: from Monday
		{"week", "2026-10-04 23:00", "2026-09-28 00:00", "2026-10-05 00:00"}, // Sunday is the week's last day
		{"week", "2026-10-05 00:00", "2026-10-05 00:00", "2026-10-12 00:00"},
		{"month", "2026-10-31 18:00", "2026-10-01 00:00", "2026-11-01 00:00"},
		{"month", "2026-12-15 08:00", "2026-12-01 00:00", "2027-01-01 00:00"},
	} {
		start, reset := Window(c.period, at(c.now))
		if !start.Equal(at(c.start)) || !reset.Equal(at(c.reset)) {
			t.Errorf("%s at %s: %s – %s", c.period, c.now, start, reset)
		}
	}
}

func TestLimitValid(t *testing.T) {
	if l, err := (&Limit{Period: "day"}).Valid(); l != nil || err != nil {
		t.Fatal("no cap is no limit", l, err)
	}
	if l, err := (*Limit)(nil).Valid(); l != nil || err != nil {
		t.Fatal(l, err)
	}
	if l, err := (&Limit{Tokens: 5}).Valid(); err != nil || l.Period != "day" {
		t.Fatal("a period is a day unless said", l, err)
	}
	if l, err := (&Limit{Period: "week", Days: 9, Tokens: 5}).Valid(); err != nil || l.Days != 0 {
		t.Fatal("a calendar period has no days", l, err)
	}
	if l, err := (&Limit{Period: "days", Days: 10, Cost: 800}).Valid(); err != nil || l.Days != 10 {
		t.Fatal(l, err)
	}
	for _, bad := range []Limit{{Period: "hour", Tokens: 1}, {Period: "day", Tokens: -1}, {Period: "day", Cost: -2},
		{Period: "days", Tokens: 1}, {Period: "days", Days: -3, Tokens: 1}, {Period: "days", Days: MaxDays + 1, Tokens: 1}} {
		if _, err := bad.Valid(); err == nil {
			t.Error("accepted", bad)
		}
	}
}

// An N-day cycle (#1509) runs from when the limit was set, each next one
// N×24h of absolute time after the last: the same instants whatever the
// time zone the clock is read in, and a summer-time change doesn't move
// them.
func TestLimitDaysCycle(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	// set at 15:30 two days before New York's clocks went back (2026-11-01)
	since := time.Date(2026, 10, 30, 15, 30, 0, 0, ny)
	day := 24 * time.Hour
	l := &Limit{Period: "days", Days: 10, Cost: 800, Since: since}
	for _, c := range []struct {
		now         time.Time
		start, rest time.Duration // from since
	}{
		{since, 0, 10 * day},
		{since.Add(10*day - time.Nanosecond), 0, 10 * day},
		{since.Add(10 * day), 10 * day, 20 * day},
		{since.Add(57*day + 3*time.Hour), 50 * day, 60 * day},
	} {
		for _, loc := range []*time.Location{ny, time.UTC, time.FixedZone("UTC+8", 8*3600)} {
			start, reset := l.Window(c.now.In(loc))
			if !start.Equal(since.Add(c.start)) || !reset.Equal(since.Add(c.rest)) {
				t.Errorf("at %s: %s – %s, want %s – %s", c.now.In(loc), start, reset, since.Add(c.start), since.Add(c.rest))
			}
			if start.Location() != loc {
				t.Errorf("the window is said in %s, not the clock's %s", start.Location(), loc)
			}
		}
	}
	// across the change the day is 24 hours, so the wall clock moves
	one := &Limit{Period: "days", Days: 1, Tokens: 5, Since: since}
	start, reset := one.Window(since.Add(2*day + time.Hour))
	if reset.Sub(start) != day || reset.Hour() != 14 || reset.Minute() != 30 {
		t.Errorf("across summer time: %s – %s", start, reset)
	}
	// the clock set back before it was set: the first cycle
	if start, reset := l.Window(since.Add(-time.Hour)); !start.Equal(since) || !reset.Equal(since.Add(10*day)) {
		t.Errorf("before since: %s – %s", start, reset)
	}
	// written by hand without a since: from the Unix epoch, the same on every read
	hand := &Limit{Period: "days", Days: 7, Tokens: 5}
	a, _ := hand.Window(since)
	b, _ := hand.Window(since.Add(time.Hour))
	if !a.Equal(b) || a.Sub(time.Unix(0, 0))%(7*day) != 0 {
		t.Errorf("hand-written cycle: %s, %s", a, b)
	}
}

// A calendar limit reset (#1509) counts from the reset to the window's
// own end; a reset in an earlier window doesn't move the next one.
func TestLimitCalendarReset(t *testing.T) {
	loc := time.FixedZone("X", 8*3600)
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, loc) // a Wednesday
	reset := time.Date(2026, 10, 7, 9, 15, 0, 0, loc)
	l := &Limit{Period: "week", Tokens: 5, Since: reset}
	start, end := l.Window(now)
	if !start.Equal(reset) || !end.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, loc)) {
		t.Fatalf("reset this week: %s – %s", start, end)
	}
	start, end = l.Window(now.AddDate(0, 0, 7))
	if !start.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, loc)) || !end.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, loc)) {
		t.Fatalf("the week after: %s – %s", start, end)
	}
	// never reset: the calendar's
	l.Since = time.Time{}
	if start, _ := l.Window(now); !start.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, loc)) {
		t.Fatal(start)
	}
}

// Setting a limit: an N-day cycle starts when it is set and keeps its
// start while only its caps change; a new period or cycle starts over; a
// since the caller sends is never kept; a key from before #1509 keeps its
// calendar window untouched; Reset counts from now, tokens and cost both.
func TestLimitSetAndReset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clock := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	Now = func() time.Time { return clock }
	t.Cleanup(func() { Now = time.Now })
	// a key as an older magpie kept it
	old := `[{"id":"k1","name":"Old","secret":"sk-magpie-key-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","limit":{"period":"week","tokens":5000}}]`
	if err := os.MkdirAll(strings.TrimSuffix(Path(), "caller-keys.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	limitOf := func() *Limit {
		t.Helper()
		keys, err := List()
		if err != nil || len(keys) != 1 {
			t.Fatal(keys, err)
		}
		return keys[0].Limit
	}
	set := func(l *Limit) {
		t.Helper()
		if _, err := Update("limit-key", Change{Key: "k1", Limit: l}); err != nil {
			t.Fatal(err)
		}
	}
	if l := limitOf(); l.Period != "week" || l.Tokens != 5000 || !l.Since.IsZero() {
		t.Fatalf("an old limit read as %+v", l)
	}
	// renamed: the limit is as it was, byte for byte
	if _, err := Update("rename-key", Change{Key: "k1", Name: "Older"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(Path()); strings.Contains(string(b), "since") || strings.Contains(string(b), "days") {
		t.Fatalf("a rename wrote %s", b)
	}
	set(&Limit{Period: "week", Tokens: 6000})
	if l := limitOf(); !l.Since.IsZero() {
		t.Fatalf("a calendar limit's new cap gave it a since: %+v", l)
	}
	set(&Limit{Period: "days", Days: 10, Cost: 800, Since: clock.Add(-99 * time.Hour)})
	if l := limitOf(); l.Period != "days" || l.Days != 10 || !l.Since.Equal(clock) {
		t.Fatalf("a 10-day cycle set at %s: %+v", clock, l)
	}
	set1 := clock
	clock = clock.Add(50 * time.Hour)
	set(&Limit{Period: "days", Days: 10, Cost: 900, Tokens: 1000})
	if l := limitOf(); !l.Since.Equal(set1) || l.Cost != 900 {
		t.Fatalf("a new cap restarted the cycle: %+v", l)
	}
	set(&Limit{Period: "days", Days: 7, Cost: 900})
	if l := limitOf(); !l.Since.Equal(clock) {
		t.Fatalf("a new cycle length kept the old start: %+v", l)
	}
	clock = clock.Add(time.Hour)
	if _, err := Update("reset-limit-key", Change{Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if l := limitOf(); !l.Since.Equal(clock) || l.Days != 7 || l.Cost != 900 {
		t.Fatalf("reset: %+v", l)
	}
	// the store reloads as a restart would
	if who, ok := Authenticate("sk-magpie-key-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !ok || !who.Limit.Since.Equal(clock) {
		t.Fatalf("after a reload: %+v", who.Limit)
	}
	set(&Limit{Period: "month", Tokens: 10})
	if l := limitOf(); !l.Since.IsZero() || l.Days != 0 {
		t.Fatalf("back to a calendar month: %+v", l)
	}
	clock = clock.Add(time.Hour)
	if _, err := Update("reset-limit-key", Change{Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if l := limitOf(); !l.Since.Equal(clock) || l.Period != "month" {
		t.Fatalf("a calendar reset: %+v", l)
	}
	set(nil)
	if _, err := Update("reset-limit-key", Change{Key: "k1"}); err == nil {
		t.Fatal("reset a key with no limit")
	}
	if _, err := Update("reset-limit-key", Change{Key: "nope"}); err == nil {
		t.Fatal("reset a missing key")
	}
}

// An "hours" limit (#2102) runs cycles of N hours from when it was set,
// in absolute time like a "days" one: a 5-hour key turns over every 5
// hours whatever the clock or zone says.
func TestLimitHoursCycle(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	// set at 23:10 the night New York's clocks went back (2026-11-01 02:00)
	since := time.Date(2026, 10, 31, 23, 10, 0, 0, ny)
	h := time.Hour
	l := &Limit{Period: "hours", Hours: 5, Tokens: 200_000, Since: since}
	for _, c := range []struct {
		now         time.Time
		start, rest time.Duration // from since
	}{
		{since, 0, 5 * h},
		{since.Add(5*h - time.Nanosecond), 0, 5 * h},
		{since.Add(5 * h), 5 * h, 10 * h},
		{since.Add(23*h + 59*time.Minute), 20 * h, 25 * h},
		{since.Add(24 * 7 * h), 165 * h, 170 * h},
	} {
		for _, loc := range []*time.Location{ny, time.UTC, time.FixedZone("UTC+8", 8*3600)} {
			start, reset := l.Window(c.now.In(loc))
			if !start.Equal(since.Add(c.start)) || !reset.Equal(since.Add(c.rest)) {
				t.Errorf("at %s: %s – %s, want %s – %s", c.now.In(loc), start, reset, since.Add(c.start), since.Add(c.rest))
			}
		}
	}
	if got := l.Per(); got != "5-hour cycle" {
		t.Errorf("Per: %q", got)
	}
	for _, c := range []struct {
		in Limit
		ok bool
	}{
		{Limit{Period: "hours", Hours: 5, Tokens: 1}, true},
		{Limit{Period: "hours", Hours: 1, Tokens: 1}, true},
		{Limit{Period: "hours", Hours: MaxHours, Tokens: 1}, true},
		{Limit{Period: "hours", Hours: 0, Tokens: 1}, false},
		{Limit{Period: "hours", Hours: MaxHours + 1, Tokens: 1}, false},
		{Limit{Period: "hours", Hours: -5, Tokens: 1}, false},
	} {
		v, err := c.in.Valid()
		if (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.in, err)
		}
		if err == nil && (v.Hours != c.in.Hours || v.Days != 0) {
			t.Errorf("%+v kept as %+v", c.in, v)
		}
	}
	// a stray Hours on another period isn't kept
	if v, _ := (&Limit{Period: "days", Days: 2, Hours: 5, Tokens: 1}).Valid(); v.Hours != 0 {
		t.Errorf("days kept hours: %+v", v)
	}
	if v, _ := (&Limit{Period: "day", Hours: 5, Tokens: 1}).Valid(); v.Hours != 0 {
		t.Errorf("day kept hours: %+v", v)
	}
}

// Setting an "hours" limit starts its cycle; a new cap keeps it, a new N
// or a switch from N days to N hours starts a new one, and Reset usage
// starts one from then (#2102).
func TestLimitHoursSetAndReset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clock := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	Now = func() time.Time { return clock }
	t.Cleanup(func() { Now = time.Now })
	k := Key{ID: "k2"}
	keys := `[{"id":"k2","name":"Family","secret":"sk-magpie-key-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`
	if err := os.MkdirAll(strings.TrimSuffix(Path(), "caller-keys.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(keys), 0o600); err != nil {
		t.Fatal(err)
	}
	limitOf := func() *Limit {
		t.Helper()
		keys, err := List()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range keys {
			if x.ID == k.ID {
				return x.Limit
			}
		}
		t.Fatal("no key")
		return nil
	}
	set := func(l *Limit) {
		t.Helper()
		if _, err := Update("limit-key", Change{Key: k.ID, Limit: l}); err != nil {
			t.Fatal(err)
		}
	}
	set(&Limit{Period: "days", Days: 5, Tokens: 1000})
	clock = clock.Add(time.Hour)
	set(&Limit{Period: "hours", Hours: 5, Tokens: 1000})
	if l := limitOf(); l.Period != "hours" || l.Hours != 5 || l.Days != 0 || !l.Since.Equal(clock) {
		t.Fatalf("5 days to 5 hours: %+v", l)
	}
	set1 := clock
	clock = clock.Add(3 * time.Hour)
	set(&Limit{Period: "hours", Hours: 5, Tokens: 2000})
	if l := limitOf(); !l.Since.Equal(set1) {
		t.Fatalf("a new cap restarted the cycle: %+v", l)
	}
	set(&Limit{Period: "hours", Hours: 6, Tokens: 2000})
	if l := limitOf(); !l.Since.Equal(clock) {
		t.Fatalf("a new N kept the old start: %+v", l)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := Update("reset-limit-key", Change{Key: k.ID}); err != nil {
		t.Fatal(err)
	}
	l := limitOf()
	if !l.Since.Equal(clock) || l.Hours != 6 {
		t.Fatalf("reset: %+v", l)
	}
	if start, reset := l.Window(clock.Add(time.Minute)); !start.Equal(clock) || !reset.Equal(clock.Add(6*time.Hour)) {
		t.Fatalf("after reset: %s – %s", start, reset)
	}
}

// A key holds several limits at once (#2227): the first stays where an
// older magpie reads it, the rest go beside it, and each is added,
// changed, removed and reset on its own.
func TestSeveralLimits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clock := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	Now = func() time.Time { return clock }
	t.Cleanup(func() { Now = time.Now })
	secret, err := Update("add-key", Change{Name: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[0].ID
	at := func(n int, l *Limit) error {
		_, err := Update("limit-key", Change{Key: id, Limit: l, Index: &n})
		return err
	}
	limits := func() []*Limit {
		t.Helper()
		keys, err := List()
		if err != nil || len(keys) != 1 {
			t.Fatal(keys, err)
		}
		return keys[0].Limits()
	}
	if err := at(0, &Limit{Period: "hours", Hours: 5, Cost: 10}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if err := at(1, &Limit{Period: "week", Cost: 50}); err != nil {
		t.Fatal(err)
	}
	if err := at(2, &Limit{Period: "days", Days: 30, Tokens: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	ls := limits()
	if len(ls) != 3 || ls[0].Hours != 5 || ls[1].Period != "week" || ls[2].Days != 30 || !ls[2].Since.Equal(clock) {
		t.Fatalf("three limits kept as %+v %+v %+v", ls[0], ls[1], ls[2])
	}
	// the first is where an older magpie reads a key's limit
	b, _ := os.ReadFile(Path())
	var saved []map[string]json.RawMessage
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved[0]["limit"]), `"hours": 5`) || !strings.Contains(string(saved[0]["moreLimits"]), `"week"`) {
		t.Fatalf("saved as %s", b)
	}
	if err := at(3, &Limit{Period: "week", Tokens: 5}); err == nil || !strings.Contains(err.Error(), "already has a limit") {
		t.Fatalf("a second week limit: %v", err)
	}
	if err := at(5, &Limit{Period: "day", Tokens: 5}); err == nil {
		t.Fatal("a limit past the end was added")
	}
	if err := at(3, &Limit{Period: "day", Tokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := at(4, &Limit{Period: "month", Tokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := at(5, &Limit{Period: "hours", Hours: 1, Tokens: 5}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("a sixth limit: %v", err)
	}
	// changing one keeps its cycle and leaves the others alone
	clock = clock.Add(time.Hour)
	if err := at(2, &Limit{Period: "days", Days: 30, Tokens: 2_000_000}); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); len(ls) != 5 || ls[2].Tokens != 2_000_000 || !ls[2].Since.Equal(clock.Add(-time.Hour)) || ls[0].Cost != 10 {
		t.Fatalf("after a change: %+v", ls[2])
	}
	// one reset, then all
	if _, err := Update("reset-limit-key", Change{Key: id, Index: new(1)}); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); !ls[1].Since.Equal(clock) || ls[0].Since.Equal(clock) {
		t.Fatalf("reset one: %+v %+v", ls[0], ls[1])
	}
	clock = clock.Add(time.Hour)
	if _, err := Update("reset-limit-key", Change{Key: id}); err != nil {
		t.Fatal(err)
	}
	for i, l := range limits() {
		if !l.Since.Equal(clock) {
			t.Fatalf("reset all left limit %d at %s", i, l.Since)
		}
	}
	// removing the first moves the next into its place
	for range 4 {
		if err := at(0, nil); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ = List()
	if l := keys[0].Limit; l == nil || l.Period != "month" || keys[0].More != nil {
		t.Fatalf("after removing four: %+v %+v", l, keys[0].More)
	}
	// the single-limit form makes one the only limit again
	at(1, &Limit{Period: "day", Tokens: 9})
	if _, err := Update("limit-key", Change{Key: id, Limit: &Limit{Period: "week", Tokens: 7}}); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); len(ls) != 1 || ls[0].Period != "week" {
		t.Fatalf("the single form left %d limits", len(ls))
	}
	who, ok := Authenticate(secret)
	if !ok || len(who.Limits()) != 1 {
		t.Fatalf("authenticated with %+v", who)
	}
	at(1, &Limit{Period: "day", Tokens: 9})
	if who, _ := Authenticate(secret); len(who.Limits()) != 2 || who.More[0].Period != "day" {
		t.Fatalf("authenticated without its second limit: %+v", who.More)
	}
}

// A "total" limit (@Cyrus_4433 on X) is a kept period: it starts when set
// or reset, ends a century off, and an old file without it reads the same.
func TestLimitTotal(t *testing.T) {
	since := time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC)
	l := &Limit{Period: "total", Cost: 300, Since: since}
	for _, now := range []time.Time{since, since.AddDate(0, 7, 0), since.AddDate(40, 0, 0)} {
		start, reset := l.Window(now)
		if !start.Equal(since) || !reset.After(now.AddDate(50, 0, 0)) {
			t.Errorf("at %s: %s – %s", now, start, reset)
		}
	}
	if start, _ := (&Limit{Period: "total", Cost: 1}).Window(since); !start.Equal(epoch) {
		t.Errorf("no Since starts at %s", start)
	}
	v, err := (&Limit{Period: "total", Days: 3, Hours: 4, Cost: 300}).Valid()
	if err != nil || v.Period != "total" || v.Days != 0 || v.Hours != 0 || v.Per() != "total" {
		t.Fatalf("%+v %v", v, err)
	}
	if v, _ := (&Limit{Period: "month", Cost: 5}).Valid(); v.Period != "month" {
		t.Errorf("month: %+v", v)
	}
	// setting it starts it, a new cap keeps it, a period change starts anew
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	if got := sinceOf(nil, v0("total"), now); !got.Equal(now) {
		t.Errorf("new total since %s", got)
	}
	if got := sinceOf(&Limit{Period: "total", Since: since}, v0("total"), now); !got.Equal(since) {
		t.Errorf("a new cap restarted it: %s", got)
	}
	if got := sinceOf(&Limit{Period: "month"}, v0("total"), now); !got.Equal(now) {
		t.Errorf("month to total since %s", got)
	}
	if got := sinceOf(&Limit{Period: "total", Since: since}, v0("month"), now); !got.IsZero() {
		t.Errorf("total to month since %s", got)
	}
}

func v0(period string) *Limit { return &Limit{Period: period, Cost: 1} }
