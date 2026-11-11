package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// JackyFill on X: a gateway key carries a service tier. Its requests go at
// that tier whatever the client asked (and over Codex's own setting), a key
// set to "follow" or another key goes as asked, standard drops the tier,
// and Ultrafast on a model no account lists it on goes as priority.
func TestGatewayKeyServiceTier(t *testing.T) {
	tried := tierAccounts(t, nil, nil)
	s := New()
	cfg := settings.Load()
	cfg.LAN = true
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "Tiered"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := access.Update("add-key", access.Change{Name: "Plain"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	var id string
	for _, k := range keys {
		if k.Name == "Tiered" {
			id = k.ID
		}
	}
	send := func(key, path, model, asked string) string {
		t.Helper()
		b := `{"model":"` + model + `","stream":true,"input":"ping","messages":[{"role":"user","content":"ping"}]`
		if asked != "" {
			b += `,"service_tier":"` + asked + `"`
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(b+"}"))
		req.Header.Set("Authorization", "Bearer "+key)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		got := tried()
		if len(got) == 0 {
			t.Fatal("nothing was asked of the backend")
		}
		return got[len(got)-1]
	}
	// (a Chat request to a Codex model reaches the fake backend on a host that
	// is not chatgpt.com, which the translation sends no tier to: TestKeyTieredChat)
	for _, path := range []string{"/v1/responses"} {
		for _, c := range []struct{ set, asked, want string }{
			{"", "priority", "acct-plus:priority"},
			{"", "", "acct-plus:"},
			{"fast", "", "acct-plus:priority"},
			{"fast", "ultrafast", "acct-plus:priority"},
			{"standard", "priority", "acct-plus:"},
			{"ultrafast", "", "acct-pro:ultrafast"},
		} {
			if _, err := access.Update("tier-key", access.Change{Key: id, ServiceTier: c.set}); err != nil {
				t.Fatal(err)
			}
			if got := send(secret, path, "codex/gpt-6-luna", c.asked); got != c.want {
				t.Errorf("%s, key %q, asked %q: tried %s, want %s", path, c.set, c.asked, got, c.want)
			}
			// another key is never touched by it
			if got := send(other, path, "codex/gpt-6-luna", c.asked); got != map[string]string{"": "acct-plus:", "priority": "acct-plus:priority", "ultrafast": "acct-pro:ultrafast"}[c.asked] {
				t.Errorf("%s, other key, asked %q: tried %s", path, c.asked, got)
			}
		}
	}
}

// A Chat request carries the key's tier the same way: the body the route
// goes on with has it, replaced, added or taken away.
func TestKeyTieredChat(t *testing.T) {
	for _, c := range []struct{ set, body, want string }{
		{"", `{"model":"gpt-5","service_tier":"priority"}`, `{"model":"gpt-5","service_tier":"priority"}`},
		{"priority", `{"model":"gpt-5"}`, `{"service_tier":"priority","model":"gpt-5"}`},
		{"priority", `{"model":"gpt-5","service_tier":"flex"}`, `{"service_tier":"priority","model":"gpt-5"}`},
		{"default", `{"model":"gpt-5","service_tier":"priority"}`, `{"model":"gpt-5"}`},
		{"ultrafast", `{"model":"gpt-5"}`, `{"service_tier":"priority","model":"gpt-5"}`},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: "k", ServiceTier: c.set}))
		if got := string(keyTiered(r, []byte(c.body), "gpt-5")); got != c.want {
			t.Errorf("key %q on %s: %s, want %s", c.set, c.body, got, c.want)
		}
	}
}
