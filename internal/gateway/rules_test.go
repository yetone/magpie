package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ruleUp is a Chat vendor that answers as its key, streaming when asked,
// and counts the prompt as 3000 tokens, 2500 of them read from its cache.
type ruleUp struct {
	mu    sync.Mutex
	key   string
	fail  int
	calls []string // "stream" or "json"
}

func (u *ruleUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	stream := strings.Contains(string(b), `"stream":true`)
	u.mu.Lock()
	u.calls = append(u.calls, map[bool]string{true: "stream", false: "json"}[stream])
	fail := u.fail
	u.mu.Unlock()
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		io.WriteString(w, `{"error":{"message":"down"}}`)
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+u.key+`"}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`,
			`data: [DONE]`,
		))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+u.key+`"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`)
}

func (u *ruleUp) n() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

// ruled sets up a group of a small model (a/small, 64k, text only) and a
// big one (b/big, 1M, images) with the rules given, a/small first.
func ruled(t *testing.T, rules ...provider.Rule) (*Server, *ruleUp, *ruleUp) {
	t.Helper()
	fresh(t)
	a, b := &ruleUp{key: "ka"}, &ruleUp{key: "kb"}
	no, yes := false, true
	for _, x := range []struct {
		id, model string
		up        *ruleUp
		m         catalog.Model
	}{
		{"a", "small", a, catalog.Model{ID: "small", Context: 64000, ImageInput: &no}},
		{"b", "big", b, catalog.Model{ID: "big", Context: 1000000, Images: true, ImageInput: &yes}},
	} {
		srv := httptest.NewServer(x.up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: x.id, Name: strings.ToUpper(x.id), Key: "k" + x.id, Models: []string{x.model}, Chat: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(x.id, srv.URL+"/v1", []catalog.Model{x.m}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"a/small", "b/big"}, Routing: provider.Ordered, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return New(), a, b
}

// chat builds a Chat conversation: the user's words, each after the first
// answered by a tool call and its result when tools is set.
func chat(first string, turns []string, toolsAfter int, extra string) string {
	var ms []string
	for i, w := range append([]string{first}, turns...) {
		if i > 0 {
			ms = append(ms, `{"role":"assistant","content":"done"}`)
		}
		ms = append(ms, `{"role":"user","content":`+quote(w)+`}`)
	}
	for i := range toolsAfter {
		id := fmt.Sprintf("c%d", i)
		ms = append(ms, `{"role":"assistant","content":null,"tool_calls":[{"id":"`+id+`","type":"function","function":{"name":"ls","arguments":"{}"}}]}`,
			`{"role":"tool","tool_call_id":"`+id+`","content":"file.txt"}`)
	}
	return `{"model":"group/r"` + extra + `,"messages":[` + strings.Join(ms, ",") + `]}`
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func lastRoute(s *Server) Route {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	return *s.trace.routes[len(s.trace.routes)-1]
}

func long(tokens int) string { return strings.Repeat("abcd", tokens) }

// A rule is looked at as a user's turn begins, and holds for every tool
// round of the turn, however long it grows meanwhile.
func TestRuleDecidesAtTurnStartAndHolds(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	post := func(body string) (string, Route) {
		t.Helper()
		code, out := postAs(t, s, "sess", body)
		if code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		return out, lastRoute(s)
	}
	// turn 1, short: no rule, the group's order
	out, r := post(chat("hello", nil, 0, ""))
	if !strings.Contains(out, "from ka") || r.Rule == nil || r.Rule.N != 0 || r.Rule.Held || r.Rule.Turn != 1 {
		t.Fatalf("turn 1: %s %+v", out, r.Rule)
	}
	// its tool rounds grow past the rule's length: they stay
	grown := long(30000)
	out, r = post(chat("hello", nil, 1, "")[:0] + strings.Replace(chat("hello", nil, 1, ""), `"file.txt"`, quote(grown), 1))
	if !strings.Contains(out, "from ka") || !r.Rule.Held || r.Rule.N != 0 || r.Rule.Tokens < 20000 {
		t.Fatalf("turn 1 tool round: %s %+v", out, r.Rule)
	}
	// turn 2 begins long: the rule sends it to b, over the conversation
	// having been on a
	out, r = post(chat("hello", []string{long(25000)}, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Use != "b/big" || r.Rule.Held || r.Rule.Unready {
		t.Fatalf("turn 2: %s %+v", out, r.Rule)
	}
	if r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "rule" {
		t.Fatalf("turn 2 affinity: %+v", r.Affinity)
	}
	if r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("turn 2 order: %+v", r.Order)
	}
	// its tool rounds stay on b
	for n := 1; n <= 3; n++ {
		out, r = post(chat("hello", []string{long(25000)}, n, ""))
		if !strings.Contains(out, "from kb") || !r.Rule.Held || r.Rule.N != 1 || !r.Affinity.Kept {
			t.Fatalf("turn 2 round %d: %s %+v %+v", n, out, r.Rule, r.Affinity)
		}
	}
	if a.n() != 2 || b.n() != 4 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// What the vendor counted the conversation's last request as is a floor
// on how long the next is taken to be: a short new turn of a long
// conversation is still a long request.
func TestRuleTokensFloorFromUsage(t *testing.T) {
	s, _, _ := ruled(t, provider.Rule{Use: "b/big", Tokens: 2000})
	if code, out := postAs(t, s, "sess", chat("hi", nil, 0, "")); code != 200 || !strings.Contains(out, "from ka") {
		t.Fatalf("%d %s", code, out)
	}
	// the vendor said 3000 tokens (500 new, 2500 read from its cache)
	code, out := postAs(t, s, "sess", chat("hi", []string{"more"}, 0, ""))
	r := lastRoute(s)
	if code != 200 || !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Tokens != 3000 {
		t.Fatalf("%d %s %+v", code, out, r.Rule)
	}
	// another conversation starts short
	if code, out := postAs(t, s, "other", chat("hi", nil, 0, "")); code != 200 || !strings.Contains(out, "from ka") {
		t.Fatalf("other: %d %s", code, out)
	}
}

// A turn magpie didn't see begin — magpie restarted, the rules changed —
// isn't moved by them; the next turn is looked at.
func TestRuleWaitsForATurnItDidNotSeeBegin(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	code, out := postAs(t, s, "sess", chat(long(25000), nil, 2, ""))
	r := lastRoute(s)
	if code != 200 || !strings.Contains(out, "from ka") || !r.Rule.Waits || r.Rule.N != 0 {
		t.Fatalf("%d %s %+v", code, out, r.Rule)
	}
	code, out = postAs(t, s, "sess", chat(long(25000), []string{"next"}, 0, ""))
	if r = lastRoute(s); code != 200 || !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Waits {
		t.Fatalf("%d %s %+v", code, out, r.Rule)
	}
	// the rule's member changed within the turn: it waits again
	g, _, _ := provider.FindGroup("group/r")
	g.Rules[0].Use = "a/small"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	postAs(t, s, "sess", chat(long(25000), []string{"next"}, 1, ""))
	if r = lastRoute(s); !r.Rule.Waits || r.Rule.Use != "" {
		t.Fatalf("changed rule: %+v", r.Rule)
	}
	if a.n() != 1 || b.n() != 2 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A subagent shares its agent's session but not its first words: its turn
// is its own, and where it goes doesn't move the main conversation.
func TestRuleSubagentHasItsOwnTurn(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	step := func(name, body, want string, n int, held bool) {
		t.Helper()
		_, out := postAs(t, s, "sess", body)
		r := lastRoute(s)
		if !strings.Contains(out, "from "+want) || r.Rule.N != n || r.Rule.Held != held || r.Rule.Waits {
			t.Fatalf("%s: %s %+v %+v", name, out, r.Rule, r.Affinity)
		}
	}
	step("main begins, short", chat("main task", nil, 0, ""), "ka", 0, false)
	step("subagent begins, long", chat("explore "+long(25000), nil, 0, ""), "kb", 1, false)
	step("main round", chat("main task", nil, 1, ""), "ka", 0, true)
	step("subagent round", chat("explore "+long(25000), nil, 1, ""), "kb", 1, true)
	step("main round again", chat("main task", nil, 2, ""), "ka", 0, true)
	step("a short subagent", chat("find tests", nil, 0, ""), "ka", 0, false)
	if a.n() != 4 || b.n() != 2 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A turn that outgrows the model it is on moves to a rule's member with
// more room — the one time a turn moves.
func TestRuleTurnOutgrowsItsModel(t *testing.T) {
	s, _, _ := ruled(t, provider.Rule{Use: "b/big", Tokens: 50000})
	if _, out := postAs(t, s, "sess", chat("hi", nil, 0, "")); !strings.Contains(out, "from ka") {
		t.Fatal(out)
	}
	// just short of 95% of a's 64k: it stays
	body := strings.Replace(chat("hi", nil, 1, ""), `"file.txt"`, quote(long(60000)), 1)
	if _, out := postAs(t, s, "sess", body); !strings.Contains(out, "from ka") || !lastRoute(s).Rule.Held {
		t.Fatalf("%s %+v", out, lastRoute(s).Rule)
	}
	// at 95%: to b
	body = strings.Replace(chat("hi", nil, 2, ""), `"file.txt"`, quote(long(61000)), 1)
	_, out := postAs(t, s, "sess", body)
	r := lastRoute(s)
	if !strings.Contains(out, "from kb") || !r.Rule.Grown || r.Rule.N != 1 || r.Rule.Held {
		t.Fatalf("%s %+v", out, r.Rule)
	}
	// and the turn's later rounds stay there
	body = strings.Replace(chat("hi", nil, 3, ""), `"file.txt"`, quote(long(61000)), 1)
	_, out = postAs(t, s, "sess", body)
	if r = lastRoute(s); !strings.Contains(out, "from kb") || !r.Rule.Held || r.Rule.N != 1 {
		t.Fatalf("%s %+v", out, r.Rule)
	}
}

// When the rule's member fails, the group's others take over, and the
// turn stays with who took over rather than trying the failed one again.
func TestRuleFailover(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	b.fail = 500
	code, out := postAs(t, s, "sess", chat(long(25000), nil, 0, ""))
	r := lastRoute(s)
	if code != 200 || !strings.Contains(out, "from ka") || r.Rule.N != 1 || len(r.Tries) != 2 {
		t.Fatalf("%d %s %+v %d", code, out, r.Rule, len(r.Tries))
	}
	b.fail = 0
	code, out = postAs(t, s, "sess", chat(long(25000), nil, 1, ""))
	r = lastRoute(s)
	if code != 200 || !strings.Contains(out, "from ka") || !r.Rule.Held || len(r.Tries) != 1 {
		t.Fatalf("round: %d %s %+v %d", code, out, r.Rule, len(r.Tries))
	}
	if a.n() != 2 || b.n() != 1 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A rule whose member is resting leaves the request to the group's order,
// and says so.
func TestRuleUnreadyMember(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	b.fail = 429
	postAs(t, s, "one", chat(long(25000), nil, 0, "")) // b rests now
	b.fail = 0
	code, out := postAs(t, s, "two", chat(long(25000), nil, 0, ""))
	r := lastRoute(s)
	if code != 200 || !strings.Contains(out, "from ka") || r.Rule.N != 1 || !r.Rule.Unready {
		t.Fatalf("%d %s %+v", code, out, r.Rule)
	}
	if b.n() != 1 || a.n() != 2 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}

// A rule for images lets a group with a text-only member take them, and
// sends them where they are seen; without one, a text-only member keeps
// images out of the group.
func TestRuleImages(t *testing.T) {
	img := `{"model":"group/r","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	s, _, b := ruled(t)
	if code, _ := postAs(t, s, "x", img); code != 400 || b.n() != 0 {
		t.Fatalf("no rules: %d, b %d", code, b.n())
	}
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Images: true})
	code, out := postAs(t, s, "x", img)
	if r := lastRoute(s); code != 200 || !strings.Contains(out, "from kb") || r.Rule.N != 1 || !r.Rule.Images {
		t.Fatalf("%d %s %+v", code, out, r.Rule)
	}
	if code, out := postAs(t, s, "y", chat("words only", nil, 0, "")); code != 200 || !strings.Contains(out, "from ka") {
		t.Fatalf("%d %s", code, out)
	}
	if a.n() != 1 || b.n() != 1 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
	for _, e := range provider.Catalog() {
		if e.ID == "group/r" && (!e.Images || e.Context != 64000) {
			t.Fatalf("catalog: %+v", e)
		}
	}
}

// Rules for reasoning and for agents.
func TestRuleEffortAndAgent(t *testing.T) {
	s, _, _ := ruled(t,
		provider.Rule{Use: "b/big", Effort: "high"},
		provider.Rule{Use: "b/big", Agents: []string{"ruletester"}},
	)
	for _, tc := range []struct {
		extra, ua, want string
		n               int
	}{
		{"", "", "ka", 0},
		{`,"reasoning_effort":"medium"`, "", "ka", 0},
		{`,"reasoning_effort":"high"`, "", "kb", 1},
		{`,"reasoning_effort":"xhigh"`, "", "kb", 1},
		{"", "RuleTester/1.0", "kb", 2},
		{"", "other/1.0", "ka", 0},
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("hi "+tc.extra+tc.ua, nil, 0, tc.extra)))
		if tc.ua != "" {
			req.Header.Set("User-Agent", tc.ua)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		r := lastRoute(s)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from "+tc.want) || r.Rule.N != tc.n {
			t.Errorf("%q %q: %d %s %+v", tc.extra, tc.ua, rec.Code, rec.Body.String(), r.Rule)
		}
	}
}

// Every protocol an agent speaks, streaming or not, goes by the rules.
func TestRuleEveryProtocol(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	for _, short := range []bool{true, false} {
		text := long(25000)
		want := "kb"
		if short {
			text, want = "hi", "ka"
		}
		for _, stream := range []bool{false, true} {
			st := map[bool]string{true: `,"stream":true`, false: ""}[stream]
			for _, tc := range []struct{ path, body string }{
				{"/v1/chat/completions", `{"model":"group/r"` + st + `,"messages":[{"role":"user","content":` + quote(text) + `}]}`},
				{"/v1/responses", `{"model":"group/r"` + st + `,"input":[{"role":"user","content":[{"type":"input_text","text":` + quote(text) + `}]}]}`},
				{"/v1/messages", `{"model":"group/r","max_tokens":16` + st + `,"messages":[{"role":"user","content":` + quote(text) + `}]}`},
				{map[bool]string{true: "/v1beta/models/group/r:streamGenerateContent?alt=sse", false: "/v1beta/models/group/r:generateContent"}[stream],
					`{"contents":[{"role":"user","parts":[{"text":` + quote(text) + `}]}]}`},
			} {
				name := fmt.Sprintf("%s stream=%v short=%v", tc.path, stream, short)
				na, nb := a.n(), b.n()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
				req.Header.Set("x-session-id", name)
				s.Handler().ServeHTTP(rec, req)
				r := lastRoute(s)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from "+want) {
					t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
					continue
				}
				if r.Rule == nil || (r.Rule.N == 1) == short || r.Rule.Turn != 1 {
					t.Errorf("%s: %+v", name, r.Rule)
				}
				// magpie streams from the vendor for any agent not speaking Chat
				up := map[string]*ruleUp{"ka": a, "kb": b}[want]
				if a.n()+b.n() != na+nb+1 || up.n() == 0 || tc.path == "/v1/chat/completions" && up.calls[len(up.calls)-1] != map[bool]string{true: "stream", false: "json"}[stream] {
					t.Errorf("%s: upstream calls a %v b %v", name, a.calls, b.calls)
				}
			}
		}
	}
}

// A group without rules routes as it did: no rule in its trace, and the
// conversation kept where it was.
func TestNoRulesUnchanged(t *testing.T) {
	s, a, b := ruled(t)
	for i, body := range []string{chat("hi", nil, 0, ""), chat("hi", []string{long(30000)}, 0, ""), chat("hi", []string{long(30000)}, 1, "")} {
		code, out := postAs(t, s, "sess", body)
		if r := lastRoute(s); code != 200 || !strings.Contains(out, "from ka") || r.Rule != nil {
			t.Fatalf("%d: %d %s %+v", i, code, out, r.Rule)
		}
	}
	if a.n() != 3 || b.n() != 0 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
	turnRules.Lock()
	defer turnRules.Unlock()
	if len(turnRules.m) != 0 {
		t.Fatalf("kept %v", turnRules.m)
	}
}

func TestRuleFirstAndOfMember(t *testing.T) {
	a, b := provider.Provider{ID: "a"}, provider.Provider{ID: "ab"}
	cs := []candidate{
		{p: a, model: "m", rest: "a"},
		{p: b, model: "m", rest: "ab"},
		{p: b, model: "x", rest: "ab#k2"},
		{p: b, model: "m", rest: "ab#k2"},
		{p: b, model: "m", rest: "ab@me"},
	}
	var pl planned
	for _, c := range cs {
		pl.order = append(pl.order, Weighed{ID: c.rest, Model: c.model})
	}
	pl.order[3].Rest = &Rest{}
	got, gp, ok := ruleFirst([]provider.Member{{Provider: b, Model: "m"}}, cs, pl)
	var ids []string
	for i, c := range got {
		ids = append(ids, c.rest+"/"+c.model)
		if gp.order[i].ID != c.rest || gp.order[i].Model != c.model {
			t.Fatalf("order out of step at %d: %+v", i, gp.order)
		}
	}
	if !ok || strings.Join(ids, " ") != "ab/m ab@me/m a/m ab#k2/x ab#k2/m" {
		t.Fatalf("%v %v", ok, ids)
	}
	if _, _, ok := ruleFirst([]provider.Member{{Provider: provider.Provider{ID: "c"}, Model: "m"}}, cs, pl); ok {
		t.Fatal("no candidates of c")
	}
	// "a" is not "ab"'s
	if ofMember(cs[1], provider.Member{Provider: a, Model: "m"}) {
		t.Fatal("prefix taken for the provider")
	}
}

// Claude Code puts a system message after its tool results: the turn
// still goes on.
func TestTurnInPastTrailingSystem(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		turn       int
		within     bool
	}{
		{"begins", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"read it"}]},{"role":"system","content":"note"}]}`, 1, false},
		{"tool round", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"read it"}]},{"role":"system","content":"note"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]},` +
			`{"role":"system","content":[{"type":"text","text":"reminder"}]}]}`, 1, true},
		{"next turn", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"read it"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]},` +
			`{"role":"assistant","content":"done"},{"role":"user","content":"more"},{"role":"system","content":"note"}]}`, 2, false},
	} {
		turn, within := turnOf(provider.Anthropic, []byte(tc.body))
		if turn != tc.turn || within != tc.within {
			t.Errorf("%s: turn %d within %v", tc.name, turn, within)
		}
	}
}

// An unlisted provider is still served through a group. Its context must
// count when a long tool turn needs the group's larger model.
func TestRuleTurnOutgrowsUnlistedMember(t *testing.T) {
	s, _, _ := ruled(t, provider.Rule{Use: "b/big", Tokens: 50000})
	p, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Unlisted = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if _, out := postAs(t, s, "sess", chat("hi", nil, 0, "")); !strings.Contains(out, "from ka") {
		t.Fatal(out)
	}
	body := strings.Replace(chat("hi", nil, 1, ""), `"file.txt"`, quote(long(61000)), 1)
	_, out := postAs(t, s, "sess", body)
	r := lastRoute(s)
	if !strings.Contains(out, "from kb") || !r.Rule.Grown {
		t.Fatalf("unlisted member failed to grow: %s %+v", out, r.Rule)
	}
}

// An unlisted text-only member still keeps images out of its group.
func TestRuleImagesUnlistedMember(t *testing.T) {
	img := `{"model":"group/r","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	s, a, b := ruled(t)
	p, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Unlisted = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if code, out := postAs(t, s, "x", img); code != 400 || a.n() != 0 || b.n() != 0 {
		t.Fatalf("%d %s, a %d b %d", code, out, a.n(), b.n())
	}
}
// A vision rule cannot fall back to a text-only member with the image still
// attached when the vision provider fails.
func TestImageRuleDoesNotLeakImageToTextOnlyFallback(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Images: true})
	b.mu.Lock()
	b.fail = 503
	b.mu.Unlock()
	img := `{"model":"group/r","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	code, _ := postAs(t, s, "image-fallback", img)
	if code != 503 {
		t.Fatalf("vision provider failed with status %d, want 503", code)
	}
	if a.n() != 0 {
		t.Fatalf("image was sent to text-only fallback %d times", a.n())
	}
}
