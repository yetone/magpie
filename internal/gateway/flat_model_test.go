package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// #1387 (worksyang): MiniMax Code names a model under magpie with one
// slash in the whole, so magpie writes "bb-codex/gpt-6.1-sol" there as
// "bb-codex~gpt-6.1-sol". The gateway takes that to the one provider's
// model it stands for — not the first provider serving gpt-6.1-sol, as a
// bare name is — on every route, a gateway key's models holding it as
// they hold the id.
func TestFlatModelIDsReachTheirProvider(t *testing.T) {
	fresh(t)
	reply := func(id string) *fake {
		return &fake{t: t, ctype: "application/json", reply: `{"id":"` + id + `","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
	}
	codex, other := reply("from-codex"), reply("from-other")
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "bb-codex", Name: "BB Codex", Key: "k", Models: []string{"gpt-6.1-sol"}}, codex},
		{provider.Provider{ID: "other", Name: "Other", Key: "k", Models: []string{"gpt-6.1-sol", "glm-5.3"}}, other},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"bb-codex/*"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	do := func(secret, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("User-Agent", "minimax-code")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	messages := func(secret, model string) *httptest.ResponseRecorder {
		return do(secret, "POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	sentModel := func(f *fake) string {
		var b struct{ Model string }
		json.Unmarshal(f.got, &b)
		return b.Model
	}

	// "other~gpt-6.1-sol" is other's, though bb-codex comes first by name
	if w := messages(secrets[1], "other~gpt-6.1-sol"); w.Code != 200 || other.calls != 1 || codex.calls != 0 || sentModel(other) != "gpt-6.1-sol" {
		t.Fatalf("other~gpt-6.1-sol: %d %s (codex %d, other %d, sent %q)", w.Code, w.Body.String(), codex.calls, other.calls, sentModel(other))
	}
	if rec := lastUsage(t); rec.Provider != "other" || rec.Model != "gpt-6.1-sol" || rec.Requested != "other/gpt-6.1-sol" {
		t.Fatalf("recorded as %+v", rec)
	}
	if w := messages(secrets[1], "bb-codex~gpt-6.1-sol"); w.Code != 200 || codex.calls != 1 || sentModel(codex) != "gpt-6.1-sol" {
		t.Fatalf("bb-codex~gpt-6.1-sol: %d %s", w.Code, w.Body.String())
	}
	// as are the routing groups magpie found
	groups := 0
	for _, e := range provider.Catalog() {
		if e.Group == "" {
			continue
		}
		groups++
		if w := messages(secrets[1], provider.FlatID(e.ID)); w.Code != 200 {
			t.Fatalf("%s: %d %s", provider.FlatID(e.ID), w.Code, w.Body.String())
		}
	}
	if groups == 0 {
		t.Fatal("no group for gpt-6.1-sol, served by two providers")
	}
	// a flat id no model has is none
	if w := messages(secrets[1], "nowhere~gpt-6.1-sol"); w.Code != 404 {
		t.Fatalf("nowhere~gpt-6.1-sol: %d %s", w.Code, w.Body.String())
	}
	if w := do(secrets[1], "GET", "/v1/models/bb-codex~gpt-6.1-sol", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"bb-codex/gpt-6.1-sol"`) {
		t.Fatalf("model: %d %s", w.Code, w.Body.String())
	}

	// a key held to bb-codex: refused other's by its flat id too, on every
	// route, and given bb-codex's
	calls := other.calls
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/responses"} {
		w := do(secrets[0], "POST", path, `{"model":"other~gpt-6.1-sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`)
		if w.Code != 403 || other.calls != calls {
			t.Fatalf("held key on %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := do(secrets[0], "GET", "/v1/models/other~gpt-6.1-sol", ""); w.Code == 200 {
		t.Fatalf("held key found other's model: %s", w.Body.String())
	}
	if w := messages(secrets[0], "bb-codex~gpt-6.1-sol"); w.Code != 200 {
		t.Fatalf("held key on its own model: %d %s", w.Code, w.Body.String())
	}
	if w := do(secrets[0], "POST", "/v1/messages/count_tokens", `{"model":"bb-codex~gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatalf("held key counting its own model: %d %s", w.Code, w.Body.String())
	}
}
