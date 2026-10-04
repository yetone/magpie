package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A conversation too long for one member's model is as long on every
// account of it, but not too long for another member, a model or vendor
// with a larger window (#700: Kimi Code on DeepSeek V4.1 Flash through
// magpie, told the context was 176k — 0.85 of a request one member called
// too long — where before the overflow went to the next member, which
// answered). The next member is asked; the agent is told it overflowed
// only when no member left may hold it.
func TestOverflowGoesToAMemberWithRoom(t *testing.T) {
	fresh(t)
	over := `{"error":{"message":"This model's maximum context length is 131072 tokens. However, you requested 207000 tokens","code":"context_length_exceeded"}}`
	a := &scripted{replies: []reply{{400, "", over}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("group: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failOverflow || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}

	// a member whose window the request is known to be past isn't asked
	big := strings.Repeat("word ", 2400) // about 3000 tokens as estimated
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}, Contexts: map[string]int{"m": 2000}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Small", Members: []string{"a/m", "c/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	a.n, b.n = 0, 0
	code, body = postAs(t, s, "", `{"model":"group/small","messages":[{"role":"user","content":"`+big+`"}]}`)
	if code != 400 || !strings.Contains(body, "context_length_exceeded") || a.n != 1 || b.n != 0 {
		t.Fatalf("no room: %d %s (a %d, c %d)", code, body, a.n, b.n)
	}

	// another account of the same model holds it no better: not asked
	up2 := httptest.NewServer(a)
	t.Cleanup(up2.Close)
	if err := provider.Save(provider.Provider{ID: "keys", Name: "Keys", Key: "first", Keys: []provider.KeyAccount{{Key: "second"}}, Chat: up2.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	a.n = 0
	code, body = postAs(t, s, "", `{"model":"keys/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "context_length_exceeded") || a.n != 1 {
		t.Fatalf("accounts: %d %s (asked %d)", code, body, a.n)
	}
}
