package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// setMembers gives group/r (a/small and b/big, see ruled) these members
// and rules.
func setMembers(t *testing.T, members []string, rules ...provider.Rule) {
	t.Helper()
	g, _, _ := provider.FindGroup("group/r")
	g.Members, g.Rules = members, rules
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
}

func sentBody(t *testing.T, u *ruleUp) map[string]any {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	var v map[string]any
	if err := json.Unmarshal([]byte(u.last), &v); err != nil {
		t.Fatalf("%v: %s", err, u.last)
	}
	return v
}

// A member fixed at an effort is sent it, at its model's nearest level,
// whatever the agent asked — none included — and the vendor is asked for
// the model's own id. One without reasons as the agent asked.
func TestFixedMemberEffort(t *testing.T) {
	s, a, b := ruled(t)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		fixed, asked, want string
	}{
		{"low", "high", "low"},
		{"low", "", "low"},       // asked for no reasoning: given it anyway
		{"xhigh", "low", "high"}, // the model's nearest
		{"max", "", "high"},
		{"minimal", "high", "low"},
		{"medium", "medium", "medium"},
	} {
		setMembers(t, []string{"a/small:" + c.fixed, "b/big"})
		extra := ""
		if c.asked != "" {
			extra = `,"reasoning_effort":"` + c.asked + `"`
		}
		out, r := postOK(t, s, "", chat("hi", nil, 0, extra))
		sent := sentBody(t, a)
		if !strings.Contains(out, "from ka") || sent["model"] != "small" || sent["reasoning_effort"] != c.want {
			t.Fatalf("%s asked %q: sent %v (%s)", c.fixed, c.asked, sent, out)
		}
		if len(r.Tries) != 1 || r.Tries[0].Fixed != c.fixed || r.Tries[0].Effort != c.want || r.Tries[0].Picked || r.Tries[0].Model != "small" {
			t.Fatalf("%s asked %q: traced %+v", c.fixed, c.asked, r.Tries)
		}
		if r.Order[0].Fixed != c.fixed || r.Group == nil || r.Group.Members[0] != "a/small:"+c.fixed {
			t.Fatalf("%s: order %+v group %+v", c.fixed, r.Order[0], r.Group)
		}
	}
	// the member without one, once the fixed one fails, reasons as asked
	setMembers(t, []string{"a/small:low", "b/big"})
	a.mu.Lock()
	a.fail = 500
	a.mu.Unlock()
	out, r := postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"medium"`))
	if sent := sentBody(t, b); !strings.Contains(out, "from kb") || sent["model"] != "big" || sent["reasoning_effort"] != "medium" {
		t.Fatalf("fallback sent %v (%s)", sent, out)
	}
	if len(r.Tries) != 2 || r.Tries[0].Fixed != "low" || r.Tries[1].Fixed != "" || r.Tries[1].Effort != "medium" {
		t.Fatalf("fallback traced %+v", r.Tries)
	}
}

// Through the Responses API too: the model's own id, at the fixed effort.
func TestFixedMemberEffortResponses(t *testing.T) {
	s, a, _ := ruled(t)
	setMembers(t, []string{"a/small:high", "b/big"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/r","input":"hi","reasoning":{"effort":"low","summary":"auto"}}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if sent := sentBody(t, a); sent["model"] != "small" || sent["reasoning_effort"] != "high" {
		t.Fatalf("sent %v", sent)
	}
}

// A fixed member's effort wins over the one the group's classifier picks
// for the turn (effort=auto).
func TestFixedMemberEffortOverAuto(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	setMembers(t, []string{"a/small:minimal", "b/big"})
	j.score = 2.4 // high
	_, r := postOK(t, s, "s1", chat("redesign the scheduler", nil, 0, `,"reasoning_effort":"low"`))
	if sent := sentBody(t, a); sent["model"] != "small" || sent["reasoning_effort"] != "minimal" {
		t.Fatalf("sent %v", sent)
	}
	if r.Rule == nil || r.Rule.Pick != "high" || r.Tries[0].Fixed != "minimal" || r.Tries[0].Effort != "minimal" || r.Tries[0].Picked {
		t.Fatalf("traced %+v %+v", r.Rule, r.Tries)
	}
}

// The same model at two efforts is two members: a rule sends a turn to
// one by its effort, and each is sent its own.
func TestSameModelTwoEfforts(t *testing.T) {
	s, a, _ := ruled(t)
	setMembers(t, []string{"a/small:low", "a/small:high", "b/big"}, provider.Rule{Use: "a/small:high", Tokens: 20000})
	_, r := postOK(t, s, "sess", chat("hello", nil, 0, `,"reasoning_effort":"medium"`))
	if sent := sentBody(t, a); sent["model"] != "small" || sent["reasoning_effort"] != "low" || r.Tries[0].Fixed != "low" {
		t.Fatalf("short turn sent %v, traced %+v", sent, r.Tries)
	}
	// both members are weighed, each at its own effort, over a's one key
	var seats []string
	for _, w := range r.Order {
		seats = append(seats, w.ID+"/"+w.Model+":"+w.Fixed)
	}
	if !slices.Contains(seats, "a/small:low") || !slices.Contains(seats, "a/small:high") {
		t.Fatalf("order %v", seats)
	}
	_, r = postOK(t, s, "sess", chat("hello", []string{long(25000)}, 0, `,"reasoning_effort":"medium"`))
	if sent := sentBody(t, a); sent["model"] != "small" || sent["reasoning_effort"] != "high" {
		t.Fatalf("long turn sent %v", sent)
	}
	if r.Rule == nil || r.Rule.N != 1 || r.Rule.Use != "a/small:high" || r.Tries[0].Fixed != "high" || r.Order[0].Fixed != "high" {
		t.Fatalf("long turn traced %+v %+v", r.Rule, r.Tries)
	}
	// its tool rounds stay on the member at high, not the one at low
	_, r = postOK(t, s, "sess", chat("hello", []string{long(25000)}, 1, `,"reasoning_effort":"medium"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "high" || r.Tries[0].Fixed != "high" {
		t.Fatalf("tool round sent %v, traced %+v", sent, r.Tries)
	}
}

// /v1/models lists a group's levels from its members without an effort of
// their own; one all fixed lists the efforts its members are fixed at.
func TestGroupLevelsListed(t *testing.T) {
	s, _, _ := ruled(t)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("b/big", []string{"medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	levels := func() []string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
		var list struct {
			Data []struct {
				ID     string `json:"id"`
				Levels []struct {
					Effort string `json:"effort"`
				} `json:"supported_reasoning_levels"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
		for _, m := range list.Data {
			if m.ID == "group/r" {
				var out []string
				for _, l := range m.Levels {
					out = append(out, l.Effort)
				}
				return out
			}
		}
		t.Fatalf("group/r not listed: %s", rec.Body)
		return nil
	}
	for _, c := range []struct {
		members, want []string
	}{
		{[]string{"a/small", "b/big"}, []string{"medium", "high"}},
		{[]string{"a/small:low", "b/big"}, []string{"medium", "high", "xhigh"}},
		{[]string{"a/small:low", "a/small:high"}, []string{"low", "high"}},
	} {
		setMembers(t, c.members)
		if got := levels(); !slices.Equal(got, c.want) {
			t.Errorf("%v: levels %v, want %v", c.members, got, c.want)
		}
	}
}

// withFixedEffort asks for the effort in each API's own words, whether or
// not the request asked for reasoning.
func TestWithFixedEffort(t *testing.T) {
	for _, c := range []struct {
		proto      provider.Protocol
		in, effort string
		want       string
	}{
		{provider.Chat, `{"model":"m"}`, "low", `"reasoning_effort":"low"`},
		{provider.Chat, `{"model":"m","reasoning_effort":"high"}`, "none", `"reasoning_effort":"none"`},
		{provider.Responses, `{"model":"m"}`, "high", `"reasoning":{"effort":"high"}`},
		{provider.Responses, `{"model":"m","reasoning":{"effort":"low","summary":"auto"}}`, "xhigh", `"effort":"xhigh"`},
		{provider.Responses, `{"model":"m","reasoning":{"effort":"low","summary":"auto"}}`, "xhigh", `"summary":"auto"`},
		{provider.Anthropic, `{"model":"m","max_tokens":32000}`, "none", `"thinking":{"type":"disabled"}`},
		{provider.Anthropic, `{"model":"m","max_tokens":32000}`, "high", `"type":"enabled"`},
		{provider.Anthropic, `{"model":"m","max_tokens":32000}`, "high", `"budget_tokens":`},
	} {
		got := string(withFixedEffort(c.proto, []byte(c.in), c.effort))
		if !strings.Contains(got, c.want) {
			t.Errorf("%v %s at %s: %s, want %s", c.proto, c.in, c.effort, got, c.want)
		}
	}
	// no room left to think in: left as it is
	if got := string(withFixedEffort(provider.Anthropic, []byte(`{"model":"m","max_tokens":500}`), "high")); strings.Contains(got, "thinking") {
		t.Errorf("small max_tokens: %s", got)
	}
	if got := string(withFixedEffort(provider.Chat, []byte(`{"model":"m"}`), "")); got != `{"model":"m"}` {
		t.Errorf("no effort: %s", got)
	}
	if fitLevel("xhigh", nil) != "high" || fitLevel("xhigh", []string{"low", "xhigh"}) != "xhigh" || fitLevel("max", []string{"low", "medium", "high"}) != "high" {
		t.Error("fitLevel")
	}
}
