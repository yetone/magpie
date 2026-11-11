package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The Gateway page lists the models a key can be held to, the routing
// groups first (Magic_zero on Discord), and keeps what was picked for it
// (#882).
func TestCallerKeyModelsRoutes(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "m-mini"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"relay/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	var l struct{ Models []keyModelJSON }
	if w := request("GET", "/api/caller-keys/models", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids []string
	for _, m := range l.Models {
		ids = append(ids, m.ID)
		if m.Group {
			if m.ID != "group/mine" || m.Name != "Mine" || m.Provider != "" {
				t.Fatalf("group %+v", m)
			}
			continue
		}
		if m.Provider != "relay" || m.ProviderName != "Relay" || m.Name == "" {
			t.Fatalf("%+v", m)
		}
	}
	if !slices.Equal(ids, []string{"group/mine", "relay/m", "relay/m-mini"}) {
		t.Fatal("models", ids)
	}
	var s struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/add-key", `{"name":"Phone"}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := s.Keys[0].ID
	if w := request("POST", "/api/caller-keys/models-key", `{"key":"`+id+`","models":["relay/*","openai/gpt-5","group/mine"]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if !slices.Equal(s.Keys[0].Models, []string{"relay/*", "openai/gpt-5", "group/mine"}) {
		t.Fatal("kept", s.Keys[0].Models)
	}
	if w := request("POST", "/api/caller-keys/models-key", `{"key":"`+id+`","models":["gpt-5"]}`); w.Code == 200 {
		t.Fatal("accepted a model without its provider")
	}
	var free struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/models-key", `{"key":"`+id+`","models":[]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &free) != nil || free.Keys[0].Models != nil {
		t.Fatal("every model", w.Code, w.Body)
	}
}

// The Gateway page lists the accounts and keys a key can be held to
// (#905), and keeps what was picked for it. An account's stable id, shown
// by who is signed in, is exercised against a real sign-in in the
// gateway's tests; a key's fingerprint is one here.
func TestCallerKeyAccountsRoutes(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1", Name: "First"}, {Key: "sk-2", Name: "Second"}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	k1, k2 := provider.KeyID("sk-1"), provider.KeyID("sk-2")
	var l struct{ Accounts []keyModelJSON }
	if w := request("GET", "/api/caller-keys/models", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids []string
	for i, a := range l.Accounts {
		ids = append(ids, a.ID)
		if !a.Key || a.Account || a.Provider != "relay" || a.ProviderName != "Relay" || a.Name != []string{"First", "Second"}[i] {
			t.Fatalf("key %+v", a)
		}
	}
	if !slices.Equal(ids, []string{"relay/" + k1, "relay/" + k2}) {
		t.Fatal("accounts", ids)
	}
	var s struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/add-key", `{"name":"Phone"}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := s.Keys[0].ID
	if w := request("POST", "/api/caller-keys/accounts-key", `{"key":"`+id+`","accounts":["relay/First","relay/`+k2+`"]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if !slices.Equal(s.Keys[0].Accounts, []string{"relay/" + k1, "relay/" + k2}) {
		t.Fatal("kept", s.Keys[0].Accounts)
	}
	if w := request("POST", "/api/caller-keys/accounts-key", `{"key":"`+id+`","accounts":["relay/`+provider.KeyID("sk-9")+`"]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "has no key") {
		t.Fatal("accepted a key the provider has not", w.Code, w.Body.String())
	}
	var free struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/accounts-key", `{"key":"`+id+`","accounts":[]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &free) != nil || free.Keys[0].Accounts != nil {
		t.Fatal("every account", w.Code, w.Body)
	}
}

// The key editor offers the decision models (Kautism on Discord: Clef and
// Jev listed nowhere for a key held to two groups), each after its own
// provider's chat models and marked as one, so a key held to some models
// can be let ask one: the gateway holds a key to them as to any model.
func TestCallerKeyModelsOfferDeciders(t *testing.T) {
	sandboxHome(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}},
		{ID: "ts", Name: "TypeSafe", Key: "k", Decide: "http://127.0.0.1:1/v1"},
		{ID: "cf", Name: "Cloudflare", Key: "k", Decide: "https://api.cloudflare.com/client/v4/accounts/test/ai", Models: []string{provider.CloudflareJev, provider.CloudflareClefModel}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/models", nil))
	var l struct{ Models []keyModelJSON }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids, deciders []string
	for _, m := range l.Models {
		ids = append(ids, m.ID)
		if m.Decides {
			deciders = append(deciders, m.ID)
			if m.Provider == "" || m.ProviderName == "" || m.Name == "" {
				t.Fatalf("decider %+v", m)
			}
		}
	}
	for _, id := range []string{"ts/" + provider.JevLatest, "cf/" + provider.CloudflareJev, "cf/" + provider.CloudflareClefModel} {
		if !slices.Contains(deciders, id) {
			t.Fatalf("%s not offered as a decision model: %v", id, ids)
		}
	}
	if slices.Contains(deciders, "relay/m") {
		t.Fatal("a chat model marked a decision model", ids)
	}
	// a provider's models stay together
	seen := map[string]bool{}
	last := ""
	for _, m := range l.Models {
		if m.Provider != last && seen[m.Provider] {
			t.Fatalf("%s's models split: %v", m.Provider, ids)
		}
		seen[m.Provider], last = true, m.Provider
	}
}

// The key editor offers the image and video models too (aipha2025 on X:
// "can't pick an image model for a key"): the gateway holds a key to them
// on /v1/images and /v1/videos as to a chat model, so a key held to some
// models had no way to be let draw but All models.
func TestCallerKeyModelsOfferImageAndVideoModels(t *testing.T) {
	sandboxHome(t)
	live := []catalog.Model{{ID: "gpt-5.5"}, {ID: "gpt-image-2", Draws: true}, {ID: "gpt-image-1", Draws: true}}
	p := provider.Provider{ID: "pix", Name: "Pix", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}, HiddenDrawers: []string{"gpt-image-1"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/models", nil))
	var l struct{ Models []keyModelJSON }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids []string
	for _, m := range l.Models {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"pix/gpt-5.5", "pix/gpt-image-2", "pix/gpt-image-1"} {
		if n := strings.Count(" "+strings.Join(ids, " ")+" ", " "+want+" "); n != 1 {
			t.Fatalf("%s offered %d times: %v", want, n, ids)
		}
	}
	seen, last := map[string]bool{}, ""
	for _, m := range l.Models {
		if m.Provider != last && seen[m.Provider] {
			t.Fatalf("%s's models split: %v", m.Provider, ids)
		}
		seen[m.Provider], last = true, m.Provider
	}
}

// A provider that serves no model now can still be allowed whole,
// <provider>/*, for the models it serves later (CoderCCz on X): the
// models list names it in "providers".
func TestCallerKeyModelsOfferAnEmptyProviderWhole(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/models", nil))
	var l struct{ Providers []keyModelJSON }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids []string
	for _, p := range l.Providers {
		ids = append(ids, p.ID)
	}
	if !slices.Contains(ids, "later/*") || slices.Contains(ids, "relay/*") {
		t.Fatal("providers", ids)
	}
}
