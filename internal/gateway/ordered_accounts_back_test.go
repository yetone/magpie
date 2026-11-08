package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's keys or accounts routed In order give a running
// conversation to the one the user put first at its next turn, as a group
// routed In order gives it to its first member (backToFirst): CryptoSarom
// on X made a new Codex account First, In order, and the conversation
// running on the old account stayed there, kept by its cache, until a 90%
// cap on the old one moved it. Within a turn it stays where the turn's
// reasoning was sealed; Smart, or Stays: Session, keep it where it is.
func TestOrderedAccountsGiveTheConversationToTheFirst(t *testing.T) {
	for _, c := range []struct {
		name, routing, mode string
		want                string // who answers turn 1, turn 2's two requests, turn 3
		why                 string // the affinity of turn 2's first request
	}{
		{"in order", provider.Ordered, "", "k1 k2 k2 k2", "order"},
		{"in order, Stays: Session", provider.Ordered, provider.AffinitySession, "k1 k1 k1 k1", "session"},
		{"smart", "", "", "k1 k1 k1 k1", "cache"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &cacheWriter{wrote: 3000}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			save := func(first, second string) {
				t.Helper()
				p := provider.Provider{ID: "ord", Name: "Ord", Key: first, Keys: []provider.KeyAccount{{Key: second}},
					Models: []string{"m1"}, Anthropic: srv.URL + "/v1", Routing: c.routing, Affinity: c.mode}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				forgetRouting()
			}
			save("k1", "k2")
			s := New()
			t.Cleanup(s.subscription.abortAll)
			conv := `{"role":"user","content":"fix the bug"}`
			var got []string
			for i := range 4 {
				if i == 1 {
					save("k2", "k1") // the user makes k2 First
				}
				body := `{"model":"ord/m1","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + conv + `]}`
				r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				r.Header.Set("x-claude-code-session-id", "s-1")
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
				}
				out := rec.Body.String()
				switch {
				case strings.Contains(out, "from k1"):
					got = append(got, "k1")
				case strings.Contains(out, "from k2"):
					got = append(got, "k2")
				default:
					t.Fatalf("request %d answered by nobody known: %s", i, out)
				}
				if a := s.trace.routes[len(s.trace.routes)-1].Affinity; i == 1 && (a == nil || a.Why != c.why) {
					t.Errorf("turn 2's first request: affinity %+v, want why %q", a, c.why)
				}
				if i == 1 {
					// a tool loop: the turn goes on
					conv += `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}`
				} else {
					conv += `,{"role":"assistant","content":"done"},{"role":"user","content":"and more"}`
				}
			}
			if g := strings.Join(got, " "); g != c.want {
				t.Fatalf("answered by %s, want %s", g, c.want)
			}
		})
	}
}
