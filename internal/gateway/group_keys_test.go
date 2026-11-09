package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group bound to gateway keys (Group.Keys, Xerxes754 on X) is
// those keys' alone: any other key, one held to models that names the
// group included, neither lists it nor reaches it on any route, and
// nothing is sent upstream for it. A bound key uses it, so does a key
// issued under one, and so does a keyless request from this computer,
// as key limits never hold one.
func TestGroupBoundToKeysIsTheirsAlone(t *testing.T) {
	fresh(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &fake{t: t, ctype: "application/json", reply: `{"id":"from-up","choices":[]}`}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Key: "k", Chat: srv.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Bound", "Other", "Held other")
	bound, other, heldOther := keys[0], secrets[1], secrets[2]
	if _, err := access.Update("models-key", access.Change{Key: keys[2].ID, Models: []string{"acme/m1", "group/team"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("issuer-key", access.Change{Key: bound.ID, Issues: true}); err != nil {
		t.Fatal(err)
	}
	issued, err := access.IssueKey(access.Issue{Name: "Under bound", TTL: time.Hour, Issuer: bound.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "team", Name: "Team", Members: []string{"acme/m1"}, Routing: provider.Ordered, Keys: []string{bound.ID}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	do := func(secret, path, body string) *httptest.ResponseRecorder {
		method := "POST"
		if body == "" {
			method = "GET"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		} else {
			r.RemoteAddr = "127.0.0.1:50000"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	listed := func(secret string) []string {
		var l struct{ Data []struct{ ID string } }
		w := do(secret, "/v1/models", "")
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	chat := func(secret, model string) *httptest.ResponseRecorder {
		return do(secret, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}

	for _, s := range []string{other, heldOther} {
		if ids := listed(s); slices.Contains(ids, "group/team") || !slices.Contains(ids, "acme/m1") {
			t.Fatalf("a key the group isn't bound to lists %v", ids)
		}
		if w := do(s, "/v1/models/group/team", ""); w.Code == 200 {
			t.Fatal("a key the group isn't bound to found it:", w.Body.String())
		}
		for _, model := range []string{"group/team", "Team"} {
			if w := chat(s, model); w.Code != 403 || !strings.Contains(w.Body.String(), "bound to other gateway keys") {
				t.Fatalf("chat %s: %d %s", model, w.Code, w.Body.String())
			}
			if w := do(s, "/v1/responses", `{"model":"`+model+`","input":"hi"}`); w.Code != 403 {
				t.Fatalf("responses %s: %d %s", model, w.Code, w.Body.String())
			}
			msg := `{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
			if w := do(s, "/v1/messages", msg); w.Code != 403 {
				t.Fatalf("messages %s: %d %s", model, w.Code, w.Body.String())
			}
			if w := do(s, "/v1/messages/count_tokens", msg); w.Code != 403 {
				t.Fatalf("count_tokens %s: %d %s", model, w.Code, w.Body.String())
			}
		}
		// the provider's own model is still the key's
		if w := chat(s, "acme/m1"); w.Code != 200 {
			t.Fatalf("a model the key may use: %d %s", w.Code, w.Body.String())
		}
	}
	if up.calls != 2 {
		t.Fatalf("%d calls reached upstream, want the 2 for acme/m1", up.calls)
	}

	for name, s := range map[string]string{"the bound key": secrets[0], "a key issued under it": issued.Key, "no key, from this computer": ""} {
		if ids := listed(s); !slices.Contains(ids, "group/team") {
			t.Fatalf("%s lists %v", name, ids)
		}
		if w := do(s, "/v1/models/group/team", ""); w.Code != 200 {
			t.Fatalf("%s: /v1/models/group/team %d %s", name, w.Code, w.Body.String())
		}
		if w := chat(s, "group/team"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-up") {
			t.Fatalf("%s: chat %d %s", name, w.Code, w.Body.String())
		}
	}

	// unbound again, every key has it as before
	g, _, _ := provider.FindGroup("group/team")
	g.Keys = []string{}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if ids := listed(other); !slices.Contains(ids, "group/team") {
		t.Fatalf("an unbound group is missing for another key: %v", ids)
	}
	if w := chat(other, "group/team"); w.Code != 200 {
		t.Fatalf("an unbound group: %d %s", w.Code, w.Body.String())
	}
}

// A save that says nothing of the keys (nil, as the command line and the
// Routing page's card toggles save) keeps them; an empty list clears
// them. Sharing a group leaves its keys out, and importing it over the
// same group keeps the ones already there.
func TestGroupKeysKeptBySavesThatDontNameThem(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "team", Name: "Team", Members: []string{"acme/m1"}, Routing: provider.Ordered, Keys: []string{" k1 ", "k1", "", "k2"}}); err != nil {
		t.Fatal(err)
	}
	keysOf := func() []string {
		g, _, ok := provider.FindGroup("group/team")
		if !ok {
			t.Fatal("group gone")
		}
		return g.Keys
	}
	if got := keysOf(); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("saved keys %v", got)
	}
	if !provider.GroupsBound() {
		t.Fatal("GroupsBound with a bound group")
	}
	g, _, _ := provider.FindGroup("group/team")
	g.Keys = nil
	g.Name = "Team 2"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("a save without keys left %v", got)
	}
	shared, err := provider.ShareGroup("team")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shared, "k1") {
		t.Fatal("a shared group carries its keys:", shared)
	}
	if _, err := provider.ImportGroupOver(shared); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("importing over the group left %v", got)
	}
	g, _, _ = provider.FindGroup("group/team")
	g.Keys = []string{}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(); len(got) != 0 || provider.GroupsBound() {
		t.Fatalf("cleared keys left %v", got)
	}
}
