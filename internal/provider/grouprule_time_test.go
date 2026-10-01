package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// at is a local time on the week of Monday 2026-09-28.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 9, 27+day, hour, min, 0, 0, time.Local) // day 0 is Sunday
}

// A rule's hours hold on the machine's clock, past midnight when they end
// before they begin, and on the day they begin on.
func TestRuleTimeWindow(t *testing.T) {
	day := TimeWindow{From: "09:00", To: "18:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}}
	night := TimeWindow{From: "22:00", To: "08:00", Days: []string{"fri"}}
	whole := TimeWindow{From: "00:00", To: "00:00", Days: []string{"sat", "sun"}}
	for _, c := range []struct {
		w    TimeWindow
		at   time.Time
		want bool
	}{
		{day, at(1, 9, 0), true},    // Monday, as it opens
		{day, at(1, 17, 59), true},  // Monday, before it ends
		{day, at(1, 18, 0), false},  // as it ends
		{day, at(1, 8, 59), false},  // before
		{day, at(6, 12, 0), false},  // Saturday
		{night, at(5, 23, 0), true}, // Friday night
		{night, at(6, 3, 0), true},  // Saturday's early hours: Friday's window
		{night, at(6, 8, 0), false}, // as it ends
		{night, at(5, 3, 0), false}, // Friday's early hours: Thursday's window
		{night, at(6, 23, 0), false},
		{whole, at(0, 0, 0), true},
		{whole, at(6, 23, 59), true},
		{whole, at(1, 12, 0), false},
		{TimeWindow{From: "22:00", To: "08:00"}, at(3, 7, 0), true},
		{TimeWindow{From: "22:00", To: "08:00"}, at(3, 12, 0), false},
		{day, time.Time{}, false}, // no time is in no hours
	} {
		if got := c.w.Holds(c.at); got != c.want {
			t.Errorf("%s at %s: %v, want %v", c.w.Text(), c.at.Format("Mon 15:04"), got, c.want)
		}
	}
}

func TestRuleTimeCleanAndConditions(t *testing.T) {
	rules, err := cleanRules([]Rule{
		{Use: "a/x", Time: &TimeWindow{From: "9:00", To: "18:00", Days: []string{"Friday", "mon", "tue", "wed", "thu", "mon"}}},
		{Use: "a/x", Time: &TimeWindow{From: "22:00", To: "08:00"}},
		{Use: "a/x", Time: &TimeWindow{From: "00:00", To: "00:00", Days: []string{"sun", "sat"}}},
		{Use: "a/x", Time: &TimeWindow{From: "08:00", To: "12:00", Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}}},
		{Use: "a/x", Tokens: 100, Time: &TimeWindow{From: "08:00", To: "12:00", Days: []string{"mon", "wed", "thu", "fri"}}},
	}, []string{"a/x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"time 09:00–18:00 Mon–Fri", "time 22:00–08:00", "time all day Sat,Sun", "time 08:00–12:00", "tokens ≥ 100 · time 08:00–12:00 Mon,Wed–Fri"}
	for i, r := range rules {
		if got := strings.Join(r.Conditions(), " · "); got != want[i] {
			t.Errorf("rule %d: %q, want %q", i+1, got, want[i])
		}
	}
	if d := rules[0].Time.Days; strings.Join(d, ",") != "mon,tue,wed,thu,fri" || rules[0].Time.From != "09:00" {
		t.Errorf("cleaned: %+v", rules[0].Time)
	}
	if rules[3].Time.Days != nil {
		t.Errorf("every day is no days: %+v", rules[3].Time)
	}
	for _, bad := range []TimeWindow{
		{From: "25:00", To: "08:00"},
		{From: "09:00", To: "8"},
		{From: "", To: "08:00"},
		{From: "09:60", To: "10:00"},
		{From: "09:00", To: "18:00", Days: []string{"funday"}},
		{From: "10:00", To: "10:00"}, // the whole day, every day: no condition
	} {
		if _, err := cleanRules([]Rule{{Use: "a/x", Time: &bad}}, []string{"a/x"}); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

// A rule with hours matches a request inside them only; one without a
// time (an older magpie's) is in none.
func TestRuleTimeMatches(t *testing.T) {
	r := Rule{Use: "a/x", Time: &TimeWindow{From: "09:00", To: "18:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}}}
	if !r.Matches(RuleRequest{At: at(2, 10, 0)}) {
		t.Error("inside the hours: no match")
	}
	if r.Matches(RuleRequest{At: at(2, 19, 0)}) || r.Matches(RuleRequest{At: at(0, 10, 0)}) || r.Matches(RuleRequest{}) {
		t.Error("outside the hours: a match")
	}
	if i := MatchRule([]Rule{r, {Use: "a/y", Tokens: 10}}, RuleRequest{Tokens: 20, At: at(2, 20, 0)}); i != 1 {
		t.Errorf("after hours: rule %d", i)
	}
}

// Stored with omitempty: a rule without hours is written as before, and a
// window round-trips.
func TestRuleTimeJSON(t *testing.T) {
	b, _ := json.Marshal(Rule{Use: "a/x", Tokens: 5})
	if strings.Contains(string(b), "time") {
		t.Errorf("no hours written: %s", b)
	}
	b, _ = json.Marshal(Rule{Use: "a/x", Time: &TimeWindow{From: "22:00", To: "08:00"}})
	if string(b) != `{"use":"a/x","time":{"from":"22:00","to":"08:00"}}` {
		t.Errorf("%s", b)
	}
	var r Rule
	if err := json.Unmarshal(b, &r); err != nil || r.Time == nil || r.Time.From != "22:00" {
		t.Errorf("%v %+v", err, r)
	}
}

// Typed: time=… days=… — read back the same.
func TestParseRuleTime(t *testing.T) {
	g := Group{ID: "g", Members: []string{"a/x", "b/y"}}
	for _, c := range []struct{ line, want, cond string }{
		{"use=b/y time=09:00-18:00 days=mon-fri", "use=b/y time=09:00-18:00 days=mon-fri", "time 09:00–18:00 Mon–Fri"},
		{"use=b/y days=sat,sun", "use=b/y days=sat,sun", "time all day Sat,Sun"},
		{"use=b/y days=fri-mon time=22:00–08:00", "use=b/y time=22:00-08:00 days=mon,fri-sun", "time 22:00–08:00 Mon,Fri–Sun"},
		{"use=b/y time=22:00-08:00", "use=b/y time=22:00-08:00", "time 22:00–08:00"},
	} {
		r, _, _, err := ParseRule(g, RuleWords(c.line))
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		rs, err := cleanRules([]Rule{r}, g.Members)
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		if got := rs[0].Line(); got != c.want {
			t.Errorf("%s: line %q, want %q", c.line, got, c.want)
		}
		if got := strings.Join(rs[0].Conditions(), " · "); got != c.cond {
			t.Errorf("%s: %q, want %q", c.line, got, c.cond)
		}
		again, _, _, err := ParseRule(g, RuleWords(rs[0].Line()))
		if err != nil || again.Time == nil || again.Time.From != rs[0].Time.From || again.Time.To != rs[0].Time.To {
			t.Errorf("%s: read back %v %+v", c.line, err, again.Time)
		}
	}
	for _, bad := range []string{"use=b/y time=9-18", "use=b/y time=09:00", "use=b/y days=someday", "use=b/y days="} {
		if _, _, _, err := ParseRule(g, RuleWords(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
