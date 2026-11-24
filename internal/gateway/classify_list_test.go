package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// clsListed is intented's group asking c/cls first and d/cls2 when c/cls
// can't answer (qinzhidaoa on X: the one classifier they had, MiMo on
// OpenCode Go, answered 502 ECONNRESET, and every turn lost its intent).
func clsListed(t *testing.T, rules ...provider.Rule) (*Server, *clsUp, *clsUp) {
	t.Helper()
	resetClassified(t)
	s, _, _, c := intented(t, rules...)
	d := &clsUp{answer: "0"}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "d", Name: "D", Key: "kd", Models: []string{"cls2"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("d", srv.URL+"/v1", []catalog.Model{{ID: "cls2", Context: 32000}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.SetClassifiers([]string{"c/cls", "d/cls2"})
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := provider.FindGroup("group/r"); strings.Join(g.Classifiers(), ",") != "c/cls,d/cls2" {
		t.Fatalf("saved %q %q", g.Classifier, g.ClassifierMore)
	}
	return s, c, d
}

// The first classifier failing, the next is asked, and the turn goes by
// its answer; the decision says who answered and why the first didn't.
// Resting after its failure, the first isn't asked again: the next is,
// at once.
func TestClassifierListFailsOver(t *testing.T) {
	s, c, d := clsListed(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("", 502)
	d.set("1", 0)
	out, r := postOK(t, s, "fo1", chat("rename this package", nil, 0, ""))
	cl := r.Rule.Classified
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || cl.By != "d/cls2" || cl.Intent != "refactoring" || cl.Error != "" {
		t.Fatalf("502 then an answer: %s %+v %+v", out, r.Rule, cl)
	}
	if len(cl.Tried) != 1 || cl.Tried[0].By != "c/cls" || !strings.Contains(cl.Tried[0].Error, "classifier down") {
		t.Fatalf("tried: %+v", cl.Tried)
	}
	if _, _, ok := GroupClassifierFailure("r", ClassifiersKey([]string{"c/cls", "d/cls2"})); ok {
		t.Fatal("a failure said for the group though its second classifier answered")
	}
	asked := c.n()
	_, r = postOK(t, s, "fo2", chat("rename that package", nil, 0, ""))
	cl = r.Rule.Classified
	if c.n() != asked || cl.By != "d/cls2" || r.Rule.N != 1 || len(cl.Tried) != 1 || !strings.Contains(cl.Tried[0].Error, "not asked again") {
		t.Fatalf("resting: asked %d more, %+v", c.n()-asked, cl)
	}
}

// Every classifier failing, the turn goes as one did with a single
// classifier failing: the intent the turn before had, or none, and the
// group says why each couldn't.
func TestClassifierListAllFail(t *testing.T) {
	s, c, d := clsListed(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("1", 0)
	out, r := postOK(t, s, "af", chat("rename this package", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.Classified.By != "c/cls" || len(r.Rule.Classified.Tried) != 0 || d.n() != 0 {
		t.Fatalf("turn 1, the first answering: %s %+v (d asked %d)", out, r.Rule.Classified, d.n())
	}
	c.set("", 500)
	d.set("", 503)
	out, r = postOK(t, s, "af", chat("rename this package", []string{"and the tests"}, 0, ""))
	cl := r.Rule.Classified
	if !strings.Contains(out, "from kb") || cl.Intent != "refactoring" || cl.Fallback != "previous" || cl.By != "d/cls2" || len(cl.Tried) != 1 {
		t.Fatalf("both down, an intent before: %s %+v", out, cl)
	}
	why, _, ok := GroupClassifierFailure("r", ClassifiersKey([]string{"c/cls", "d/cls2"}))
	if !ok || !strings.Contains(why, "c/cls: ") || !strings.Contains(why, "d/cls2: ") {
		t.Fatalf("the group's failure: %q %v", why, ok)
	}
	out, r = postOK(t, s, "af2", chat("rename everything", nil, 0, ""))
	if cl := r.Rule.Classified; !strings.Contains(out, "from ka") || r.Rule.N != 0 || cl.Fallback != "" || cl.Error == "" {
		t.Fatalf("both down, none before: %s %+v %+v", out, r.Rule, cl)
	}
}

// One that answers, though with nothing a rule can take, has answered:
// the next isn't asked a second time for the same turn.
func TestClassifierListAnswerIsNotPassedOver(t *testing.T) {
	s, c, d := clsListed(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("maybe", 0)
	d.set("1", 0)
	_, r := postOK(t, s, "an", chat("rename this package", nil, 0, ""))
	if cl := r.Rule.Classified; d.n() != 0 || cl.By != "c/cls" || !strings.Contains(cl.Error, "not a number") {
		t.Fatalf("d asked %d times: %+v", d.n(), cl)
	}
}

// A first classifier that hangs leaves the next the rest of the request's
// wait, and is rested for it as for a 502.
func TestClassifierListSharesTheWait(t *testing.T) {
	was := classifyUnit
	classifyUnit = 100 * time.Millisecond // 15 seconds unset are 1.5s
	t.Cleanup(func() { classifyUnit = was })
	s, c, d := clsListed(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	c.set("1", 0)
	c.mu.Lock()
	c.wait = 10 * time.Second
	c.mu.Unlock()
	d.set("1", 0)
	t0 := time.Now()
	out, r := postOK(t, s, "sw1", chat("rename this package", nil, 0, ""))
	cl := r.Rule.Classified
	if !strings.Contains(out, "from kb") || cl.By != "d/cls2" || cl.Intent != "refactoring" || len(cl.Tried) != 1 || !strings.Contains(cl.Tried[0].Error, "no answer in its") {
		t.Fatalf("the first hanging: %s %+v", out, cl)
	}
	if took := time.Since(t0); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	asked := c.n()
	_, r = postOK(t, s, "sw2", chat("rename that package", nil, 0, ""))
	if c.n() != asked || r.Rule.Classified.By != "d/cls2" || r.Rule.N != 1 {
		t.Fatalf("after: asked %d more, %+v", c.n()-asked, r.Rule.Classified)
	}
}

// effortUp answers a question of intent with its answer and fails one
// of effort with a 502.
type effortUp struct {
	clsUp
}

func (u *effortUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.asked = append(u.asked, string(b))
	answer := u.answer
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(string(b), "Levels:") {
		w.WriteHeader(502)
		io.WriteString(w, `{"error":{"message":"effort down"}}`)
		return
	}
	io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":`+quote(answer)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":1}}`)
}

// A first classifier that tells the intent but fails to say how hard the
// turn is has answered (2bb010ad1): its intent routes the turn, and the
// next isn't asked.
func TestClassifierListEffortFailureHoldsTheIntent(t *testing.T) {
	s, _, d := clsListed(t, provider.Rule{Use: "b/big", Intent: "refactoring"})
	e := &effortUp{clsUp: clsUp{answer: "1"}}
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"cls"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("c", srv.URL+"/v1", []catalog.Model{{ID: "cls", Context: 32000}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Effort = provider.EffortAuto
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	d.set("1", 0)
	out, r := postOK(t, s, "ef", chat("rename this package", nil, 0, `,"reasoning_effort":"low"`))
	cl := r.Rule.Classified
	if !strings.Contains(out, "from kb") || cl.By != "c/cls" || cl.Intent != "refactoring" || cl.EffortError == "" || cl.Error != "" || d.n() != 0 {
		t.Fatalf("%s %+v (d asked %d)", out, cl, d.n())
	}
}
