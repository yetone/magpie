package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A rule's hours are looked at as a turn begins, on the clock then: a turn
// begun inside them goes to its member and stays there for its tool
// rounds after they end; a turn begun outside them isn't the rule's (and the
// conversation goes where the group routes it then:
// TestRuleHoursOverGiveTheConversationBack).
func TestRuleTimeWindowAtTurnStart(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.Local) // a Tuesday
	ruleClock = func() time.Time { return now }
	t.Cleanup(func() { ruleClock = time.Now })
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Time: &provider.TimeWindow{From: "09:00", To: "18:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}}})
	post := func(body string, session string) (string, Route) {
		t.Helper()
		code, out := postAs(t, s, session, body)
		if code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		return out, lastRoute(s)
	}
	// turn 1 begins inside the hours: the rule's member, the trace says why
	out, r := post(chat("hello", nil, 0, ""), "sess")
	if !strings.Contains(out, "from kb") || r.Rule == nil || r.Rule.N != 1 || r.Rule.Use != "b/big" || strings.Join(r.Rule.When, " · ") != "time 09:00–18:00 Mon–Fri" {
		t.Fatalf("turn 1: %s %+v", out, r.Rule)
	}
	// its tool rounds after the hours end stay where it began
	now = time.Date(2026, 9, 29, 18, 30, 0, 0, time.Local)
	out, r = post(chat("hello", nil, 1, ""), "sess")
	if !strings.Contains(out, "from kb") || !r.Rule.Held || r.Rule.N != 1 {
		t.Fatalf("turn 1 tool round: %s %+v", out, r.Rule)
	}
	// turn 2 begins after them: no rule, and the group goes in order, so
	// the conversation goes back to its first member
	if out, r = post(chat("hello", []string{"more"}, 0, ""), "sess"); !strings.Contains(out, "from ka") || r.Rule.N != 0 || r.Rule.Held {
		t.Fatalf("turn 2: %s %+v", out, r.Rule)
	}
	// nor another conversation's: the group's order
	if out, r = post(chat("hi", nil, 0, ""), "s2"); !strings.Contains(out, "from ka") || r.Rule.N != 0 {
		t.Fatalf("after hours: %s %+v", out, r.Rule)
	}
	// a turn begun before them stays off the rule when they open mid-turn
	now = time.Date(2026, 9, 30, 8, 59, 0, 0, time.Local)
	if out, r = post(chat("hello", nil, 0, ""), "s3"); !strings.Contains(out, "from ka") || r.Rule.N != 0 {
		t.Fatalf("before hours: %s %+v", out, r.Rule)
	}
	now = time.Date(2026, 9, 30, 9, 1, 0, 0, time.Local)
	if out, r = post(chat("hello", nil, 1, ""), "s3"); !strings.Contains(out, "from ka") || !r.Rule.Held || r.Rule.N != 0 {
		t.Fatalf("before hours, tool round: %s %+v", out, r.Rule)
	}
	// on a Saturday, the hours don't hold
	now = time.Date(2026, 10, 3, 10, 0, 0, 0, time.Local)
	if out, r = post(chat("hello", nil, 0, ""), "s4"); !strings.Contains(out, "from ka") || r.Rule.N != 0 {
		t.Fatalf("saturday: %s %+v", out, r.Rule)
	}
	if a.n() != 5 || b.n() != 2 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A rule's hours set in a zone hold at that zone's hours, on a server
// whose clock reads UTC (gaubeebangeel on X: magpie on a cloud server, its
// user in Beijing, the hours 8 off): the instants here are UTC, as such a
// server has them, and the zone is one hours away from this computer's.
func TestRuleTimeWindowInItsZoneAtTurnStart(t *testing.T) {
	zone, loc := "", (*time.Location)(nil)
	for _, name := range []string{"Asia/Shanghai", "America/New_York"} {
		l, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		_, a := time.Date(2026, 9, 29, 12, 0, 0, 0, l).Zone()
		_, b := time.Date(2026, 9, 29, 12, 0, 0, 0, l).In(time.Local).Zone()
		if d := a - b; d >= 4*3600 || d <= -4*3600 {
			zone, loc = name, l
			break
		}
	}
	if loc == nil {
		t.Fatal("no zone far from this computer's")
	}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, loc).UTC() // Tuesday 10:00 there
	ruleClock = func() time.Time { return now }
	t.Cleanup(func() { ruleClock = time.Now })
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Time: &provider.TimeWindow{From: "09:00", To: "18:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Zone: zone}})
	post := func(session string) (string, Route) {
		t.Helper()
		code, out := postAs(t, s, session, chat("hello", nil, 0, ""))
		if code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		return out, lastRoute(s)
	}
	out, r := post("s1")
	if !strings.Contains(out, "from kb") || r.Rule == nil || r.Rule.N != 1 || strings.Join(r.Rule.When, " · ") != "time 09:00–18:00 Mon–Fri ("+zone+")" {
		t.Fatalf("10:00 in %s: %s %+v", zone, out, r.Rule)
	}
	// 8:30 there is outside, whatever the server's clock says
	now = time.Date(2026, 9, 29, 8, 30, 0, 0, loc).UTC()
	if out, r = post("s2"); !strings.Contains(out, "from ka") || r.Rule.N != 0 {
		t.Fatalf("08:30 in %s: %s %+v", zone, out, r.Rule)
	}
	// 17:30 there is inside
	now = time.Date(2026, 9, 29, 17, 30, 0, 0, loc).UTC()
	if out, r = post("s3"); !strings.Contains(out, "from kb") || r.Rule.N != 1 {
		t.Fatalf("17:30 in %s: %s %+v", zone, out, r.Rule)
	}
	if a.n() != 1 || b.n() != 2 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A conversation a rule's hours sent to its member goes where the group
// routes it once they are over, whatever that member cached (MinglunZhu on
// X: a cheaper provider at night, the usual one by day). Kept by its cache,
// a Claude Code session begun at night stayed on the night's provider all
// day in a group routed Smart. Session, the user's own, keeps it.
func TestRuleHoursOverGiveTheConversationBack(t *testing.T) {
	for _, tc := range []struct {
		routing, mode, day string // where turn 2, by day, goes
		why                string
	}{
		{"", "", "from ka", "hours"},
		{"", provider.AffinitySession, "from kb", "session"},
	} {
		t.Run(tc.routing+"/"+tc.mode, func(t *testing.T) {
			now := time.Date(2026, 9, 29, 23, 0, 0, 0, time.Local) // a Tuesday night
			ruleClock = func() time.Time { return now }
			t.Cleanup(func() { ruleClock = time.Now })
			night := provider.Rule{Use: "b/big", Time: &provider.TimeWindow{From: "22:00", To: "08:00"}}
			s, a, b := ruled(t, night)
			g := provider.Group{Name: "R", Members: []string{"a/small", "b/big"}, Routing: tc.routing, Affinity: tc.mode, Rules: []provider.Rule{night}}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			post := func(body string) (string, Route) {
				t.Helper()
				code, out := postAs(t, s, "sess", body)
				if code != 200 {
					t.Fatalf("%d %s", code, out)
				}
				return out, lastRoute(s)
			}
			// turn 1, at night: the rule's member
			if out, r := post(chat("hello", nil, 0, "")); !strings.Contains(out, "from kb") || r.Rule.N != 1 {
				t.Fatalf("night: %s %+v", out, r.Rule)
			}
			// its tool round past 08:00 stays where the turn began
			now = time.Date(2026, 9, 30, 8, 1, 0, 0, time.Local)
			if out, r := post(chat("hello", nil, 1, "")); !strings.Contains(out, "from kb") || !r.Rule.Held {
				t.Fatalf("tool round: %s %+v", out, r.Rule)
			}
			// turn 2, by day, a minute later: the vendor read 2500 tokens of
			// it from its cache, and the hours are over
			now = time.Date(2026, 9, 30, 8, 2, 0, 0, time.Local)
			out, r := post(chat("hello", []string{"more"}, 0, ""))
			if !strings.Contains(out, tc.day) || r.Rule.N != 0 || r.Affinity == nil || r.Affinity.Why != tc.why {
				t.Fatalf("day: %s rule %+v affinity %+v", out, r.Rule, r.Affinity)
			}
			if tc.why == "hours" && r.Rule.Ended != 1 {
				t.Fatalf("the trace names the rule whose hours ended: %+v", r.Rule)
			}
			// and the day's turns after it keep the day's member by its cache
			out, r = post(chat("hello", []string{"more", "again"}, 0, ""))
			if !strings.Contains(out, tc.day) || r.Rule.Ended != 0 {
				t.Fatalf("turn 3: %s rule %+v affinity %+v", out, r.Rule, r.Affinity)
			}
			if a.n()+b.n() != 4 {
				t.Fatalf("a %d b %d", a.n(), b.n())
			}
		})
	}
}
