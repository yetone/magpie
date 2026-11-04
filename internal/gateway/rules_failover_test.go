package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1946 (XcodeFish): a group of two groups, coding-fast and
// coding-quality, its rule sending every turn to coding-quality. When
// coding-quality's member failed mid-turn, the request went to coding-fast
// before coding-quality's other member, and the turn stayed there: the
// rule held for the turn, the one the turn stayed on was kept whoever it
// was, and the rule's order wasn't applied at all.
func TestHeldRuleFailsOverWithinItsMember(t *testing.T) {
	fresh(t)
	k := &keyed{fail: map[string]int{}}
	for _, id := range []string{"f1", "f2", "q1", "q2"} {
		serveOn(t, id, "k"+id, []string{"m"}, k)
	}
	for _, g := range []provider.Group{
		{Name: "Coding Fast", Members: []string{"f1/m", "f2/m"}, Routing: provider.Ordered},
		{Name: "Coding Quality", Members: []string{"q1/m", "q2/m"}, Routing: provider.Ordered},
		{Name: "Coding", Members: []string{"group/coding-fast", "group/coding-quality"}, Routing: provider.Ordered,
			Rules: []provider.Rule{{Use: "group/coding-quality", Tokens: 1}}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	body := func(rounds int) string {
		ms := []string{`{"role":"user","content":"fix the bug"}`}
		for range rounds {
			ms = append(ms, `{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]}`,
				`{"role":"tool","tool_call_id":"c","content":"file.txt"}`)
		}
		return `{"model":"group/coding","messages":[` + strings.Join(ms, ",") + `]}`
	}
	send := func(session string, rounds int, want string) Route {
		t.Helper()
		k.tried = nil
		code, out := postAs(t, s, session, body(rounds))
		r := lastRoute(s)
		if code != 200 || !strings.Contains(out, "from k"+want) {
			t.Fatalf("%s round %d: %d %s, tried %v, affinity %+v", session, rounds, code, out, k.tried, r.Affinity)
		}
		return r
	}
	order := func(r Route) string {
		var out []string
		for _, w := range r.Order {
			out = append(out, w.Provider)
		}
		return strings.Join(out, " ")
	}

	// the turn begins on coding-quality
	if r := send("one", 0, "q1"); order(r) != "q1 q2 f1 f2" {
		t.Fatalf("turn start: %s", order(r))
	}
	// q1 fails in a tool round: asked once more, as the turn is kept on
	// it (keptAgain), then q2, coding-quality's other, takes it
	k.fail["kq1"] = 500
	r := send("one", 1, "q2")
	if strings.Join(k.tried, " ") != "kq1 kq1 kq2" || !r.Rule.Held || r.Rule.Unready {
		t.Fatalf("failover: tried %v order %s rule %+v", k.tried, order(r), r.Rule)
	}
	// q2 is kept for the turn, q1 back or not: it is of the rule's member
	delete(k.fail, "kq1")
	s.UnrestAll("q1")
	if r := send("one", 2, "q2"); !r.Affinity.Kept || r.Affinity.Why != "turn" || order(r) != "q2 q1 f1 f2" {
		t.Fatalf("kept within the member: %s %+v", order(r), r.Affinity)
	}

	// coding-quality failing whole, coding-fast answers the round
	send("two", 0, "q1")
	k.fail["kq1"], k.fail["kq2"] = 500, 500
	send("two", 1, "f1")
	if strings.Join(k.tried, " ") != "kq1 kq1 kq2 kf1" { // q1, kept, asked once more
		t.Fatalf("tried %v", k.tried)
	}
	// both resting, the turn stays on f1
	delete(k.fail, "kq1")
	delete(k.fail, "kq2")
	if r := send("two", 2, "f1"); !r.Affinity.Kept {
		t.Fatalf("resting: %s %+v", order(r), r.Affinity)
	}
	// their rests over, the turn goes back to coding-quality, not kept on
	// the fallback
	s.UnrestAll("q1")
	s.UnrestAll("q2")
	r = send("two", 3, "q1")
	if r.Affinity.Kept || r.Affinity.Why != "rule" || order(r) != "q1 q2 f1 f2" || !r.Rule.Held {
		t.Fatalf("back to the rule's member: %s %+v %+v", order(r), r.Affinity, r.Rule)
	}
}
