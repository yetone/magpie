package gateway

import (
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// jevUp is a System One API: it answers each question it is asked with
// what choice and score say, and keeps what it was asked.
type jevUp struct {
	mu     sync.Mutex
	choice string
	level  string  // its choice without "none of these"; choice when ""
	levels float64 // whether the intents are levels
	sure   float64
	score  float64
	asked  []map[string]any
	auth   string
}

func (u *jevUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked, u.auth = append(u.asked, q), r.Header.Get("Authorization")
	if r.URL.Path != "/v1/systemone" {
		http.NotFound(w, r)
		return
	}
	answers := map[string]any{}
	qs, _ := q["questions"].(map[string]any)
	if _, ok := qs["intent"]; ok {
		answers["intent"] = map[string]any{"type": "choice", "choice": u.choice, "confidence": u.sure}
	}
	if _, ok := qs["level"]; ok {
		answers["level"] = map[string]any{"type": "choice", "choice": cmp.Or(u.level, u.choice), "confidence": u.sure}
	}
	if _, ok := qs["levels"]; ok {
		answers["levels"] = map[string]any{"type": "noul", "noul": u.levels}
	}
	if _, ok := qs["effort"]; ok {
		answers["effort"] = map[string]any{"type": "score", "score": u.score, "confidence": 0.8}
	}
	json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 120, "output_tokens": 0}})
}

// turns is what it was asked about messages, not about the intents.
func (u *jevUp) turns() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []map[string]any
	for _, q := range u.asked {
		if _, ok := q["state"].(map[string]any)["message"]; ok {
			out = append(out, q)
		}
	}
	return out
}

func (u *jevUp) n() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.asked)
}

// jevved is ruled's group with Jev as its classifier, the rules given and
// the effort.
func jevved(t *testing.T, effort string, rules ...provider.Rule) (*Server, *ruleUp, *ruleUp, *jevUp) {
	t.Helper()
	s, a, b := ruled(t)
	j := &jevUp{choice: noIntent, sure: 0.9, score: 0}
	srv := httptest.NewServer(j)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "ts", Name: "TypeSafe", Key: "kts", Decide: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Rules, g.Classifier, g.Effort = rules, "ts/jev-latest", effort
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return s, a, b, j
}

// Jev picks the intent in one call: the rule goes by its choice, and a
// choice it isn't sure of is none.
func TestJevPicksTheIntent(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.7
	out, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Classified.Intent != "debugging" || r.Rule.Classified.Sure != 0.7 {
		t.Fatalf("%s %+v", out, r.Rule.Classified)
	}
	asked := j.turns()[0]
	if j.auth != "Bearer kts" || asked["model"] != "jev-latest" {
		t.Fatalf("asked %v with %q", asked, j.auth)
	}
	qs := asked["questions"].(map[string]any)
	crit := qs["intent"].(map[string]any)["criteria"].(map[string]any)
	if _, ok := crit["debugging"]; !ok || qs["effort"] != nil {
		t.Fatalf("questions %v", qs)
	}
	if st := asked["state"].(map[string]any); st["message"] != "why does this crash?" {
		t.Fatalf("state %v", st)
	}
	// not sure enough: no rule
	j.choice, j.sure = "debugging", 0.2
	_, r = postOK(t, s, "s2", chat("hmm, what now?", nil, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" || r.Rule.Classified.Error != "" {
		t.Fatalf("unsure: %+v", r.Rule.Classified)
	}
	// Jev's call is magpie's own; it holds no conversation itself
	if code, out := postAs(t, s, "s3", `{"model":"ts/jev-latest","messages":[{"role":"user","content":"hi"}]}`); code != 400 || !strings.Contains(out, "only decides") {
		t.Fatalf("chat to Jev: %d %s", code, out)
	}
}

// Intents that are levels (how hard a request is) leave no message out:
// Jev's answer without "none of these" counts for them, and whether they
// are is asked once for the set.
func TestJevLevels(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "simple task"}, provider.Rule{Use: "a/small", Intent: "complex task"})
	j.choice, j.level, j.sure, j.levels = noIntent, "simple task", 0.9, 0.7
	_, r := postOK(t, s, "s1", chat("who are you?", nil, 0, ""))
	if r.Rule.N != 1 || r.Rule.Classified.Intent != "simple task" {
		t.Fatalf("levels: %+v", r.Rule.Classified)
	}
	_, r = postOK(t, s, "s2", chat("hello", nil, 0, ""))
	if r.Rule.N != 1 || j.n() != 3 { // the intents asked about once, two turns
		t.Fatalf("again: %+v, %d calls", r.Rule.Classified, j.n())
	}
	// topics: "none of these" stands
	s, _, _, j = jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.level, j.sure, j.levels = noIntent, "debugging", 0.9, 0.1
	_, r = postOK(t, s, "s3", chat("write me a poem", nil, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" {
		t.Fatalf("topics: %+v", r.Rule.Classified)
	}
}

// A group whose effort is auto has Jev pick each turn's reasoning, where
// the agent asked for some; the turn keeps it, and a request that asked
// for none stays without.
func TestJevPicksTheEffort(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	j.score = 2.4 // high
	_, r := postOK(t, s, "s1", chat("redesign the scheduler", nil, 0, `,"reasoning_effort":"low"`))
	if r.Rule == nil || r.Rule.Pick != "high" || r.Rule.Classified.Effort != "high" || r.Rule.Classified.Intents != nil {
		t.Fatalf("%+v", r.Rule)
	}
	if !strings.Contains(a.last, `"reasoning_effort":"high"`) {
		t.Fatalf("sent %s", a.last)
	}
	if qs := j.turns()[0]["questions"].(map[string]any); qs["intent"] != nil || qs["effort"] == nil {
		t.Fatalf("questions %v", qs)
	}
	// the turn's tool rounds keep it, without asking again
	_, r = postOK(t, s, "s1", chat("redesign the scheduler", nil, 2, `,"reasoning_effort":"low"`))
	if r.Rule.Pick != "high" || j.n() != 1 || !strings.Contains(a.last, `"reasoning_effort":"high"`) {
		t.Fatalf("within: %+v %d %s", r.Rule, j.n(), a.last)
	}
	// a request without reasoning (a title) isn't asked about, nor given any
	_, r = postOK(t, s, "s2", chat("title this", nil, 0, ""))
	if r.Rule.Pick != "" || r.Rule.Classified != nil || j.n() != 1 || strings.Contains(a.last, "reasoning_effort") {
		t.Fatalf("no reasoning: %+v %s", r.Rule, a.last)
	}
}

// withEffort changes the effort in each API's own words, only where the
// request asked for reasoning.
func TestWithEffort(t *testing.T) {
	for _, c := range []struct {
		proto      provider.Protocol
		in, effort string
		want       []string
	}{
		{provider.Chat, `{"reasoning_effort":"low"}`, "high", []string{`"reasoning_effort":"high"`}},
		{provider.Chat, `{"model":"m"}`, "high", []string{`{"model":"m"}`}},
		{provider.Responses, `{"reasoning":{"effort":"medium","summary":"auto"}}`, "xhigh", []string{`"effort":"xhigh"`, `"summary":"auto"`}},
		{provider.Responses, `{"reasoning":{"effort":"none"}}`, "high", []string{`"effort":"none"`}},
		{provider.Anthropic, `{"max_tokens":32000,"thinking":{"type":"adaptive"}}`, "low", []string{`"output_config":{"effort":"low"}`}},
		{provider.Anthropic, `{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":4096}}`, "high", []string{`"budget_tokens":24000`}},
		{provider.Anthropic, `{"max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":4096}}`, "xhigh", []string{`"budget_tokens":7999`}},
		{provider.Anthropic, `{"max_tokens":8000,"thinking":{"type":"disabled"}}`, "high", []string{`"type":"disabled"`}},
	} {
		got := string(withEffort(c.proto, []byte(c.in), c.effort))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s %s at %s: %s, want %s", c.proto, c.in, c.effort, got, w)
			}
		}
	}
}

// Jev is told what it said of the conversation's turn before, so that
// "go on" keeps that turn's kind and reasoning.
func TestJevIsToldTheTurnBefore(t *testing.T) {
	var got struct {
		State     map[string]string `json:"state"`
		Questions map[string]struct {
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	intents := []string{"simple task", "complex task"}
	if err := json.Unmarshal(jevBody("jev-latest", intents, before{Intent: "complex task", Effort: "xhigh"}, true, "go on"), &got); err != nil {
		t.Fatal(err)
	}
	if got.State["previous_message_kind"] != "complex task" || got.State["previous_message_reasoning"] != "xhigh" {
		t.Errorf("state = %v", got.State)
	}
	for _, q := range []string{"intent", "effort"} {
		if !strings.Contains(got.Questions[q].Instructions, "carries on") {
			t.Errorf("%s: %q", q, got.Questions[q].Instructions)
		}
	}
	// a first turn has nothing of the kind
	got.State, got.Questions = nil, nil
	if err := json.Unmarshal(jevBody("jev-latest", intents, before{}, true, "hi"), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.State) != 1 || strings.Contains(got.Questions["intent"].Instructions, "carries on") {
		t.Errorf("first turn: %v %q", got.State, got.Questions["intent"].Instructions)
	}
}

// Levels have every message at one of them however torn Jev is: a torn
// one stays at the turn before's level, or with none before takes the
// likelier.
func TestJevTornLevel(t *testing.T) {
	in := []string{"简单任务", "复杂任务"}
	b := []byte(`{"model":"jev-1.13.0","answers":{` +
		`"intent":{"type":"choice","choice":"复杂任务","confidence":0.43},` +
		`"level":{"type":"choice","choice":"复杂任务","confidence":0.09}}}`)
	for _, c := range []struct {
		prev before
		want string
	}{
		{before{}, "复杂任务"},
		{before{Intent: "复杂任务"}, "复杂任务"},
		{before{Intent: "简单任务"}, "简单任务"},
		{before{Intent: "gone"}, "复杂任务"},
	} {
		if v, err := readJev(b, in, true, c.prev); err != nil || v.Intent != c.want || v.Sure != 0.09 {
			t.Fatalf("after %q: %+v %v, want %s", c.prev.Intent, v, err, c.want)
		}
	}
	// a sure level is taken over the turn before's
	sure := []byte(`{"answers":{"level":{"type":"choice","choice":"简单任务","confidence":0.86}}}`)
	if v, _ := readJev(sure, in, true, before{Intent: "复杂任务"}); v.Intent != "简单任务" {
		t.Fatalf("sure: %+v", v)
	}
	// topics still need Jev sure, and may be none
	if v, _ := readJev(b, in, false, before{Intent: "复杂任务"}); v.Intent != "复杂任务" {
		t.Fatalf("topic sure enough: %+v", v)
	}
	unsure := []byte(`{"answers":{"intent":{"type":"choice","choice":"复杂任务","confidence":0.2}}}`)
	if v, _ := readJev(unsure, in, false, before{Intent: "复杂任务"}); v.Intent != "" {
		t.Fatalf("topic unsure: %+v", v)
	}
}
