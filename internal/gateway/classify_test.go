package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// clsUp is a Chat vendor that classifies: it answers what answer says,
// after wait, and keeps what it was asked.
type clsUp struct {
	mu     sync.Mutex
	answer string
	fail   int
	wait   time.Duration
	asked  []string
	agents []string
}

func (u *clsUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.asked = append(u.asked, string(b))
	answer, fail, wait := u.answer, u.fail, u.wait
	u.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if fail != 0 {
		w.WriteHeader(fail)
		io.WriteString(w, `{"error":{"message":"classifier down"}}`)
		return
	}
	io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":`+quote(answer)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":1}}`)
}

func (u *clsUp) n() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.asked)
}

func (u *clsUp) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.asked[len(u.asked)-1]
}

func (u *clsUp) set(answer string, fail int) {
	u.mu.Lock()
	u.answer, u.fail = answer, fail
	u.mu.Unlock()
}

// intented is ruled's group with a classifier, c/cls, and the rules given.
func intented(t *testing.T, rules ...provider.Rule) (*Server, *ruleUp, *ruleUp, *clsUp) {
	t.Helper()
	s, a, b := ruled(t)
	c := &clsUp{answer: "0"}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"cls"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("c", srv.URL+"/v1", []catalog.Model{{ID: "cls", Context: 32000}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Rules, g.Classifier = rules, "c/cls"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return s, a, b, c
}

func postOK(t *testing.T, s *Server, session, body string) (string, Route) {
	t.Helper()
	code, out := postAs(t, s, session, body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	return out, lastRoute(s)
}

// A rule with an intent: the classifier is asked as the turn begins, with
// the user's words only, and the turn goes where its answer says; the
// turn's tool rounds don't ask again.
func TestIntentRoutesATurn(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	c.set("1", 0)
	msg := "add a unit test for the parser <system-reminder>secret agent context</system-reminder>"
	out, r := postOK(t, s, "s1", chat(msg, nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule == nil || r.Rule.N != 1 || r.Rule.Use != "b/big" {
		t.Fatalf("turn 1: %s %+v", out, r.Rule)
	}
	cl := r.Rule.Classified
	if cl == nil || cl.By != "c/cls" || cl.Intent != "writing or fixing tests" || cl.Error != "" || cl.Cached || len(cl.Intents) != 1 {
		t.Fatalf("classified %+v", cl)
	}
	if len(r.Rule.When) != 1 || r.Rule.When[0] != `intent "writing or fixing tests"` {
		t.Fatalf("when %v", r.Rule.When)
	}
	asked := c.last()
	if !strings.Contains(asked, `"model":"cls"`) || !strings.Contains(asked, `1. writing or fixing tests`) ||
		!strings.Contains(asked, "add a unit test for the parser") || strings.Contains(asked, "secret agent context") ||
		!strings.Contains(asked, `"temperature":0`) {
		t.Fatalf("asked %s", asked)
	}
	// its tool rounds stay, and the classifier isn't asked again
	out, r = postOK(t, s, "s1", chat(msg, nil, 2, ""))
	if !strings.Contains(out, "from kb") || !r.Rule.Held || r.Rule.N != 1 || r.Rule.Classified != nil || c.n() != 1 {
		t.Fatalf("within: %s %+v %d", out, r.Rule, c.n())
	}
	// the next turn is another kind: no rule, so it stays where it was
	// (the group's affinity), with the classifier's answer in the trace
	c.set("0", 0)
	_, r = postOK(t, s, "s1", chat(msg, []string{"what does this function return?"}, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified == nil || r.Rule.Classified.Intent != "" || c.n() != 2 {
		t.Fatalf("turn 2: %+v %d", r.Rule, c.n())
	}
	if !strings.Contains(c.last(), "what does this function return?") || strings.Contains(c.last(), "add a unit test") {
		t.Fatalf("turn 2 asked about %s", c.last())
	}
	// it was told what the turn before was, for a message that only
	// carries on from it; after a turn that was none, nothing
	if r.Rule.Classified.After != "writing or fixing tests" || !strings.Contains(c.last(), "was of kind 1") {
		t.Fatalf("turn 2 not told of turn 1: %+v %s", r.Rule.Classified, c.last())
	}
	_, r = postOK(t, s, "s1", chat(msg, []string{"what does this function return?", "go on"}, 0, ""))
	if r.Rule.Classified == nil || r.Rule.Classified.After != "" || strings.Contains(c.last(), "was of kind") || c.n() != 3 {
		t.Fatalf("turn 3: %+v %s", r.Rule.Classified, c.last())
	}
	// the classifier's call is magpie's own, in the usage
	var seen bool
	for _, call := range s.Recent() {
		if call.Model == "c/cls" && call.Agent == "magpie-router" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the classifier's call isn't recorded as magpie-router's")
	}
}

// Several intents: the classifier chooses among those of the rules that
// may match, each once, and the first rule with its answer decides.
func TestIntentChoosesAmongRules(t *testing.T) {
	s, _, _, c := intented(t,
		provider.Rule{Use: "a/small", Intent: "a quick question"},
		provider.Rule{Use: "b/big", Intent: "Planning a large change"},
		provider.Rule{Use: "b/big", Intent: "planning a large change", Agents: []string{"codex"}},
		provider.Rule{Use: "b/big", Intent: "reviewing code", Images: true}, // no image: not offered
	)
	c.set("2", 0)
	out, r := postOK(t, s, "s2", chat("design the new sync engine", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 2 {
		t.Fatalf("%s %+v", out, r.Rule)
	}
	if got := r.Rule.Classified.Intents; len(got) != 2 || got[0] != "a quick question" || got[1] != "Planning a large change" {
		t.Fatalf("intents %q", got)
	}
	if strings.Contains(c.last(), "reviewing code") || strings.Count(strings.ToLower(c.last()), "planning a large change") != 1 {
		t.Fatalf("asked %s", c.last())
	}
	// an answer that is no number of the list matches nothing
	for _, bad := range []string{"3", "maybe", ""} {
		c.set(bad, 0)
		out, r = postOK(t, s, "s2-"+bad, chat("tell me about "+bad+" things", nil, 0, ""))
		if !strings.Contains(out, "from ka") || r.Rule.N != 0 || r.Rule.Classified.Error == "" {
			t.Fatalf("answer %q: %s %+v", bad, out, r.Rule)
		}
		classified.Lock()
		delete(classified.failed, "c/cls") // an answer that isn't one rests it too; let it be asked again
		classified.Unlock()
	}
	// an answer with words around the number is read
	c.set("Kind 1.", 0)
	_, r = postOK(t, s, "s2-words", chat("what's a goroutine", nil, 0, ""))
	if r.Rule.N != 1 || r.Rule.Classified.Intent != "a quick question" {
		t.Fatalf("%+v", r.Rule)
	}
}

// The classifier isn't asked when no rule that could be the first to
// match waits on an intent.
func TestIntentNotAskedWhenItCantMatter(t *testing.T) {
	s, _, _, c := intented(t,
		provider.Rule{Use: "b/big", Tokens: 1000},
		provider.Rule{Use: "b/big", Intent: "debugging", Agents: []string{"codex"}},
		provider.Rule{Use: "a/small", Intent: "debugging"},
	)
	// a long request: the tokens rule is first to match whatever it says
	_, r := postOK(t, s, "n1", chat(long(2000), nil, 0, ""))
	if r.Rule.N != 1 || r.Rule.Classified != nil || c.n() != 0 {
		t.Fatalf("long: %+v %d", r.Rule, c.n())
	}
	// a short one: rule 3 may match, rule 2 isn't for this agent
	c.set("1", 0)
	_, r = postOK(t, s, "n2", chat("why does this panic", nil, 0, ""))
	if r.Rule.N != 3 || r.Rule.Classified == nil || len(r.Rule.Classified.Intents) != 1 || c.n() != 1 {
		t.Fatalf("short: %+v %d", r.Rule, c.n())
	}
	// an image alone has no words to classify: not asked, nothing matches
	g, ms, _ := provider.FindGroup("group/r")
	img := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Image}}}}}
	asked := 0
	hit := ruleFor("img", g, ms, img, "claude", func(string, []string, before, bool, string) (verdict, error) {
		asked++
		return verdict{Intent: "debugging"}, nil
	})
	if hit.N != 0 || hit.Classified == nil || !strings.Contains(hit.Classified.Error, "no words") || asked != 0 {
		t.Fatalf("image: %+v %+v", hit, hit.Classified)
	}
}

// A classifier that fails or is slow leaves the turn to the rest of the
// rules; after a failure it rests rather than making each turn wait.
func TestIntentClassifierFails(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("", 500)
	out, r := postOK(t, s, "f1", chat("rename this package", nil, 0, ""))
	if !strings.Contains(out, "from ka") || r.Rule.N != 0 || !strings.Contains(r.Rule.Classified.Error, "classifier down") {
		t.Fatalf("down: %s %+v", out, r.Rule)
	}
	asked := c.n()
	c.set("1", 0)
	out, r = postOK(t, s, "f2", chat("rename that package", nil, 0, ""))
	if !strings.Contains(out, "from ka") || !strings.Contains(r.Rule.Classified.Error, "not asked again") || c.n() != asked {
		t.Fatalf("resting: %s %+v %d/%d", out, r.Rule, c.n(), asked)
	}
	// once it has rested, it's asked again
	classified.Lock()
	f := classified.failed["c/cls"]
	f.at = time.Now().Add(-classifyRest - time.Second)
	classified.failed["c/cls"] = f
	classified.Unlock()
	out, r = postOK(t, s, "f3", chat("rename it back", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Classified.Error != "" {
		t.Fatalf("rested: %s %+v", out, r.Rule)
	}
	// slow: past the timeout it's no answer
	was := classifyTimeout
	classifyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { classifyTimeout = was })
	c.mu.Lock()
	c.wait = 2 * time.Second
	c.mu.Unlock()
	t0 := time.Now()
	out, r = postOK(t, s, "f4", chat("rename everything", nil, 0, ""))
	if !strings.Contains(out, "from ka") || !strings.Contains(r.Rule.Classified.Error, "no answer") || time.Since(t0) > 1500*time.Millisecond {
		t.Fatalf("slow: %s %+v %s", out, r.Rule, time.Since(t0))
	}
}

// A classifier that answers something else is up: the turn goes on without
// an intent, and the next one asks it again rather than resting.
func TestIntentOddAnswerDoesNotRest(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("", 0)
	out, r := postOK(t, s, "o1", chat("rename this package", nil, 0, ""))
	if !strings.Contains(out, "from ka") || r.Rule.N != 0 || !strings.Contains(r.Rule.Classified.Error, "not a number") {
		t.Fatalf("empty: %s %+v", out, r.Rule)
	}
	c.set("1", 0)
	out, r = postOK(t, s, "o2", chat("rename that package", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Classified.Error != "" || c.n() != 2 {
		t.Fatalf("after: %s %+v %d", out, r.Rule, c.n())
	}
}

// A classifier that reasons is asked to reason least, with room for its
// reasoning and the number; one whose levels aren't known is asked plainly.
func TestIntentClassifierEffort(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("1", 0)
	postOK(t, s, "e1", chat("rename this package", nil, 0, ""))
	if asked := c.last(); strings.Contains(asked, "reasoning_effort") || !strings.Contains(asked, `"max_tokens":2048`) {
		t.Fatalf("plain: %s", asked)
	}
	p, _ := provider.Find("c")
	for levels, want := range map[string]string{"low,high,max": "low", "none,low,high": "none", "minimal,medium": "minimal"} {
		if err := catalog.SaveLive("c", p.Chat, []catalog.Model{{ID: "cls", Context: 32000, Efforts: strings.Split(levels, ",")}}); err != nil {
			t.Fatal(err)
		}
		postOK(t, s, "e-"+levels, chat("rename "+levels, nil, 0, ""))
		if asked := c.last(); !strings.Contains(asked, `"reasoning_effort":"`+want+`"`) {
			t.Fatalf("%s: %s", levels, asked)
		}
	}
}

// The same message is classified once: another conversation starting with
// it (an agent retrying, a subagent) reads the answer kept.
func TestIntentAnswerKept(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("1", 0)
	_, r := postOK(t, s, "k1", chat("split main.go", nil, 0, ""))
	if r.Rule.N != 1 || r.Rule.Classified.Cached {
		t.Fatalf("%+v", r.Rule)
	}
	_, r = postOK(t, s, "k2", chat("split main.go", nil, 0, ""))
	if r.Rule.N != 1 || !r.Rule.Classified.Cached || c.n() != 1 {
		t.Fatalf("%+v %d", r.Rule, c.n())
	}
}

func TestUserText(t *testing.T) {
	req := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "first"}}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "ok"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\nx\n</system-reminder>"}, {Kind: Text, Text: "  second  "}}},
	}}
	if got := userText(req); got != "second" {
		t.Fatalf("%q", got)
	}
	big := strings.Repeat("a", 3000) + strings.Repeat("m", 5000) + strings.Repeat("z", 1000)
	got := userText(&Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: big}}}}})
	if !strings.HasPrefix(got, strings.Repeat("a", 3000)+"\n…\n") || !strings.HasSuffix(got, strings.Repeat("z", 1000)) || strings.Contains(got, "m") {
		t.Fatalf("%d", len(got))
	}
	for in, want := range map[string]string{"1": "x", "2": "y", " 0 ": "", "2\n": "y"} {
		if got, err := readIntent(in, []string{"x", "y"}); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"3", "-", "none", ""} {
		if _, err := readIntent(in, []string{"x", "y"}); err == nil {
			t.Fatalf("%q read", in)
		}
	}
}
