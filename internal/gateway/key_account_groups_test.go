package gateway

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// LeiWang_2000 on X: a gateway key held to an account group ("@<id>"),
// or keeping one out ("!@<id>"), asks only the accounts the group holds
// now, or every other, on every route that spends an account; a group
// gone holds the key to nothing.
func TestGatewayKeyAccountGroupsKeyedRoutes(t *testing.T) {
	fresh(t)
	up := newKeptOutUp(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Anthropic: up.URL, Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"m", "gpt-image-1"}},
		{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: up.URL + "/v1", Key: "oA", Keys: []provider.KeyAccount{{Key: "oB"}}, Models: []string{"grok-imagine-video"}},
		{ID: "jev", Name: "Jev", Decide: up.URL + "/decide/v1", Key: "jA", Keys: []provider.KeyAccount{{Key: "jB"}}, Models: []string{"jev-latest"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.SaveLive("relay", up.URL+"/v1", []catalog.Model{{ID: "m"}, {ID: "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	group := func(ks ...string) []string {
		var out []string
		for _, k := range ks {
			out = append(out, map[byte]string{'k': "relay", 'o': "office", 'j': "jev"}[k[0]]+"/"+provider.KeyID(k))
		}
		return out
	}
	g, err := access.SaveAccountGroup(access.AccountGroup{Name: "Work", Accounts: group("kB", "oB", "jB")})
	if err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held", "Out")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"@Work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("accounts-key", access.Change{Key: keys[1].ID, Accounts: []string{"!@Work"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(secret, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	routes := []struct {
		name, path, body string
		local            bool
	}{
		{"chat", "/v1/chat/completions", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, false},
		{"messages", "/v1/messages", `{"model":"relay/m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"responses", "/v1/responses", `{"model":"relay/m","input":"hi"}`, false},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, true},
		{"images", "/v1/images/generations", `{"model":"relay/gpt-image-1","prompt":"a magpie"}`, false},
		{"videos", "/v1/videos", `{"model":"office/grok-imagine-video","prompt":"a magpie"}`, false},
		{"systemone", "/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`, false},
	}
	// served by the one account of each provider want names
	served := func(who, secret string, want map[string]string) {
		t.Helper()
		for _, c := range routes {
			w := ask(secret, "POST", c.path, c.body)
			if w.Code != 200 {
				t.Fatalf("%s %s: %d %s", who, c.name, w.Code, w.Body.String())
			}
			k := want[map[string]string{"videos": "office", "systemone": "jev"}[c.name]]
			if k == "" {
				k = want["relay"]
			}
			if got := up.take(); !slices.Equal(got, []string{k}) {
				t.Fatalf("%s %s: asked with %v, want %s alone", who, c.name, got, k)
			}
		}
	}
	// the default order asks kA, oA, jA first: the group's B accounts
	// serve the key held to it, the A ones the key keeping it out
	served("held", secrets[0], map[string]string{"relay": "kB", "office": "oB", "jev": "jB"})
	served("out", secrets[1], map[string]string{"relay": "kA", "office": "oA", "jev": "jA"})

	// the group changed: both keys follow it on the next request
	g.Accounts = group("kA", "oA", "jA")
	if _, err := access.SaveAccountGroup(g); err != nil {
		t.Fatal(err)
	}
	served("held, changed", secrets[0], map[string]string{"relay": "kA", "office": "oA", "jev": "jA"})
	served("out, changed", secrets[1], map[string]string{"relay": "kB", "office": "oB", "jev": "jB"})

	// the group gone: neither key asks any account or lists a model
	if err := access.RestoreGroups(nil); err != nil {
		t.Fatal(err)
	}
	for i, secret := range secrets {
		for _, c := range routes {
			w := ask(secret, "POST", c.path, c.body)
			if got := up.take(); len(got) != 0 {
				t.Fatalf("key %d %s: asked %v with its group gone (%d %s)", i, c.name, got, w.Code, w.Body.String())
			}
			if !c.local && w.Code == 200 {
				t.Fatalf("key %d %s: served with its group gone: %s", i, c.name, w.Body.String())
			}
		}
		if w := ask(secret, "GET", "/v1/models", ""); strings.Contains(w.Body.String(), `"relay/m"`) {
			t.Fatalf("key %d: /v1/models lists relay with its group gone", i)
		}
	}
}
