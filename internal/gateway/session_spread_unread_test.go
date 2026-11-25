package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// wangtufly on X: a group of a Codex account (Plus, its week 62% used,
// renewing in 45 hours) and a relay that tells nothing of what it has
// left, on Quota first: requests went to the relay, not Codex. Routing
// puts Codex first, as what its week has left is lost at the reset and
// the relay's balance keeps; but with Stays: Session or Strict, a new
// conversation was spread to the seat the fewest others were on
// (leastHeld), and the relay, held by nobody, took every other one and
// kept it for the session. A key nothing is read of is no seat to spread
// to while one whose allowance is known leads (unreadBehind), under each
// routing that spreads sessions.
func TestSessionsNotSpreadToAnUnreadRelay(t *testing.T) {
	for _, routing := range []string{"", provider.LeastUsed, provider.Pace} {
		for _, mode := range []string{provider.AffinitySession, provider.AffinityStrict} {
			t.Run(cmpOr(routing, "smart")+"/"+mode, func(t *testing.T) {
				g := newUnreadRelayGroup(t, routing, mode, 62)
				for _, sess := range []string{"s1", "s2", "s3", "s4"} {
					if on := g.ask(t, sess, 1); on != "codex" {
						t.Fatalf("new conversation %s went to the %s, want codex", sess, on)
					}
				}
				for _, sess := range []string{"s1", "s2"} {
					if on := g.ask(t, sess, 2); on != "codex" {
						t.Fatalf("%s's next turn went to the %s, want codex", sess, on)
					}
				}
			})
		}
	}
	// Codex's week used up: routing puts the relay first, and new
	// conversations go there, as before
	t.Run("codex spent", func(t *testing.T) {
		g := newUnreadRelayGroup(t, "", provider.AffinitySession, 100)
		for _, sess := range []string{"s1", "s2"} {
			if on := g.ask(t, sess, 1); on != "relay" {
				t.Fatalf("new conversation %s went to %s with Codex spent, want the relay", sess, on)
			}
		}
	})
}

type unreadRelayGroup struct{ s *Server }

// newUnreadRelayGroup is the reporter's group: Codex's account first, its
// week used as given, its five hours not started; "H API 2", a relay key
// on the same model, second.
func newUnreadRelayGroup(t *testing.T, routing, mode string, week float64) *unreadRelayGroup {
	t.Helper()
	codexSignedIn(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7000,"output_tokens":1,"input_tokens_details":{"cached_tokens":6000}}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	wasA := allowances
	t.Cleanup(func() { allowances = wasA })
	allowances = func(agent string) map[string]provider.Allowance {
		if agent != "codex" {
			return nil
		}
		now := time.Now()
		return map[string]provider.Allowance{"me@example.com": {
			{Used: 0, Resets: now.Add(5 * time.Hour), Span: 5 * time.Hour},
			{Used: week, Resets: now.Add(45 * time.Hour), Span: 7 * 24 * time.Hour},
		}}
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7000,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":6000}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(relay.Close)
	if err := provider.Save(provider.Provider{ID: "h-api-2", Name: "H API 2", Key: "k", Models: []string{"gpt-6.1-sol"}, Chat: relay.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"codex/gpt-6.1-sol", "h-api-2/gpt-6.1-sol"}, Routing: routing, Affinity: mode}); err != nil {
		t.Fatal(err)
	}
	return &unreadRelayGroup{s: New()}
}

// ask sends turn n of session sess, and says who answered: "codex" or
// "relay".
func (g *unreadRelayGroup) ask(t *testing.T, sess string, n int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(responsesTurn(n)))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", sess)
	g.s.Handler().ServeHTTP(rec, req)
	switch body := rec.Body.String(); {
	case strings.Contains(body, "pong"):
		return "codex"
	case strings.Contains(body, "hello"):
		return "relay"
	default:
		t.Fatalf("turn %d of %s: %d %s", n, sess, rec.Code, body)
	}
	return ""
}
