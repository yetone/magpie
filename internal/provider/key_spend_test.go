package provider

import (
	"strings"
	"testing"
	"time"
)

// A key's spend cap (sparkling_lemur_62868 on Discord) is set by the key's
// id or name, kept by its fingerprint in lower case, and taken off with
// nil. A cap in all counts from when it was set, and keeps that start
// while it stays one in all, its amount changed; a cap a day or a month
// keeps none. A key the provider hasn't and a cap out of range are
// refused, and a key's value replaced keeps its cap.
func TestKeySpendCapSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-first", KeyName: "Team",
		Keys: []KeyAccount{{Name: "Spare", Key: "sk-spare"}}}); err != nil {
		t.Fatal(err)
	}
	first, spare := keyID("sk-first"), keyID("sk-spare")
	capOf := func(ref string) (SpendCap, bool) {
		t.Helper()
		p, err := Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeySpendCap(ref)
	}
	if err := SetKeySpendCap("relay", "Team", &SpendCap{USD: 20, Period: SpendDay}); err != nil {
		t.Fatal(err)
	}
	if c, ok := capOf(strings.ToUpper(first)); !ok || c.USD != 20 || c.Period != SpendDay || !c.Since.IsZero() {
		t.Fatalf("Team's cap %+v %v", c, ok)
	}
	if _, ok := capOf(spare); ok {
		t.Fatal("Spare got a cap it wasn't given")
	}
	before := time.Now().UTC().Truncate(time.Second)
	if err := SetKeySpendCap("relay", spare, &SpendCap{USD: 100, Period: SpendTotal}); err != nil {
		t.Fatal(err)
	}
	c, ok := capOf(spare)
	if !ok || c.Period != SpendTotal || c.Since.Before(before) || c.Since.After(time.Now()) {
		t.Fatalf("Spare's cap in all %+v %v", c, ok)
	}
	// set in all a while back, raised: it still counts from then
	p, _ := Find("relay")
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p.KeySpendCaps[spare] = SpendCap{USD: 100, Period: SpendTotal, Since: since}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := SetKeySpendCap("relay", "Spare", &SpendCap{USD: 150, Period: SpendTotal}); err != nil {
		t.Fatal(err)
	}
	if c, _ := capOf(spare); c.USD != 150 || !c.Since.Equal(since) {
		t.Fatalf("raised in all, it counts from %v, not %v: %+v", c.Since, since, c)
	}
	// a month: no start of its own; in all again counts from now
	if err := SetKeySpendCap("relay", spare, &SpendCap{USD: 150, Period: SpendMonth}); err != nil {
		t.Fatal(err)
	}
	if c, _ := capOf(spare); c.Period != SpendMonth || !c.Since.IsZero() {
		t.Fatalf("a month's cap %+v", c)
	}
	for _, bad := range []SpendCap{{USD: 0, Period: SpendDay}, {USD: -1, Period: SpendDay}, {USD: MaxSpendCap + 1, Period: SpendDay}, {USD: 5, Period: "week"}} {
		if err := SetKeySpendCap("relay", spare, &bad); err == nil {
			t.Fatalf("%+v was taken", bad)
		}
	}
	if err := SetKeySpendCap("relay", "nobody", &SpendCap{USD: 1, Period: SpendDay}); err == nil {
		t.Fatal("a key the provider hasn't got a cap")
	}
	// the value replaced, the cap goes with the key
	if _, _, err := ReplaceKey("relay", first, "sk-first-2"); err != nil {
		t.Fatal(err)
	}
	if c, ok := capOf(keyID("sk-first-2")); !ok || c.USD != 20 {
		t.Fatalf("replaced, Team's cap %+v %v", c, ok)
	}
	if err := SetKeySpendCap("relay", "Team", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := capOf(keyID("sk-first-2")); ok {
		t.Fatal("the cap taken off is still there")
	}
}

// A day's cap counts from local midnight to the next, a month's from the
// 1st to the next month's, and one in all from its Since, never renewing.
func TestSpendCapWindow(t *testing.T) {
	loc := time.FixedZone("far", 13*3600)
	now := time.Date(2026, 12, 31, 23, 30, 0, 0, loc)
	s, r := SpendCap{USD: 1, Period: SpendDay}.Window(now)
	if !s.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, loc)) || !r.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("day %v %v", s, r)
	}
	s, r = SpendCap{USD: 1, Period: SpendMonth}.Window(now)
	if !s.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, loc)) || !r.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month %v %v", s, r)
	}
	since := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	s, r = SpendCap{USD: 1, Period: SpendTotal, Since: since}.Window(now)
	if !s.Equal(since) || !r.IsZero() {
		t.Fatalf("in all %v %v", s, r)
	}
}

// The CLI's words for a cap: an amount, $ or not, a period by any of its
// names, a day when none; off for none; anything else refused.
func TestParseSpendCap(t *testing.T) {
	for _, c := range []struct {
		amount, period string
		want           *SpendCap
	}{
		{"20", "", &SpendCap{USD: 20, Period: SpendDay}},
		{"$12.5", "monthly", &SpendCap{USD: 12.5, Period: SpendMonth}},
		{"100", "total", &SpendCap{USD: 100, Period: SpendTotal}},
		{"100", "all", &SpendCap{USD: 100, Period: SpendTotal}},
		{"off", "", nil},
	} {
		got, err := ParseSpendCap(c.amount, c.period)
		if err != nil || (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Fatalf("%q %q: %+v %v", c.amount, c.period, got, err)
		}
	}
	for _, bad := range [][2]string{{"--help", ""}, {"twenty", ""}, {"0", ""}, {"-5", ""}, {"5", "week"}, {"NaN", ""}, {"2000000", ""}} {
		if _, err := ParseSpendCap(bad[0], bad[1]); err == nil {
			t.Fatalf("%q %q was taken", bad[0], bad[1])
		}
	}
}
