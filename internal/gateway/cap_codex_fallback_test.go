package gateway

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// not_4O4 on X: the Codex app, signed in to its one ChatGPT account,
// should stop spending the subscription at a share of its windows and go
// to another vendor's model. That is the account's usage cap with a
// fallback set on the Codex subscription: past the cap, Codex's own model
// (asked bare, on Codex's sign-in) is served by the fallback, and nothing
// reaches ChatGPT; under it, the account answers as ever.
func TestCappedLoneCodexAccountGoesToItsFallback(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			codexSignedIn(t)
			b := newResetBackend(t)
			if err := provider.SetAccountCap("codex", "me@example.com", 80); err != nil {
				t.Fatal(err)
			}
			a := &scripted{replies: []reply{{200, "", chatSSEReply("OK")}, {200, "", chatSSEReply("OK")}}}
			scriptedOn(t, "a", provider.Chat, a)
			p, _ := provider.Find("codex")
			p.Fallback = []string{"a/m"}
			if err := provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			post := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(fmt.Sprintf(`{"model":"gpt-5.5","stream":%v,"input":"hi"}`, stream)))
				req.Header.Set("Authorization", "Bearer chatgpt-token")
				req.Header.Set("chatgpt-account-id", "acct-1")
				New().Handler().ServeHTTP(rec, req)
				return rec
			}
			capUsage(t, map[string]float64{"me@example.com": 85})
			rec := post()
			if tried, _ := b.seen(); rec.Code != 200 || tried != "" || a.n != 1 {
				t.Fatalf("past the cap: %d %s (codex %q, fallback %d)", rec.Code, rec.Body.String(), tried, a.n)
			}
			capUsage(t, map[string]float64{"me@example.com": 50})
			sticks.Lock()
			sticks.m = map[string]stick{}
			sticks.Unlock()
			rec = post()
			if tried, _ := b.seen(); rec.Code != 200 || tried != "acct-1" || a.n != 1 {
				t.Fatalf("under the cap: %d %s (codex %q, fallback %d)", rec.Code, rec.Body.String(), tried, a.n)
			}
		})
	}
}
