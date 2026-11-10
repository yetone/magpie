package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// keyDo sends a request with a gateway key to a handler.
func keyDo(h http.Handler, secret, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func keyListedIDs(t *testing.T, h http.Handler, secret, path string) []string {
	t.Helper()
	var l struct{ Data []struct{ ID string } }
	w := keyDo(h, secret, "GET", path, "")
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(w.Body.String())
	}
	var ids []string
	for _, m := range l.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// A key's model may be a regular expression, "re:<expression>" (tsingchao
// on X), and holds a key on every route that checks its models: the lists,
// a chat, Anthropic's and the Responses API, counting tokens, Gemini's
// path and System One's decision model.
func TestGatewayKeyModelPatternHoldsEveryRoute(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	var decided int
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decided++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	t.Cleanup(jev.Close)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "k", Decide: jev.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	// the model alone, or the whole id
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"re:^m1$"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	ids := keyListedIDs(t, h, secrets[0], "/v1/models")
	if !slices.Contains(ids, "plan/m1") || slices.Contains(ids, "spare/m2") || slices.Contains(ids, "group/both") {
		t.Fatalf("lists %v", ids)
	}
	if w := keyDo(h, secrets[0], "GET", "/v1beta/models", ""); strings.Contains(w.Body.String(), "m2") || !strings.Contains(w.Body.String(), "m1") {
		t.Fatalf("gemini list %s", w.Body.String())
	}
	msg := `"messages":[{"role":"user","content":"hi"}]`
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"spare/m2",` + msg + `}`},
		{"/v1/messages", `{"model":"spare/m2","max_tokens":10,` + msg + `}`},
		{"/v1/messages/count_tokens", `{"model":"spare/m2",` + msg + `}`},
		{"/v1/responses", `{"model":"spare/m2","input":"hi"}`},
		{"/v1/chat/completions", `{"model":"group/both",` + msg + `}`},
		{"/v1beta/models/spare/m2:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`},
	} {
		if w := keyDo(h, secrets[0], "POST", c.path, c.body); w.Code != 403 {
			t.Fatalf("%s %s: %d %s", c.path, c.body, w.Code, w.Body.String())
		}
	}
	if plan.calls != 0 || spare.calls != 0 || decided != 0 {
		t.Fatal("a refused request reached a provider", plan.calls, spare.calls, decided)
	}
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"plan/m1",` + msg + `}`},
		{"/v1/messages/count_tokens", `{"model":"plan/m1",` + msg + `}`},
	} {
		if w := keyDo(h, secrets[0], "POST", c.path, c.body); w.Code != 200 {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
	}
	// the decision model by a pattern of its own
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"re:^jev/jev-"}}); err != nil {
		t.Fatal(err)
	}
	if w := keyDo(h, secrets[0], "POST", "/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`); w.Code != 200 || decided != 1 {
		t.Fatalf("system one by pattern: %d %s", w.Code, w.Body.String())
	}
	// a pattern that names everything names no routing group of the
	// user's own: that is "group/<name>"
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"re:.*"}}); err != nil {
		t.Fatal(err)
	}
	if ids := keyListedIDs(t, h, secrets[0], "/v1/models"); !slices.Contains(ids, "plan/m1") || slices.Contains(ids, "group/both") {
		t.Fatalf("re:.* lists %v", ids)
	}
	if w := keyDo(h, secrets[0], "POST", "/v1/chat/completions", `{"model":"group/both",`+msg+`}`); w.Code != 403 {
		t.Fatalf("re:.* reached a group: %d %s", w.Code, w.Body.String())
	}
}

// A key gives a model another name (tsingchao on X): /v1/models lists the
// name in the model's place, and a request by it goes to the model on
// every route that carries one: the body, Gemini's path, a model's URL.
func TestGatewayKeyAliasRenamesAModel(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Named", "Plain")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"plan/*"}}); err != nil {
		t.Fatal(err)
	}
	// a model the key may not use can't be named
	if _, err := access.Update("aliases-key", access.Change{Key: keys[0].ID, Aliases: map[string]string{"gpt-4": "spare/m2"}}); err == nil {
		t.Fatal("named a model the key may not use")
	}
	if _, err := access.Update("aliases-key", access.Change{Key: keys[0].ID, Aliases: map[string]string{"gpt-4": "plan/m1"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	ids := keyListedIDs(t, h, secrets[0], "/v1/models")
	if !slices.Contains(ids, "gpt-4") || slices.Contains(ids, "plan/m1") {
		t.Fatalf("lists %v", ids)
	}
	if ids := keyListedIDs(t, h, secrets[1], "/v1/models"); slices.Contains(ids, "gpt-4") || !slices.Contains(ids, "plan/m1") {
		t.Fatalf("another key lists %v", ids)
	}
	if w := keyDo(h, secrets[0], "GET", "/v1/models?format=text", ""); !strings.Contains(w.Body.String(), "gpt-4\n") {
		t.Fatalf("text list %q", w.Body.String())
	}
	if w := keyDo(h, secrets[0], "GET", "/v1/models/gpt-4", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"gpt-4"`) {
		t.Fatalf("one model: %d %s", w.Code, w.Body.String())
	}
	msg := `"messages":[{"role":"user","content":"hi"}]`
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-4",` + msg + `}`},
		{"/v1/chat/completions", `{"model":"GPT-4","stream":false,` + msg + `}`},
		{"/v1/messages", `{"model":"gpt-4","max_tokens":10,` + msg + `}`},
		{"/v1/messages/count_tokens", `{"model":"gpt-4",` + msg + `}`},
	} {
		plan.calls = 0
		w := keyDo(h, secrets[0], "POST", c.path, c.body)
		// the fake answers in chat's JSON, which Anthropic's route can't
		// stream: what counts is that it reached the model, not a 403
		if w.Code == 403 || w.Code == 404 {
			t.Fatalf("%s %s: %d %s", c.path, c.body, w.Code, w.Body.String())
		}
		if strings.HasSuffix(c.path, "count_tokens") {
			continue
		}
		if plan.calls != 1 || !strings.Contains(string(plan.got), `"m1"`) || strings.Contains(string(plan.got), "gpt-4") {
			t.Fatalf("%s: the provider got %s", c.path, plan.got)
		}
	}
	if w := keyDo(h, secrets[0], "POST", "/v1beta/models/gpt-4:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`); w.Code != 200 || !strings.Contains(w.Body.String(), "totalTokens") {
		t.Fatalf("gemini by name: %d %s", w.Code, w.Body.String())
	}
	// another key has no such name
	if w := keyDo(h, secrets[1], "POST", "/v1/chat/completions", `{"model":"gpt-4",`+msg+`}`); w.Code == 200 {
		t.Fatalf("another key reached the name: %s", w.Body.String())
	}
	// the name stands for a model the key still must be given: taken out
	// of its models, the name is refused and no longer listed
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"spare/*"}}); err != nil {
		t.Fatal(err)
	}
	if w := keyDo(h, secrets[0], "POST", "/v1/chat/completions", `{"model":"gpt-4",`+msg+`}`); w.Code != 403 {
		t.Fatalf("name of a model the key lost: %d %s", w.Code, w.Body.String())
	}
	if ids := keyListedIDs(t, h, secrets[0], "/v1/models"); slices.Contains(ids, "gpt-4") {
		t.Fatalf("lists %v", ids)
	}
}
