package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
)

// usedReply is a chat answer of 350 tokens, which the key's limits count.
func usedReply(id string) string {
	return `{"id":"` + id + `","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":50}}`
}

// keyCall sends body to path with secret as the gateway key.
func keyCall(h http.Handler, secret, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// spentSaid fails t unless w is the 429 of a spent limit of some models:
// it names the key and the models, says when it resets, and leaves the
// key's reset header out, as the key's other models are still served.
func spentSaid(t *testing.T, w *httptest.ResponseRecorder, scope string) {
	t.Helper()
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != http.StatusTooManyRequests || e.Error.Type != "rate_limit_error" || !strings.Contains(e.Error.Message, `"Capped"`) || !strings.Contains(e.Error.Message, scope) {
		t.Fatalf("refusal: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" || w.Header().Get("X-Should-Retry") != "false" || w.Header().Get(remoteKeyLimitHeader) != "" {
		t.Fatalf("headers %v", w.Header())
	}
}

// zola_xynb on X: a key's limit of some models counts their calls alone
// and refuses them alone once spent, said as a limit; the key's other
// models go on, a routing group goes on to its members outside it (and
// is refused only when none is left), counting tokens on a spent model is
// refused as serving it would be, the list is unchanged, another key is
// free, and a restart reads what was spent back from the ledger.
func TestKeyModelLimitHoldsItsModelsAlone(t *testing.T) {
	fresh(t)
	budget.Forget()
	pinLimitClock(t)
	plan := &fake{t: t, ctype: "application/json", reply: usedReply("from-plan")}
	spare := &fake{t: t, ctype: "application/json", reply: usedReply("from-spare")}
	twoProviders(t, plan, spare)
	for _, g := range []provider.Group{
		{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered},
		{Name: "Solo", Members: []string{"plan/m1"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	keys, secrets := newCaller(t, "Capped", "Other")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700, Models: []string{"plan/*"}})
	h := New().Handler()
	chat := func(secret, model string) *httptest.ResponseRecorder {
		return keyCall(h, secret, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	// spare's calls don't count in it
	for range 3 {
		if w := chat(secrets[0], "spare/m2"); w.Code != 200 {
			t.Fatal("another provider's model", w.Code, w.Body.String())
		}
	}
	for i := range 2 {
		if w := chat(secrets[0], "plan/m1"); w.Code != 200 {
			t.Fatal("within the limit", i, w.Code, w.Body.String())
		}
	}
	if plan.calls != 2 {
		t.Fatal(plan.calls)
	}
	for _, model := range []string{"plan/m1", "m1", "group/solo"} {
		spentSaid(t, chat(secrets[0], model), "plan/*")
	}
	if plan.calls != 2 {
		t.Fatal("a spent model's request reached its provider", plan.calls)
	}
	if rec := lastUsage(t); !rec.Rejected || rec.CallerKeyID != keys[0].ID || rec.ErrType != "gateway_key_limit" || rec.Status != 429 {
		t.Fatalf("refusal recorded as %+v", rec)
	}
	// the key's other models go on, and a group to its member outside it
	spareCalls := spare.calls
	if w := chat(secrets[0], "spare/m2"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("a model outside the limit", w.Code, w.Body.String())
	}
	if w := chat(secrets[0], "group/both"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") || plan.calls != 2 || spare.calls != spareCalls+2 {
		t.Fatal("a group didn't go on past its spent member", w.Code, w.Body.String(), plan.calls)
	}
	// counting tokens on a spent model is refused, on another counted
	count := func(model string) *httptest.ResponseRecorder {
		return keyCall(h, secrets[0], "/v1/messages/count_tokens", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	spentSaid(t, count("plan/m1"), "plan/*")
	if w := count("spare/m2"); w.Code != 200 || !strings.Contains(w.Body.String(), "input_tokens") {
		t.Fatal("counting another model", w.Code, w.Body.String())
	}
	// the list still has it, as a spent key's has every model
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"plan/m1"`) {
		t.Fatal("a spent model left the list", w.Body.String())
	}
	if w := chat(secrets[1], "plan/m1"); w.Code != 200 {
		t.Fatal("another key held to the spent one's limit", w.Code)
	}
	budget.Forget()
	spentSaid(t, chat(secrets[0], "plan/m1"), "plan/*")
	if w := chat(secrets[0], "spare/m2"); w.Code != 200 {
		t.Fatal("after a restart, a model outside the limit", w.Code)
	}
}

// A request that moves to another model midway — a provider's fallback
// when it is out of quota — doesn't move onto a model a spent limit of
// the key's counts: that one is never asked, and the refusal says why.
func TestKeyModelLimitHoldsAFallback(t *testing.T) {
	fresh(t)
	budget.Forget()
	pinLimitClock(t)
	plan := &fake{t: t, ctype: "application/json", reply: usedReply("from-plan")}
	spare := &fake{t: t, ctype: "application/json", reply: usedReply("from-spare")}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 350, Models: []string{"spare/*"}})
	h := New().Handler()
	if w := keyCall(h, secrets[0], "/v1/chat/completions", `{"model":"spare/m2","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	plan.code, plan.reply = 429, `{"error":{"message":"slow down"}}`
	w := keyCall(h, secrets[0], "/v1/chat/completions", chatReq)
	if spare.calls != 1 || strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("the fallback went to a spent model", spare.calls, w.Code, w.Body.String())
	}
	if plan.calls == 0 {
		t.Fatal("the model asked wasn't tried")
	}
}

// System One's decision model is held by a spent limit that counts it.
func TestKeyModelLimitHoldsSystemOne(t *testing.T) {
	fresh(t)
	budget.Forget()
	pinLimitClock(t)
	var decided int
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decided++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":300,"output_tokens":50}}`))
	}))
	t.Cleanup(jev.Close)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "k", Decide: jev.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 300, Models: []string{"jev/*"}})
	h := New().Handler()
	const ask = `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`
	if w := keyCall(h, secrets[0], "/v1/systemone", ask); w.Code != 200 || decided != 1 {
		t.Fatal("within the limit", w.Code, w.Body.String())
	}
	spentSaid(t, keyCall(h, secrets[0], "/v1/systemone", ask), "jev/*")
	if decided != 1 {
		t.Fatal("a spent decider was asked", decided)
	}
}

// A routing group of embedding or image models goes on past a member a
// spent limit of the key's counts, and is refused when it has no other.
func TestKeyModelLimitHoldsMediaGroups(t *testing.T) {
	s, e := easeled(t)
	budget.Forget()
	pinLimitClock(t)
	spent := &fake{t: t, ctype: "application/json", reply: usedReply("from-spent")}
	up := httptest.NewServer(spent)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "flare", Name: "Flare", Chat: up.URL + "/v1", Key: "k", Models: []string{"chat", "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "pics", Name: "pics", Members: []string{"flare/gpt-image-1", "art/gpt-image-1"}, Routing: provider.Ordered},
		{ID: "lone", Name: "lone", Members: []string{"flare/gpt-image-1"}, Routing: provider.Ordered},
		{ID: "emb", Name: "emb", Members: []string{"flare/embed-1"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	keys, secrets := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 350, Models: []string{"flare/*"}})
	h := s.Handler()
	if w := keyCall(h, secrets[0], "/v1/chat/completions", `{"model":"flare/chat","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := keyCall(h, secrets[0], "/v1/images/generations", `{"model":"group/pics","prompt":"a magpie"}`)
	if w.Code != 200 || spent.calls != 1 || len(e.got("/v1/images/generations")) != 1 {
		t.Fatal("a drawing group didn't go on past its spent member", w.Code, w.Body.String(), spent.calls)
	}
	spentSaid(t, keyCall(h, secrets[0], "/v1/images/generations", `{"model":"group/lone","prompt":"a magpie"}`), "flare/*")
	spentSaid(t, keyCall(h, secrets[0], "/v1/embeddings", `{"model":"group/emb","input":"hi"}`), "flare/*")
	if spent.calls != 1 {
		t.Fatal("a spent member was asked", spent.calls)
	}
}

// A limit of the whole key, as every one from before, is untouched by
// limits of some models beside it: it counts every model, and says its
// reset to a remote magpie.
func TestKeyModelLimitBesideTheWholeKey(t *testing.T) {
	fresh(t)
	budget.Forget()
	pinLimitClock(t)
	plan := &fake{t: t, ctype: "application/json", reply: usedReply("from-plan")}
	spare := &fake{t: t, ctype: "application/json", reply: usedReply("from-spare")}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Capped")
	if _, err := access.Update("limit-key", access.Change{Key: keys[0].ID, Limit: &access.Limit{Period: "day", Tokens: 1050}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("limit-key", access.Change{Key: keys[0].ID, Index: new(1), Limit: &access.Limit{Period: "day", Tokens: 10000, Models: []string{"plan/*"}}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, model := range []string{"spare/m2", "spare/m2", "plan/m1"} {
		if w := keyCall(h, secrets[0], "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
			t.Fatal(model, w.Code, w.Body.String())
		}
	}
	w := keyCall(h, secrets[0], "/v1/chat/completions", `{"model":"spare/m2","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 429 || w.Header().Get(remoteKeyLimitHeader) == "" {
		t.Fatal("the whole key's limit", w.Code, w.Header(), w.Body.String())
	}
}
