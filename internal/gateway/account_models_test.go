package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A key set to serve some of its provider's models only is never a
// candidate for another, whatever the routing; it is one for those, and a
// key without a list of its own is one for every model, as before (#474).
func TestKeyModelsNarrowCandidates(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed} {
		p := provider.Provider{ID: "narrow", Name: "Narrow", Chat: "https://example.invalid/v1", Key: "first", Keys: []provider.KeyAccount{{Key: "second"}, {Key: "third"}}, Routing: mode}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		keysFor := func(model string) []string {
			got, err := provider.Find(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, c := range perKey(*got, model, provider.Chat) {
				keys = append(keys, c.p.Key)
			}
			return keys
		}
		// none set: every key for every model, as before
		if got := keysFor("big"); !reflect.DeepEqual(got, []string{"first", "second", "third"}) {
			t.Fatalf("mode=%q unset: %v", mode, got)
		}
		if err := provider.SetAccountModels(p.ID, provider.KeyID("first"), []string{"small"}); err != nil {
			t.Fatal(err)
		}
		if got := keysFor("big"); !reflect.DeepEqual(got, []string{"second", "third"}) {
			t.Fatalf("mode=%q big: %v", mode, got)
		}
		if got := keysFor("small"); !reflect.DeepEqual(got, []string{"first", "second", "third"}) {
			t.Fatalf("mode=%q small: %v", mode, got)
		}
		// every key barred from big: none, never the provider's key all the same
		for _, k := range []string{"second", "third"} {
			if err := provider.SetAccountModels(p.ID, provider.KeyID(k), []string{"small"}); err != nil {
				t.Fatal(err)
			}
		}
		if got := keysFor("big"); len(got) != 0 {
			t.Fatalf("mode=%q all barred: %v", mode, got)
		}
		// all again: as before
		for _, k := range []string{"first", "second", "third"} {
			if err := provider.SetAccountModels(p.ID, provider.KeyID(k), nil); err != nil {
				t.Fatal(err)
			}
		}
		if got := keysFor("big"); !reflect.DeepEqual(got, []string{"first", "second", "third"}) {
			t.Fatalf("mode=%q reset: %v", mode, got)
		}
		if got, _ := provider.Find(p.ID); got.AccountModels != nil {
			t.Fatalf("mode=%q reset kept %v", mode, got.AccountModels)
		}
	}
}

// An account of a subscription set to serve other models only is never
// sent a request for this one: every request goes to the other account,
// the trace telling it left out; with every account barred the request is
// refused with why, not sent to one all the same; unset, both serve it
// again (#474).
func TestAccountModelsNeverChosen(t *testing.T) {
	heads := twoAccounts(t)
	s := New()
	post := func() (int, string) { return pinnedPost(t, s, "/v1/responses", "codex/gpt-5.5", "") }
	if err := provider.SetAccountModels("codex", "Me@example.com", []string{"gpt-5.4-mini"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed} {
		if err := provider.SetRouting("codex", mode); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			*heads = nil
			code, body := post()
			if code != 200 || !strings.Contains(body, "from acct-2") {
				t.Fatalf("mode=%q: %d %s", mode, code, body)
			}
			if len(*heads) != 1 || (*heads)[0].Get("chatgpt-account-id") != "acct-2" {
				t.Fatalf("mode=%q: upstream %v", mode, *heads)
			}
		}
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Order) != 1 || r.Order[0].Who != "spare@example.com" || len(r.Left) != 1 || !r.Left[0].Barred || r.Left[0].Who != "me@example.com" {
		t.Fatalf("trace order %+v left %+v", r.Order, r.Left)
	}
	// the model it is set to serve still goes to it first
	*heads = nil
	if code, body := pinnedPost(t, s, "/v1/responses", "codex/gpt-5.4-mini", ""); code != 200 || len(*heads) != 1 {
		t.Fatalf("its own model: %d %s", code, body)
	}
	if err := provider.SetAccountModels("codex", "spare@example.com", []string{"gpt-5.4-mini"}); err != nil {
		t.Fatal(err)
	}
	*heads = nil
	code, body := post()
	if code != 403 || len(*heads) != 0 || !strings.Contains(body, "gpt-5.5") || !strings.Contains(body, "own list of models") {
		t.Fatalf("all barred: %d %s, upstream %v", code, body, *heads)
	}
	var e struct {
		Error struct{ Message string }
	}
	if json.Unmarshal([]byte(body), &e) != nil || e.Error.Message == "" {
		t.Fatalf("all barred, not an error the client reads: %s", body)
	}
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		if err := provider.SetAccountModels("codex", u, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetRouting("codex", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		*heads = nil
		if code, body := post(); code != 200 || len(*heads) != 1 {
			t.Fatalf("unset: %d %s", code, body)
		}
		seen[(*heads)[0].Get("chatgpt-account-id")] = true
	}
	if !seen["acct-1"] || !seen["acct-2"] {
		t.Fatalf("unset, rotating, both serve it again: %v", seen)
	}
}
