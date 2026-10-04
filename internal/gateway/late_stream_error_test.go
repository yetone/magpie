package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A member whose stream breaks off after some of the answer — its 200
// already sent — hasn't answered the conversation: the agent's retry, the
// same conversation, goes to the group's other member, not back to the
// one that failed it (#733: Codex's retries stayed on Cursor, "Unable to
// reach the model provider" eleven times, kept there by the turn's
// affinity, while Grok was fine).
func TestLateStreamErrorNotKept(t *testing.T) {
	const broke = "Unable to reach the model provider: We're having trouble connecting to the model provider."
	for _, c := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`},
		{"responses", "/v1/responses", `{"model":"group/g","stream":true,"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"a.go"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			var tried []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				tried = append(tried, who)
				w.Header().Set("Content-Type", "text/event-stream")
				if who == "broken" {
					io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look"}}]}`,
						`data: {"error":{"message":"`+broke+`"}}`))
					return
				}
				io.WriteString(w, sse(`data: {"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"from fine"}}]}`,
					`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					`data: [DONE]`))
			}))
			defer up.Close()
			for _, p := range []provider.Provider{
				{ID: "flaky", Name: "Flaky", Key: "broken", Chat: up.URL + "/v1", Models: []string{"m"}},
				{ID: "steady", Name: "Steady", Key: "fine", Chat: up.URL + "/v1", Models: []string{"m"}},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"flaky/m", "steady/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			post := func() string {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
				req.Header.Set("x-session-id", "s1")
				s.Handler().ServeHTTP(rec, req)
				return rec.Body.String()
			}
			if out := post(); !strings.Contains(out, "Let me look") || !strings.Contains(out, "Unable to reach") {
				t.Fatalf("the first reply isn't the broken one as it came: %s", out)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if n := len(r.Tries); n != 1 || r.Tries[0].Status != 200 || r.Tries[0].Fail == "" || r.Tries[0].Rest == nil {
				t.Fatalf("the broken try isn't told as failed: %+v", r.Tries)
			}
			sticks.Lock()
			kept := len(sticks.m)
			sticks.Unlock()
			if kept != 0 {
				t.Fatalf("the broken member is remembered as the conversation's answerer")
			}
			if out := post(); !strings.Contains(out, "from fine") {
				t.Fatalf("the retry: %s (tried %v)", out, tried)
			}
			if got := strings.Join(tried, ","); got != "broken,fine" {
				t.Fatalf("tried %s, want broken,fine", got)
			}
			if a := s.trace.routes[len(s.trace.routes)-1].Affinity; a == nil || a.Kept {
				t.Fatalf("the retry was kept on the broken member: %+v", a)
			}
		})
	}
}

// A conversation affine keeps on an account by the account alone — the
// model that answered it has left the group, and the widest of affine's
// matches still holds it there — loses its stick when that reply breaks
// off with nothing resting it (too long for its model, a refusal):
// unanswered forgot a stick only when the model answered matched too, so
// the agent's retry went straight back to the account whose reply had
// just broken off, which is what #733 is about.
func TestUnansweredForgetsTheAccountWhenItsModelIsGone(t *testing.T) {
	fresh(t)
	const key = "group/g|s1"
	flaky := candidate{p: provider.Provider{ID: "flaky", Key: "broken"}, rest: "flaky"}
	answered(key, candidate{p: provider.Provider{ID: "flaky", Key: "broken"}, model: "m", rest: "flaky"}, 1, 0)
	answered(key+"-other", candidate{p: provider.Provider{ID: "steady", Key: "fine"}, model: "m", rest: "steady"}, 1, 0)
	unanswered(key, candidate{p: flaky.p, model: "m2", rest: flaky.rest}) // the model that answered is gone from the group; the account isn't
	sticks.Lock()
	_, kept := sticks.m[key]
	_, other := sticks.m[key+"-other"]
	sticks.Unlock()
	if kept {
		t.Fatal("the conversation is still kept on the account whose model has left the group")
	}
	if !other {
		t.Fatal("another conversation's stick was forgotten")
	}
}
