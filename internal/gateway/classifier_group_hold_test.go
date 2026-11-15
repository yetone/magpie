package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A group held to one ChatGPT account whose classifier is a model, not a
// group (AWilsonPeng on X, second round: the found group GPT-6 Luna held
// to one of two Codex accounts, its 意图判断模型 GPT-6-Luna · Codex — its
// own member, as a group can't classify with itself): the classifier's
// questions are the group's spend too, so they go on the group's account
// alone, as the turn does, never on the subscription's other account.
// Held by the group's Accounts, or by the member naming its account.
func TestClassifierModelHeldToItsGroupsAccount(t *testing.T) {
	for _, held := range []bool{true, false} {
		t.Run(fmt.Sprint("held=", held), func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := lunaBackend(t)
			user := provider.LoginID("codex", "spare@example.com")
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("the relay, switched off in the group, was asked")
				w.WriteHeader(500)
			}))
			t.Cleanup(relay.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: relay.URL + "/v1", Key: "sk-r", Models: []string{"gpt-6-luna"}}); err != nil {
				t.Fatal(err)
			}
			id := provider.AutoGroupID("gpt-6-luna")
			g, _, ok := provider.FindGroup("group/" + id)
			if !ok {
				t.Fatal("no found group")
			}
			g.Members, g.Off, g.Accounts = []string{"relay/gpt-6-luna", "codex/gpt-6-luna"}, []string{"relay/gpt-6-luna"}, []string{"codex/" + user}
			if !held {
				g.Members, g.Off, g.Accounts = []string{"relay/gpt-6-luna", "codex/gpt-6-luna#" + user}, []string{"relay/gpt-6-luna"}, nil
			}
			g.Classifier = "codex/gpt-6-luna"
			g.Rules = []provider.Rule{{Use: g.Members[1], Intent: "feature"}}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := range 3 {
				noRests()
				code, out := postAs(t, s, fmt.Sprint("s", i), `{"model":"group/`+id+`","messages":[{"role":"user","content":"add feature `+fmt.Sprint(i)+`"}]}`)
				r := lastRoute(s)
				if code != 200 {
					t.Fatalf("turn %d: %d %s", i, code, out)
				}
				if r.Rule == nil || r.Rule.Classified == nil || r.Rule.Classified.Error != "" {
					t.Fatalf("turn %d: the classifier didn't answer: %+v", i, r.Rule)
				}
				a := asked()
				if len(a) < 2 {
					t.Fatalf("turn %d: %d requests reached the backend, not the classifier's and the turn's", i, len(a))
				}
				for _, a := range a {
					if a != "acct-2" {
						t.Fatalf("turn %d: sent on %q, not the group's acct-2", i, a)
					}
				}
			}
		})
	}
}

// A gateway key held to one account holds what the classifier is asked
// for its turns too, as it holds magpie's other calls for the key (a web
// search, a title): a ruled group whose classifier is a Codex model asks
// it on the key's account, as the turn itself is sent.
func TestClassifierModelHeldToTheKeysAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := lunaBackend(t)
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"codex/gpt-6-luna", "codex/gpt-6-luna:high"}, Routing: provider.Ordered,
		Classifier: "codex/gpt-6-luna", Rules: []provider.Rule{{Use: "codex/gpt-6-luna:high", Intent: "feature"}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		noRests()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/r","messages":[{"role":"user","content":"add feature `+fmt.Sprint(i)+`"}]}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		req.Header.Set("x-session-id", fmt.Sprint("k", i))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body.String())
		}
		a := asked()
		if len(a) < 2 {
			t.Fatalf("turn %d: %d requests reached the backend, not the classifier's and the turn's", i, len(a))
		}
		for _, a := range a {
			if a != "acct-2" {
				t.Fatalf("turn %d: the classifier was asked on %q, not the key's acct-2", i, a)
			}
		}
	}
}
