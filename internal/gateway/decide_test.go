package gateway

import (
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
	if _, ok := qs["effort"]; ok {
		answers["effort"] = map[string]any{"type": "score", "score": u.score, "confidence": 0.8}
	}
	json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 120, "output_tokens": 0}})
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
	if j.auth != "Bearer kts" || j.asked[0]["model"] != "jev-latest" {
		t.Fatalf("asked %v with %q", j.asked[0], j.auth)
	}
	qs := j.asked[0]["questions"].(map[string]any)
	crit := qs["intent"].(map[string]any)["criteria"].(map[string]any)
	if _, ok := crit["debugging"]; !ok || qs["effort"] != nil {
		t.Fatalf("questions %v", qs)
	}
	if st := j.asked[0]["state"].(map[string]any); st["message"] != "why does this crash?" {
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
	if qs := j.asked[0]["questions"].(map[string]any); qs["intent"] != nil || qs["effort"] == nil {
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
