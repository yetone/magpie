package gateway

import (
	"cmp"
	"encoding/json"
	"fmt"
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
	level  string    // its choice without "none of these"; choice when ""
	levels float64   // whether the intents are levels
	work   []float64 // how much work a request at each level is, 0 to 3
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
	for i, w := range u.work {
		if _, ok := qs[fmt.Sprintf("work%d", i)]; ok {
			answers[fmt.Sprintf("work%d", i)] = map[string]any{"type": "score", "score": w, "confidence": 0.9}
		}
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
	// the route shows both: what the agent asked, what the model was sent
	if len(r.Tries) != 1 || r.Effort != "low" || r.Tries[0].Effort != "high" || !r.Tries[0].Picked {
		t.Fatalf("traced at %q: %+v", r.Effort, r.Tries)
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
	if r.Effort != "" || r.Tries[0].Effort != "" || r.Tries[0].Picked {
		t.Fatalf("no reasoning traced at %q: %+v", r.Effort, r.Tries)
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
	if err := json.Unmarshal(jevBody("jev-latest", intents, nil, before{Intent: "complex task", Effort: "xhigh"}, true, "go on"), &got); err != nil {
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
	if err := json.Unmarshal(jevBody("jev-latest", intents, nil, before{}, true, "hi"), &got); err != nil {
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

// jevAt is ruled's group with the Jev a decision API at decide serves as
// its classifier.
func jevAt(t *testing.T, decide, classifier string, rules ...provider.Rule) *Server {
	t.Helper()
	s, _, _ := ruled(t)
	if err := provider.Save(provider.Provider{ID: "jv", Name: "Jev", Key: "kj", Decide: decide}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Rules, g.Classifier, g.Effort = rules, classifier, provider.EffortAuto
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return s
}

// Vercel's AI Gateway at its TypeSafe API (…/typesafe, its docs' base) is
// asked as System One is, at /v1/systemone, with Vercel's name for Jev.
func TestJevOnVercelTypeSafe(t *testing.T) {
	// an intent of its own: what Jev said of a set of intents is kept
	j := &jevUp{choice: "crashes", sure: 0.9, score: 2}
	up := httptest.NewServer(http.StripPrefix("/typesafe", j))
	defer up.Close()
	s := jevAt(t, up.URL+"/typesafe", "jv/typesafe-ai/jev", provider.Rule{Use: "b/big", Intent: "crashes"})
	out, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"low"`))
	if c := r.Rule.Classified; !strings.Contains(out, "from kb") || c.Intent != "crashes" || c.Sure != 0.9 || r.Rule.Pick != "high" {
		t.Fatalf("%s %+v %+v", out, r.Rule, c)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.asked) != 2 || j.auth != "Bearer kj" || j.asked[1]["model"] != "typesafe-ai/jev" ||
		j.asked[0]["questions"].(map[string]any)["levels"].(map[string]any)["type"] != "noul" {
		t.Fatalf("asked %v as %q", j.asked, j.auth)
	}
}

// Levels are told what each is for, as Jev rated them: a short message
// asking for a whole program (写个纸牌游戏) was torn between two bare names
// and fell back to the turn before's level.
func TestJevLevelsSayWhatFor(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "a/small", Intent: "简单任务"}, provider.Rule{Use: "b/big", Intent: "复杂任务"})
	j.choice, j.level, j.sure, j.levels, j.work = noIntent, "复杂任务", 0.99, 0.7, []float64{0.03, 2.7}
	postOK(t, s, "s1", chat("写个纸牌游戏", nil, 0, ""))
	lv := j.turns()[0]["questions"].(map[string]any)["level"].(map[string]any)
	c := lv["criteria"].(map[string]any)
	easy, hard := fmt.Sprint(c["简单任务"]), fmt.Sprint(c["复杂任务"])
	if !strings.HasPrefix(easy, "The lowest level: little work") || !strings.Contains(easy, "some work") || strings.Contains(easy, "game") ||
		!strings.HasPrefix(hard, "The highest level: much work") || !strings.Contains(hard, "game") ||
		!strings.Contains(lv["instructions"].(string), "not how short it is") {
		t.Fatalf("level asked as %v", lv)
	}
	if q := j.asked[0]["questions"].(map[string]any); q["work0"].(map[string]any)["type"] != "score" || q["work1"] == nil {
		t.Fatalf("levels asked as %v", q)
	}
	// three levels, rated out of order: each gets the work nearest it
	w := levelsOf([]string{"hard", "easy", "medium"}, []float64{2.66, 0.01, 1.06})
	if !strings.HasPrefix(w["easy"], "The lowest level: little work") || !strings.HasPrefix(w["medium"], "Level 2 of 3, lowest first: some work") ||
		!strings.HasPrefix(w["hard"], "The highest level: much work") || !strings.Contains(w["hard"], "the most work") {
		t.Fatalf("%v", w)
	}
	// not rated: bare names, as before
	if levelsOf([]string{"a", "b"}, []float64{1}) != nil {
		t.Fatal("unrated levels described")
	}
}

// Vercel's AI Gateway at /v4/ai, where the preset once pointed, is asked
// as the AI SDK asks it — the model in a header, a noul as a boolean — and
// its probabilities stand for System One's confidence.
func TestJevOnVercel(t *testing.T) {
	var mu sync.Mutex
	var asked []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/ai/evaluation-model" || r.Header.Get("Authorization") != "Bearer kj" ||
			r.Header.Get("ai-model-id") != "typesafe-ai/jev" || r.Header.Get("ai-evaluation-model-specification-version") != "4" ||
			r.Header.Get("ai-gateway-protocol-version") == "" {
			http.Error(w, `{"error":{"message":"no"}}`, 400)
			return
		}
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		answers := map[string]any{}
		for id, x := range q["questions"].(map[string]any) {
			switch x.(map[string]any)["type"] {
			case "boolean":
				answers[id] = map[string]any{"type": "boolean", "probability": 0.1}
			case "choice":
				answers[id] = map[string]any{"type": "choice", "choice": "debugging", "probabilities": map[string]float64{"debugging": 0.87, noIntent: 0.13}}
			case "score":
				answers[id] = map[string]any{"type": "score", "score": 2.4, "probabilities": map[string]float64{"0": 0, "1": 0, "2": 0.6, "3": 0.4}}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers, "usage": map[string]int{"inputTokens": 90}})
	}))
	defer up.Close()
	s := jevAt(t, up.URL+"/v4/ai", "jv/typesafe-ai/jev", provider.Rule{Use: "b/big", Intent: "debugging"})
	out, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"low"`))
	c := r.Rule.Classified
	if !strings.Contains(out, "from kb") || c.Intent != "debugging" || c.Sure < 0.73 || c.Sure > 0.75 || r.Rule.Pick != "high" {
		t.Fatalf("%s %+v %+v", out, r.Rule, c)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0]["model"] != nil || asked[0]["questions"].(map[string]any)["levels"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("asked %v", asked)
	}
}

// Workers AI is asked at the account its token works in, found once, with
// System One's questions as the input of a run of Jev, and answers in
// Cloudflare's envelope.
func TestJevOnCloudflare(t *testing.T) {
	j := &jevUp{choice: "debugging", sure: 0.9, score: 1}
	var mu sync.Mutex
	accounts := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kj" {
			http.Error(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`, 403)
			return
		}
		switch r.URL.Path {
		case "/client/v4/accounts":
			mu.Lock()
			accounts++
			mu.Unlock()
			w.Write([]byte(`{"success":true,"result":[{"id":"acc1","name":"me"}]}`))
		case "/client/v4/accounts/acc1/ai/run":
			var q struct {
				Model string          `json:"model"`
				Input json.RawMessage `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			if q.Model != "typesafe/jev" {
				http.Error(w, `{"success":false,"errors":[{"message":"no such model"}]}`, 400)
				return
			}
			rec := httptest.NewRecorder()
			j.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(string(q.Input))))
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + rec.Body.String() + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	s := jevAt(t, up.URL+"/client/v4", "jv/typesafe/jev", provider.Rule{Use: "b/big", Intent: "debugging"})
	for i, sess := range []string{"s1", "s2"} {
		out, r := postOK(t, s, sess, chat([]string{"why does this crash?", "and this one?"}[i], nil, 0, `,"reasoning_effort":"low"`))
		if c := r.Rule.Classified; !strings.Contains(out, "from kb") || c.Intent != "debugging" || c.Sure != 0.9 || r.Rule.Pick != "medium" {
			t.Fatalf("%s: %s %+v %+v", sess, out, r.Rule, c)
		}
	}
	if accounts != 1 || j.n() != 3 {
		t.Fatalf("accounts looked up %d times, Jev asked %d", accounts, j.n())
	}
}

// Workers AI at the address Cloudflare's docs give, the account in it, is
// asked there as it is: a token for Workers AI alone, which may not list
// accounts, works.
func TestJevOnCloudflareAccount(t *testing.T) {
	j := &jevUp{choice: "crashes", sure: 0.9, score: 1}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/v4/accounts/acc7/ai/run" || r.Header.Get("Authorization") != "Bearer kj" {
			http.Error(w, `{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`, 403)
			return
		}
		var q struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		if q.Model != "typesafe/jev" {
			http.Error(w, `{"success":false,"errors":[{"message":"no such model"}]}`, 400)
			return
		}
		rec := httptest.NewRecorder()
		j.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(string(q.Input))))
		w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + rec.Body.String() + `}`))
	}))
	defer up.Close()
	s := jevAt(t, up.URL+"/client/v4/accounts/acc7/ai/run", "jv/typesafe/jev", provider.Rule{Use: "b/big", Intent: "crashes"})
	out, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"low"`))
	if c := r.Rule.Classified; !strings.Contains(out, "from kb") || c.Intent != "crashes" || c.Sure != 0.9 || r.Rule.Pick != "medium" {
		t.Fatalf("%s %+v %+v", out, r.Rule, c)
	}
}

func TestJevConfidence(t *testing.T) {
	for _, c := range []struct {
		ps   map[string]any
		want float64
	}{
		{map[string]any{"a": 0.87, "b": 0.13, "c": 0.0}, 0.805},
		{map[string]any{"0": 0.0, "1": 0.96, "2": 0.04}, 0.94},
		{map[string]any{"a": 0.5, "b": 0.5}, 0},
		{map[string]any{"a": 1.0}, 1},
	} {
		if got := confidence(c.ps); got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("%v: %v, want %v", c.ps, got, c.want)
		}
	}
}
