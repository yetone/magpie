package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model the user disabled (#1867) is refused on every route through
// magpie before any provider is asked: by id, bare or by a glob, on each
// API in its own error shape, as a routing group's member (the group goes
// on without it; one left with none is refused), as a fallback, by
// count_tokens and as System One's decision model. It is gone from
// /v1/models, and enabled again it is served as before.
func TestDisabledModelRefusedOnEveryRoute(t *testing.T) {
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
	for _, g := range []provider.Group{
		{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered},
		{Name: "Spare only", Members: []string{"spare/m2"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	chat := func(model string) *httptest.ResponseRecorder {
		return do("POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	listed := func() []string {
		var l struct{ Data []struct{ ID string } }
		w := do("GET", "/v1/models", "")
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	// default: nothing disabled, both served
	if ids := listed(); !slices.Contains(ids, "spare/m2") {
		t.Fatalf("lists %v", ids)
	}
	if w := chat("spare/m2"); w.Code != 200 {
		t.Fatal("before disabling", w.Code, w.Body.String())
	}
	spare.calls = 0

	if err := provider.DisableModels("*M2*"); err != nil {
		t.Fatal(err)
	}
	if ids := listed(); slices.Contains(ids, "spare/m2") || !slices.Contains(ids, "plan/m1") || slices.Contains(ids, "group/spare-only") {
		t.Fatalf("lists %v", ids)
	}
	refused := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		var e struct {
			Error struct{ Message, Type string }
		}
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, "is disabled in magpie") {
			t.Fatalf("%s: %d %s", what, w.Code, w.Body.String())
		}
	}
	for _, model := range []string{"spare/m2", "m2", "SPARE/M2", "group/spare-only", "spare-only"} {
		refused(model, chat(model))
	}
	if w := chat("group/spare-only"); !strings.Contains(w.Body.String(), "routing group") || !strings.Contains(w.Body.String(), "spare/m2") {
		t.Fatal("an emptied group says why", w.Body.String())
	}
	// each API in its own shape
	w := do("POST", "/v1/messages", `{"model":"spare/m2","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	var a struct {
		Type  string
		Error struct{ Type, Message string }
	}
	if json.Unmarshal(w.Body.Bytes(), &a); w.Code != 403 || a.Type != "error" || a.Error.Type != "permission_error" || !strings.Contains(a.Error.Message, "disabled") {
		t.Fatal("anthropic", w.Code, w.Body.String())
	}
	refused("responses", do("POST", "/v1/responses", `{"model":"spare/m2","input":"hi"}`))
	gem := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		var g struct {
			Error struct {
				Code    int
				Status  string
				Message string
			}
		}
		if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &g) != nil || g.Error.Status != "PERMISSION_DENIED" || !strings.Contains(g.Error.Message, "disabled") {
			t.Fatalf("%s: %d %s", what, w.Code, w.Body.String())
		}
	}
	gem("gemini", do("POST", "/v1beta/models/spare/m2:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	// count_tokens, Anthropic's and Gemini's
	refused("count_tokens", do("POST", "/v1/messages/count_tokens", `{"model":"spare/m2","messages":[{"role":"user","content":"hi"}]}`))
	refused("count_tokens group", do("POST", "/v1/messages/count_tokens", `{"model":"group/spare-only","messages":[{"role":"user","content":"hi"}]}`))
	gem("gemini count", do("POST", "/v1beta/models/spare/m2:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if w := do("POST", "/v1/messages/count_tokens", `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatal("counting another model", w.Code, w.Body.String())
	}
	// a group with one left goes on without it
	if w := chat("group/both"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-plan") {
		t.Fatal("group/both", w.Code, w.Body.String())
	}
	// plan out of quota: neither the group's next member nor plan's own
	// fallback, both spare/m2, is asked
	plan.code, plan.reply = 429, `{"error":{"message":"slow down"}}`
	if w := chat("group/both"); w.Code == 200 {
		t.Fatal("the group fell to a disabled member", w.Body.String())
	}
	if w := chat("plan/m1"); w.Code == 200 {
		t.Fatal("a fallback to a disabled model", w.Body.String())
	}
	if spare.calls != 0 {
		t.Fatal("a disabled model was asked", spare.calls)
	}
	plan.code, plan.reply = 0, `{"id":"from-plan","choices":[]}`

	// System One's decision model, by a pattern of its own
	if err := provider.DisableModels("jev/*"); err != nil {
		t.Fatal(err)
	}
	const ask = `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`
	refused("system one", do("POST", "/v1/systemone", ask))
	// magpie's own decision calls (a group rule's classifier, a decision
	// group's members, the provider check) all post through postDecide
	jpp, err := provider.Find("jev")
	if err != nil {
		t.Fatal(err)
	}
	jp := *jpp
	if _, err := New().postDecideSent(context.Background(), jp, "jev-latest", []byte(ask)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("magpie's own decision call", err)
	}
	if err := jp.AskSystemOne(context.Background(), "jev-latest"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("the provider check", err)
	}
	if decided != 0 {
		t.Fatal("a disabled decision model was asked", decided)
	}
	for _, d := range provider.Deciders() {
		if d.Provider.ID == "jev" {
			t.Fatal("the decision models list a disabled one", d)
		}
	}

	// settings.json that stops parsing: the list read last stays in force
	path := settings.Path()
	good, _ := os.ReadFile(path)
	os.WriteFile(path, []byte("{not json"), 0o600)
	refused("unreadable settings", chat("spare/m2"))
	os.WriteFile(path, good, 0o600)

	// an exact pattern refuses the model asked bare too, as itself, not
	// as a model magpie doesn't know
	if _, err := provider.EnableModels("*m2*"); err != nil {
		t.Fatal(err)
	}
	if err := provider.DisableModels("spare/m2"); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"spare/m2", "m2"} {
		refused("exact "+model, chat(model))
	}

	// enabled again: served as before
	if missing, err := provider.EnableModels("spare/m2", "jev/*"); err != nil || len(missing) != 0 {
		t.Fatal(missing, err)
	}
	if w := chat("spare/m2"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("enabled again", w.Code, w.Body.String())
	}
	if w := do("POST", "/v1/systemone", ask); w.Code != 200 || decided != 1 {
		t.Fatal("decision enabled again", w.Code, w.Body.String())
	}
}

// An embeddings model the user disabled is refused before its provider is
// asked.
func TestDisabledEmbeddingsModelRefused(t *testing.T) {
	s, l := shelved(t)
	if err := provider.DisableModels("lib/embed-1"); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"lib/embed-1", "embed-1"} {
		code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"`+model+`","input":"hi"}`)
		if code != 403 || !strings.Contains(raw, "is disabled in magpie") {
			t.Fatalf("%s: %d %s", model, code, raw)
		}
	}
	if sent := l.got("/v1/embeddings"); len(sent) != 0 {
		t.Fatal("a disabled embeddings model was asked", sent)
	}
}

// Codex's own models, disabled: gone from the list Codex reads, refused at
// the ChatGPT backend's route before it is asked, and a subagent put on
// one stays on its lead's model.
func TestDisabledCodexModels(t *testing.T) {
	up := codexSubagentSetup(t)
	if err := provider.SetCodexSubagentModel("codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	if err := provider.DisableModels("gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "gpt-5.5", "collab_spawn", sealedTaskFernet)
	if rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if r := lastRoute(s); r.Subagent == nil || r.Subagent.Kept != "it is disabled in magpie" {
		t.Fatalf("trace %+v", r.Subagent)
	}
	rec = codexAsks(s, "gpt-6-astra", "", leadAsk)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "is disabled in magpie") {
		t.Fatalf("lead on a disabled model: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
}

// Codex's model list leaves a disabled one of its own out.
func TestDisabledCodexModelsListed(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"up"`)
		w.Write([]byte(`{"models":[{"slug":"gpt-6","priority":1},{"slug":"gpt-5.5","priority":2}]}`))
	})
	list := func() []string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models?client_version=0.159.2", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		var l struct {
			Models []map[string]any `json:"models"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var slugs []string
		for _, m := range l.Models {
			slugs = append(slugs, m["slug"].(string))
		}
		return slugs
	}
	if got := list(); !slices.Contains(got, "gpt-6") || !slices.Contains(got, "fake/m1") {
		t.Fatalf("before: %v", got)
	}
	if err := provider.DisableModels("gpt-6", "fake/*"); err != nil {
		t.Fatal(err)
	}
	if got := list(); slices.Contains(got, "gpt-6") || slices.Contains(got, "fake/m1") || !slices.Contains(got, "gpt-5.5") {
		t.Fatalf("after: %v", got)
	}
}
