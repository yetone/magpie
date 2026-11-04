package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// riskBlocked answers as an account whose vendor's risk control blocked it,
// in magpie's words (provider.ZCodeRestrictedHint, as the ZCode plugin
// puts its 3012).
func riskBlocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	io.WriteString(w, `{"error":{"message":"blocked — `+provider.ZCodeRestrictedHint+`","type":"permission_error"}}`)
}

// A vendor's risk control reads the request as well as the account: one
// request sent on from account to account got each of them restricted in
// turn, until its tries ran out (_teamtaylor_ on Discord, WorkBuddy). An
// account its vendor restricted walls its provider off for the rest of the
// request: none of the provider's other accounts is sent it, and a group's
// other provider answers. The restricted account rests; the provider's
// other accounts don't, and answer the next request.
func TestRiskRefusalNotSentToTheProvidersOtherAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	var tried []string
	chatgptRefusing(t, &tried, riskBlocked)
	other := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "b", provider.Chat, other)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || other.n != 1 {
		t.Fatalf("%d %s; the other provider asked %d times; accounts asked %v", code, body, other.n, tried)
	}
	if strings.Join(tried, ",") != "acct-1" {
		t.Fatalf("the restricted request was sent on to %v", tried)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failRestricted || r.Tries[0].Rest == nil || r.Tries[0].Rest.By != "restricted" {
		t.Fatalf("tries %+v", r.Tries)
	}
	for _, who := range []string{"codex@b@example.com", "codex@c@example.com"} {
		if _, ok := restOf(who); ok {
			t.Fatalf("%s rests, never having been asked", who)
		}
	}
	// the next request goes to the provider's next account
	tried = nil
	if code, body := postAs(t, s, "", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`); code != 200 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("next request: %d %s; asked %v", code, body, tried)
	}
}

// With no other provider, the agent is told at once and the account
// rests, whether it was the only account or the provider had more; a
// refusal that isn't the vendor's risk control (a plain 403 alone) still
// goes on to the next account as before.
func TestRiskRefusalAloneIsToldAndRests(t *testing.T) {
	for _, x := range []struct {
		name   string
		spares []string
	}{{"lone account", nil}, {"two accounts", []string{"b@example.com"}}} {
		t.Run(x.name, func(t *testing.T) {
			codexSignedIn(t, x.spares...)
			if err := provider.SetRouting("codex", provider.Ordered); err != nil {
				t.Fatal(err)
			}
			var tried []string
			chatgptRefusing(t, &tried, riskBlocked)
			s := New()
			code, body := postAs(t, s, "", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
			if code != http.StatusForbidden || !strings.Contains(body, provider.ZCodeRestrictedHint) || strings.Join(tried, ",") != "acct-1" {
				t.Fatalf("%d %s; asked %v", code, body, tried)
			}
			r, ok := restOf("codex@me@example.com")
			if !ok || r.Why != failRestricted || r.By != "restricted" {
				t.Fatalf("the restricted account rests %+v (%v)", r, ok)
			}
		})
	}
	t.Run("a plain 403 goes on", func(t *testing.T) {
		codexSignedIn(t, "b@example.com")
		if err := provider.SetRouting("codex", provider.Ordered); err != nil {
			t.Fatal(err)
		}
		var tried []string
		chatgptRefusing(t, &tried, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"message":"Forbidden"}}`)
		})
		if code, body := postAs(t, New(), "", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`); code != 200 || strings.Join(tried, ",") != "acct-1,acct-2" {
			t.Fatalf("%d %s; asked %v", code, body, tried)
		}
	})
}
