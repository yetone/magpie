package gateway

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Discord (v0.1.3354): a subscription account at its usage cap in a
// routing group with two APIs; the group's next member takes the request.
func TestCappedSubscriptionGivesWayInGroup(t *testing.T) {
	for _, routing := range []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed, provider.Pace, provider.Weighted, provider.FreeFirst, provider.Fastest, provider.Cheapest, provider.Drain, provider.FreeExpiring} {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", CodexPath + "/responses"} {
			t.Run(routing+path, func(t *testing.T) {
				codexSignedIn(t)
				b := newResetBackend(t)
				capUsage(t, map[string]float64{"me@example.com": 75})
				if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
					t.Fatal(err)
				}
				a := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				c := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				scriptedOn(t, "a", provider.Chat, a)
				scriptedOn(t, "c", provider.Chat, c)
				if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "a/m", "c/m"}, Routing: routing}); err != nil {
					t.Fatal(err)
				}
				s := New()
				var code int
				var body string
				switch {
				case strings.HasPrefix(path, CodexPath):
					rec := codexOn(s, path, `{"model":"group/g","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
					code, body = rec.Code, rec.Body.String()
				case path == "/v1/responses":
					code, body = postTo(t, s, path, `{"model":"group/g","input":"hi"}`)
				case path == "/v1/messages":
					code, body = postTo(t, s, path, `{"model":"group/g","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)
				default:
					code, body = postTo(t, s, path, `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
				}
				tried, _ := b.seen()
				if code != 200 || tried != "" || a.n+c.n != 1 {
					t.Fatalf("%d %s (codex %q, a %d, c %d)", code, body, tried, a.n, c.n)
				}
			})
		}
	}
}

// The same, with the account crossing its cap during a conversation: the
// turn after the reading says it is past the cap goes to the group's next
// member, as the conversation's own account is used up.
func TestSubscriptionCappedMidConversationGivesWay(t *testing.T) {
	for _, routing := range []string{"", provider.Ordered} {
		for _, used := range []float64{50, 68} {
			t.Run(fmt.Sprintf("%s-%v", routing, used), func(t *testing.T) {
				codexSignedIn(t)
				b := newResetBackend(t)
				now := map[string]float64{"me@example.com": used}
				old := allowances
				t.Cleanup(func() { allowances = old })
				reset := time.Now().Add(3 * time.Hour)
				var mu sync.Mutex
				allowances = func(string) map[string]provider.Allowance {
					mu.Lock()
					defer mu.Unlock()
					out := map[string]provider.Allowance{}
					for u, n := range now {
						out[u] = provider.Allowance{{Used: n, Resets: reset, Span: 5 * time.Hour}}
					}
					return out
				}
				if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
					t.Fatal(err)
				}
				a := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				c := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				scriptedOn(t, "a", provider.Chat, a)
				scriptedOn(t, "c", provider.Chat, c)
				if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "a/m", "c/m"}, Routing: routing}); err != nil {
					t.Fatal(err)
				}
				s := New()
				code, body := postTo(t, s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
				if tried, _ := b.seen(); code != 200 || tried == "" {
					t.Fatalf("first turn: %d %s (codex %q)", code, body, tried)
				}
				mu.Lock()
				now["me@example.com"] = 75
				mu.Unlock()
				code, body = postTo(t, s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"OK"},{"role":"user","content":"again"}]}`)
				if code != 200 || a.n+c.n != 1 {
					tried, _ := b.seen()
					t.Fatalf("second turn: %d %s (codex %q, a %d, c %d)", code, body, tried, a.n, c.n)
				}
			})
		}
	}
}

// The reporter's case: the Codex app, signed in to ChatGPT, sends Codex's
// own model names, never the group's. With the account past its cap, a
// routing group the user built with that account first goes on to its
// next member, on both of magpie's Codex paths, rather than answering
// "usage cap reached".
func TestCappedCodexOwnModelGoesToItsGroup(t *testing.T) {
	for _, c := range []struct{ path, model string }{
		{CodexPath + "/responses", "gpt-5.5"},
		{"/v1/responses", "codex/gpt-5.5"},
	} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s-%s-%v", c.path, c.model, stream), func(t *testing.T) {
				codexSignedIn(t)
				b := newResetBackend(t)
				capUsage(t, map[string]float64{"me@example.com": 75})
				if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
					t.Fatal(err)
				}
				a := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				c2 := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}}}
				scriptedOn(t, "a", provider.Chat, a)
				scriptedOn(t, "c", provider.Chat, c2)
				if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "a/m", "c/m"}}); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", c.path, strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":%v,"input":"hi"}`, c.model, stream)))
				req.Header.Set("Authorization", "Bearer chatgpt-token")
				req.Header.Set("chatgpt-account-id", "acct-1")
				New().Handler().ServeHTTP(rec, req)
				tried, _ := b.seen()
				if rec.Code != 200 || tried != "" || a.n != 1 || c2.n != 0 {
					t.Fatalf("%d %s (codex %q, a %d, c %d)", rec.Code, rec.Body.String(), tried, a.n, c2.n)
				}
			})
		}
	}
}
