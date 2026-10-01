package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A rule's hours are looked at as a turn begins, on the clock then: a turn
// begun inside them goes to its member and stays there for its tool
// rounds after they end; a turn begun outside them isn't the rule's (where
// the same conversation goes then is the group's Stays to say).
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
	// turn 2 begins after them: no rule
	if _, r = post(chat("hello", []string{"more"}, 0, ""), "sess"); r.Rule.N != 0 || r.Rule.Held {
		t.Fatalf("turn 2: %+v", r.Rule)
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
	if a.n() != 4 || b.n() != 3 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}
