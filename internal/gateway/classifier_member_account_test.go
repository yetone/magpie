package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// lunaBackend stands in for the ChatGPT backend, noting the account each
// request was sent on, and answers "1".
func lunaBackend(t *testing.T) func() []string {
	return lunaBackendRefusing(t, "")
}

// lunaBackendRefusing is lunaBackend answering 429 on the account refuse.
func lunaBackendRefusing(t *testing.T, refuse string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","visibility":"list"}]}`)
			return
		}
		mu.Lock()
		asked = append(asked, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if refuse != "" && r.Header.Get("chatgpt-account-id") == refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-luna"}}`,
			`data: {"type":"response.output_text.delta","delta":"1"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asked)
		asked = nil
		return out
	}
}

// A routing group's member pinned to one ChatGPT account (AWilsonPeng on
// X: GPT-6-luna on one of two accounts, picked as a group's intent
// classifier) is asked on that account alone when the group classifies a
// turn, never on the subscription's other account: either account, in a
// group of the user's or a found group they changed.
func TestClassifierGroupKeepsItsMembersAccount(t *testing.T) {
	for _, tc := range []struct {
		name, user, acct string
		found, held      bool
		routing          string
	}{
		{"own group, spare", "spare@example.com", "acct-2", false, false, provider.Ordered},
		{"own group, signed in", "me@example.com", "acct-1", false, false, provider.Ordered},
		{"found group, spare", "spare@example.com", "acct-2", true, false, provider.Ordered},
		{"found group, signed in", "me@example.com", "acct-1", true, false, provider.Ordered},
		{"found group, default routing", "spare@example.com", "acct-2", true, false, ""},
		{"found group, held", "spare@example.com", "acct-2", true, true, ""},
		{"found group, held, signed in", "me@example.com", "acct-1", true, true, ""},
		{"own group, held", "spare@example.com", "acct-2", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := lunaBackend(t)
			// as the Routing editor names it: the account's id, not its email
			tc.user = provider.LoginID("codex", tc.user)
			ref := "group/luna"
			if tc.found {
				other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("the relay was asked")
					w.WriteHeader(500)
				}))
				t.Cleanup(other.Close)
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: other.URL + "/v1", Key: "sk-r", Models: []string{"gpt-6-luna"}}); err != nil {
					t.Fatal(err)
				}
				id := provider.AutoGroupID("gpt-6-luna")
				g, _, ok := provider.FindGroup("group/" + id)
				if !ok {
					var ids []string
					for _, g := range provider.Groups() {
						ids = append(ids, g.ID)
					}
					t.Fatalf("no found group %s in %v", id, ids)
				}
				if tc.held {
					g.Members, g.Accounts = []string{"codex/gpt-6-luna"}, []string{"codex/" + tc.user}
				} else {
					g.Members = []string{"codex/gpt-6-luna#" + tc.user}
				}
				g.Routing = tc.routing
				if err := provider.SaveGroup(g); err != nil {
					t.Fatal(err)
				}
				ref = "group/" + id
			} else {
				g := provider.Group{Name: "Luna", Routing: tc.routing, Members: []string{"codex/gpt-6-luna#" + tc.user}}
				if tc.held {
					g.Members, g.Accounts = []string{"codex/gpt-6-luna"}, []string{"codex/" + tc.user}
				}
				if err := provider.SaveGroup(g); err != nil {
					t.Fatal(err)
				}
			}
			g, ms, ok := provider.FindGroup(ref)
			if !ok || len(ms) == 0 || ms[0].Account == "" && len(g.Accounts) == 0 {
				t.Fatalf("saved as %v %+v", g.Members, ms)
			}
			s := New()
			for i := range 4 {
				noRests()
				v, err := s.askClassifier(ref, []string{"feature", "bug"}, before{}, true, "fix this")
				if err != nil || v.Intent != "feature" {
					t.Fatalf("ask %d: %+v %v", i, v, err)
				}
				a := asked()
				if len(a) == 0 {
					t.Fatalf("ask %d: nothing reached the backend", i)
				}
				for _, a := range a {
					if a != tc.acct {
						t.Fatalf("ask %d: sent on %q, not the member's %s", i, a, tc.acct)
					}
				}
			}
		})
	}
}

// The pinned account refusing (its limit reached) never sends the
// classifier's question on the subscription's other account.
func TestClassifierGroupPinnedAccountRefusing(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := lunaBackendRefusing(t, "acct-2")
			g := provider.Group{Name: "Luna", Routing: provider.Ordered, Members: []string{"codex/gpt-6-luna#spare@example.com"}}
			if held {
				g.Members, g.Accounts = []string{"codex/gpt-6-luna"}, []string{"codex/spare@example.com"}
			}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := range 3 {
				v, err := s.askClassifier("group/luna", []string{"feature", "bug"}, before{}, true, "fix this")
				t.Logf("held=%v ask %d: %+v %v", held, i, v, err)
				for _, a := range asked() {
					if a != "acct-2" {
						t.Errorf("held=%v ask %d: sent on %q", held, i, a)
					}
				}
			}
		})
	}
}

// A turn through a ruled group whose classifier is the pinned group:
// every question asked of it on the member's account, across turns.
func TestClassifierGroupPinnedThroughATurn(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := lunaBackend(t)
			user := provider.LoginID("codex", "spare@example.com")
			b := &ruleUp{key: "kb"}
			srv := httptest.NewServer(b)
			t.Cleanup(srv.Close)
			if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"big"}, Chat: srv.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			g := provider.Group{Name: "Luna", Routing: provider.Ordered, Members: []string{"codex/gpt-6-luna#" + user}}
			if held {
				g.Members, g.Accounts = []string{"codex/gpt-6-luna"}, []string{"codex/" + user}
			}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"b/big", "codex/gpt-6-luna"}, Routing: provider.Ordered,
				Classifier: "group/luna", Effort: provider.EffortAuto, Rules: []provider.Rule{{Use: "b/big", Intent: "feature"}}}); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := range 4 {
				noRests()
				code, out := postAs(t, s, fmt.Sprint("s", i), `{"model":"group/r","reasoning_effort":"high","messages":[{"role":"user","content":"add feature `+fmt.Sprint(i)+`"}]}`)
				r := lastRoute(s)
				t.Logf("turn %d: %d %.80s %+v", i, code, out, r.Rule)
				a := asked()
				if len(a) == 0 {
					t.Fatalf("turn %d: the classifier wasn't asked", i)
				}
				for _, a := range a {
					if a != "acct-2" {
						t.Fatalf("turn %d: asked on %q", i, a)
					}
				}
			}
		})
	}
}

// A Manual group with a member for each account, one of them picked.
func TestClassifierGroupManualPickedAccount(t *testing.T) {
	for _, tc := range []struct {
		name, user, other, acct string
		found                   bool
	}{
		{"own, spare", "spare@example.com", "me@example.com", "acct-2", false},
		{"own, signed in", "me@example.com", "spare@example.com", "acct-1", false},
		{"found, spare", "spare@example.com", "me@example.com", "acct-2", true},
		{"found, signed in", "me@example.com", "spare@example.com", "acct-1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := lunaBackend(t)
			pin, other := "codex/gpt-6-luna#"+provider.LoginID("codex", tc.user), "codex/gpt-6-luna#"+provider.LoginID("codex", tc.other)
			ref := "group/luna"
			g := provider.Group{Name: "Luna"}
			if tc.found {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
				t.Cleanup(up.Close)
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "sk-r", Models: []string{"gpt-6-luna"}}); err != nil {
					t.Fatal(err)
				}
				id := provider.AutoGroupID("gpt-6-luna")
				var ok bool
				if g, _, ok = provider.FindGroup("group/" + id); !ok {
					t.Fatal("no found group")
				}
				ref = "group/" + id
			}
			g.Members, g.Routing, g.Pick = []string{other, pin, "relay/gpt-6-luna"}, provider.Manual, pin
			if !tc.found {
				g.Members = g.Members[:2]
			}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			g, ms, _ := provider.FindGroup(ref)
			t.Logf("%s pick %s members %v resolved %d", g.ID, g.Pick, g.Members, len(ms))
			s := New()
			for i := range 4 {
				noRests()
				v, err := s.askClassifier(ref, []string{"feature", "bug"}, before{}, true, fmt.Sprint("fix this ", i))
				if err != nil || v.Intent != "feature" {
					t.Fatalf("ask %d: %+v %v", i, v, err)
				}
				for _, a := range asked() {
					if a != tc.acct {
						t.Fatalf("ask %d: sent on %q, not the picked %s", i, a, tc.acct)
					}
				}
			}
		})
	}
}

func TestClassifierGroupPinnedEveryRouting(t *testing.T) {
	for _, routing := range []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed, provider.Pace, provider.Weighted, provider.FreeFirst, provider.Fastest, provider.Cheapest, provider.Drain, provider.FreeExpiring} {
		for _, held := range []bool{false, true} {
			t.Run(fmt.Sprint(routing, held), func(t *testing.T) {
				codexSignedIn(t, "spare@example.com")
				asked := lunaBackend(t)
				user := provider.LoginID("codex", "spare@example.com")
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("relay asked"); w.WriteHeader(500) }))
				t.Cleanup(up.Close)
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "sk-r", Models: []string{"gpt-6-luna"}}); err != nil {
					t.Fatal(err)
				}
				id := provider.AutoGroupID("gpt-6-luna")
				g, _, ok := provider.FindGroup("group/" + id)
				if !ok {
					t.Fatal("no found group")
				}
				g.Routing, g.Members = routing, []string{"codex/gpt-6-luna#" + user}
				if held {
					g.Members, g.Accounts = []string{"codex/gpt-6-luna"}, []string{"codex/" + user}
				}
				if err := provider.SaveGroup(g); err != nil {
					t.Fatal(err)
				}
				s := New()
				for i := range 6 {
					v, err := s.askClassifier("group/"+id, []string{"feature", "bug"}, before{}, true, fmt.Sprint("fix this ", i))
					if err != nil || v.Intent != "feature" {
						t.Fatalf("ask %d: %+v %v", i, v, err)
					}
					for _, a := range asked() {
						if a != "acct-2" {
							t.Fatalf("ask %d: sent on %q", i, a)
						}
					}
				}
			})
		}
	}
}
