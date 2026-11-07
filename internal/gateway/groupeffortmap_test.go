package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// setEffortMaps gives group/r's members (as Members spells them) these
// effort maps.
func setEffortMaps(t *testing.T, maps map[string]map[string]string) {
	t.Helper()
	g, _, _ := provider.FindGroup("group/r")
	g.EffortMaps = nil
	for id, to := range maps {
		g.SetMemberEffortMap(id, to)
	}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
}

// A member with an effort map (LuckymanJC on Discord: one vendor's high
// is another's xhigh) is sent the level its map names for the one asked,
// in each API's words, and a level the map doesn't name as it was asked.
func TestMemberEffortMap(t *testing.T) {
	s, a, _ := ruled(t)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	setEffortMaps(t, map[string]map[string]string{"a/small": {"high": "xhigh", "low": "medium", "none": "low"}})
	for _, c := range []struct{ asked, want, from string }{
		{"high", "xhigh", "high"},
		{"low", "medium", "low"},
		{"medium", "medium", ""}, // not in the map: as asked
		{"xhigh", "xhigh", ""},
		{"none", "low", "none"},
	} {
		out, r := postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"`+c.asked+`"`))
		if sent := sentBody(t, a); !strings.Contains(out, "from ka") || sent["reasoning_effort"] != c.want {
			t.Errorf("chat asked %s: sent %v", c.asked, sent["reasoning_effort"])
		}
		if len(r.Tries) != 1 || r.Tries[0].Effort != c.want || r.Tries[0].MappedFrom != c.from || r.Tries[0].Fixed != "" {
			t.Errorf("chat asked %s: traced %+v", c.asked, r.Tries)
		}
	}
	// a request asking for no reasoning (a session title) is left without
	postOK(t, s, "", chat("hi", nil, 0, ""))
	if sent := sentBody(t, a); sent["reasoning_effort"] != nil {
		t.Errorf("no ask: sent %v", sent["reasoning_effort"])
	}
	// Responses
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/r","input":"hi","reasoning":{"effort":"high","summary":"auto"}}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if sent := sentBody(t, a); sent["reasoning_effort"] != "xhigh" {
		t.Errorf("responses high: sent %v", sent["reasoning_effort"])
	}
	// Anthropic: its effort, its budget, and thinking turned off ("none")
	anthropic := func(body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return sentBody(t, a)
	}
	if sent := anthropic(`{"model":"group/r","max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`); sent["reasoning_effort"] != "xhigh" {
		t.Errorf("anthropic effort high: sent %v", sent["reasoning_effort"])
	}
	if sent := anthropic(`{"model":"group/r","max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[{"role":"user","content":"hi"}]}`); sent["reasoning_effort"] != "medium" {
		t.Errorf("anthropic budget 4096 (low): sent %v", sent["reasoning_effort"])
	}
	if sent := anthropic(`{"model":"group/r","max_tokens":64000,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`); sent["reasoning_effort"] != "low" {
		t.Errorf("anthropic thinking off: sent %v", sent["reasoning_effort"])
	}
	// a model's suffix is asked through the map too
	postOK(t, s, "", strings.Replace(chat("hi", nil, 0, `,"reasoning_effort":"low"`), `"group/r"`, `"group/r:high"`, 1))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "xhigh" {
		t.Errorf("group/r:high: sent %v", sent["reasoning_effort"])
	}
	// a level the model hasn't is sent at its nearest
	setEffortMaps(t, map[string]map[string]string{"a/small": {"high": "max"}})
	postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"high"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "xhigh" {
		t.Errorf("high to max on a model up to xhigh: sent %v", sent["reasoning_effort"])
	}
}

// Without a map a member is sent the level asked, as before; each
// member's map is its own, the one fallen back to included.
func TestMemberEffortMapPerMember(t *testing.T) {
	s, a, b := ruled(t)
	if err := provider.SetModelEfforts("b/big", []string{"low", "medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"high"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "high" {
		t.Fatalf("no map: sent %v", sent["reasoning_effort"])
	}
	setEffortMaps(t, map[string]map[string]string{"b/big": {"high": "xhigh"}})
	postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"high"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "high" {
		t.Fatalf("a without a map: sent %v", sent["reasoning_effort"])
	}
	a.mu.Lock()
	a.fail = 500
	a.mu.Unlock()
	out, r := postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"high"`))
	if sent := sentBody(t, b); !strings.Contains(out, "from kb") || sent["reasoning_effort"] != "xhigh" {
		t.Fatalf("fallback to b: sent %v (%s)", sent, out)
	}
	last := r.Tries[len(r.Tries)-1]
	if last.Model != "big" || last.Effort != "xhigh" || last.MappedFrom != "high" || r.Tries[0].MappedFrom != "" {
		t.Fatalf("fallback traced %+v", r.Tries)
	}
}

// The map is asked after the group's classifier picks the turn's level
// (effort=auto): the pick is what the member is asked for.
func TestMemberEffortMapOverAuto(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	setEffortMaps(t, map[string]map[string]string{"a/small": {"high": "xhigh"}})
	j.score = 2.4 // high
	_, r := postOK(t, s, "s1", chat("redesign the scheduler", nil, 0, `,"reasoning_effort":"low"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "xhigh" {
		t.Fatalf("sent %v", sent["reasoning_effort"])
	}
	if r.Rule == nil || r.Rule.Pick != "high" || r.Tries[0].MappedFrom != "high" || r.Tries[0].Effort != "xhigh" {
		t.Fatalf("traced %+v %+v", r.Rule, r.Tries)
	}
}

// A member fixed at an effort keeps it: a map on it is not kept.
func TestMemberEffortMapNotOnFixed(t *testing.T) {
	s, a, _ := ruled(t)
	setMembers(t, []string{"a/small:low", "b/big"})
	setEffortMaps(t, map[string]map[string]string{"a/small:low": {"high": "xhigh"}})
	if g, _, _ := provider.FindGroup("group/r"); g.EffortMaps != nil {
		t.Fatalf("kept %v", g.EffortMaps)
	}
	postOK(t, s, "", chat("hi", nil, 0, `,"reasoning_effort":"high"`))
	if sent := sentBody(t, a); sent["reasoning_effort"] != "low" {
		t.Fatalf("sent %v", sent["reasoning_effort"])
	}
}

// /v1/models offers, for a member with a map, the levels its map is asked
// for besides its model's own.
func TestMemberEffortMapLevelsListed(t *testing.T) {
	s, _, _ := ruled(t)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("b/big", []string{"medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	setEffortMaps(t, map[string]map[string]string{"a/small": {"xhigh": "high"}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	got := levelsListed(t, rec.Body.Bytes(), "group/r")
	if strings.Join(got, ",") != "medium,high,xhigh" {
		t.Fatalf("levels %v", got)
	}
}

// levelsListed are the reasoning levels /v1/models' body lists for id.
func levelsListed(t *testing.T, body []byte, id string) []string {
	t.Helper()
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	for _, m := range list.Data {
		if m.ID == id {
			var out []string
			for _, l := range m.Levels {
				out = append(out, l.Effort)
			}
			return out
		}
	}
	t.Fatalf("%s not listed: %s", id, body)
	return nil
}
