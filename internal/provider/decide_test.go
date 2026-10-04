package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestMixedDecisionProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"typesafe/jev"},{"id":"deepseek-v4.1-flash"},{"id":"jevons"}]}`))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			var q struct{ Model string }
			json.NewDecoder(r.Body).Decode(&q)
			if q.Model != "deepseek-v4.1-flash" {
				http.Error(w, "wrong conversation model", 400)
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	p := Provider{ID: "mixed", Name: "Mixed", Key: "k", Chat: up.URL + "/v1", Decide: up.URL + "/v1", Models: []string{"typesafe/jev", "deepseek-v4.1-flash", "jevons"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "other", Name: "Other", Key: "k", Chat: up.URL + "/v1", Models: []string{"deepseek-v4-1-flash", "typesafe/jev"}}); err != nil {
		t.Fatal(err)
	}
	if ms, err := p.Fetch(context.Background()); err != nil || len(ms) != 3 {
		t.Fatalf("mixed fetch: %+v %v", ms, err)
	}
	var ids []string
	for _, e := range providerEntries() {
		if e.Provider.ID == p.ID {
			ids = append(ids, e.ID)
		}
	}
	if !slices.Equal(ids, []string{"mixed/deepseek-v4.1-flash", "mixed/jevons"}) {
		t.Fatalf("conversation models: %v", ids)
	}
	if ds := Deciders(); len(ds) != 1 || ds[0].ID != "mixed/typesafe/jev" || IsDecider("mixed/deepseek-v4.1-flash") || !IsDecider("mixed/typesafe/jev") {
		t.Fatalf("classifiers: %+v", ds)
	}
	if _, ms, ok := FindGroup("group/auto-deepseek-v4-1-flash"); !ok || len(ms) != 2 || ms[0].Provider.ID != p.ID {
		t.Fatalf("automatic group: %+v %v", ms, ok)
	}
	if err := SaveGroup(Group{Name: "Bad", Members: []string{"mixed/typesafe/jev"}}); err == nil {
		t.Fatal("Jev accepted as a conversation member")
	}
	if got, m, err := RouteDecider("mixed/typesafe/jev"); err != nil || got.ID != p.ID || m != "typesafe/jev" {
		t.Fatalf("Jev route: %+v %s %v", got, m, err)
	}
	for _, id := range []string{"mixed/deepseek-v4.1-flash", "deepseek-v4.1-flash", "mixed/jevons"} {
		if _, _, err := RouteDecider(id); err == nil {
			t.Fatalf("conversation model %s accepted on System One", id)
		}
	}
	if why := p.ModelTest(); why != "" {
		t.Fatalf("conversation tests disabled: %s", why)
	}
	if r := p.Test(context.Background()); len(r) != 2 || !r[0].OK || r[0].Model != "deepseek-v4.1-flash" || !r[1].OK || r[1].Protocol != "decide" {
		t.Fatalf("endpoint tests: %+v", r)
	}
	if ms, _, _ := catalog.Live(p.ID); len(ms) != 3 {
		t.Fatalf("decision probe replaced conversation list: %+v", ms)
	}
	if r := p.TestModels(context.Background(), []string{"deepseek-v4.1-flash", "typesafe/jev"}); !r[0].OK || r[1].OK || r[1].Error == "" {
		t.Fatalf("model tests: %+v", r)
	}
	for _, endpoint := range []Provider{{Chat: "chat"}, {Responses: "responses"}, {Anthropic: "anthropic"}} {
		endpoint.Decide = "decide"
		if endpoint.DecideOnly() || endpoint.DecidesModel("deepseek-v4.1-flash") || !endpoint.DecidesModel("typesafe/jev-preview") {
			t.Fatalf("mixed endpoint: %+v", endpoint)
		}
	}
	p.Models = nil
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	var many []catalog.Model
	for i := 1; i <= manyModels; i++ {
		many = append(many, catalog.Model{ID: strings.Repeat("m", i)})
	}
	many = append(many, catalog.Model{ID: "typesafe/jev"})
	if err := catalog.SaveLive(p.ID, p.Chat, many); err != nil {
		t.Fatal(err)
	}
	if ds := Deciders(); len(ds) != 1 || ds[0].ID != "mixed/typesafe/jev" {
		t.Fatalf("classifier hidden by conversation model limit: %+v", ds)
	}
}

// A decision provider (TypeSafe's Jev) is only ever a group's classifier:
// its models aren't in the catalog nor a group's members, and a group's
// effort is picked only by it.
func TestDecider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	ts, err := FromPreset("typesafe")
	if err != nil || !ts.Decides() {
		t.Fatalf("preset %+v %v", ts, err)
	}
	ts.Key = "kts"
	if err := Save(ts); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("typesafe"); err != nil || p.Host() != "api.typesafe.ai" {
		t.Fatalf("saved %+v %v", p, err)
	}
	for _, e := range Catalog() {
		if e.Provider.ID == "typesafe" {
			t.Fatalf("Jev in the catalog: %+v", e)
		}
	}
	if ds := Deciders(); len(ds) == 0 || ds[0].ID != "typesafe/jev-latest" || !IsDecider("typesafe/jev-latest") || IsDecider("a/m") {
		t.Fatalf("deciders %+v", ds)
	}
	if p, _, ok := Resolve("jev-latest"); ok && p.Decides() {
		t.Fatal("a bare jev-latest resolves to the decider")
	}
	g := Group{Name: "G", Members: []string{"a/m"}}
	for _, tc := range []struct {
		effort, classifier, err string
	}{
		{"auto", "", "needs the group's classifier"},
		{"auto", "b/m", "knows no model"},
		{"auto", "a/m", ""}, // any model, asked in words
		{"high", "typesafe/jev-latest", "not \"high\""},
		{"auto", "typesafe/jev-latest", ""},
	} {
		g.Effort, g.Classifier = tc.effort, tc.classifier
		err := SaveGroup(g)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s by %q: %v, want %q", tc.effort, tc.classifier, err, tc.err)
		}
	}
	if g, _, _ := FindGroup("group/g"); g.Effort != EffortAuto || g.Classifier != "typesafe/jev-latest" || !g.Ruled() {
		t.Fatalf("saved %+v", g)
	}
	if err := SaveGroup(Group{Name: "H", Members: []string{"typesafe/jev-latest"}}); err == nil {
		t.Error("Jev saved as a group's member")
	}
}

// TypeSafe's errors are FastAPI's: {"detail":{"message":…}}.
func TestAPIErrorDetail(t *testing.T) {
	b := []byte(`{"detail":{"error_type":"authentication_error","message":"Must supply an API key! Check your request and try again."}}`)
	if got := APIError(b, "403 Forbidden"); got != "Must supply an API key! Check your request and try again." {
		t.Fatal(got)
	}
}

// Jev on Vercel's and Cloudflare's gateways: known by where it is, named
// as each names it, and a key checked by the gateway's own free call.
func TestDecideGateways(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for id, want := range map[string][2]string{"typesafe": {ViaSystemOne, "jev-latest"}, "vercel-jev": {ViaVercel, "typesafe-ai/jev"}, "cloudflare-jev": {ViaCloudflare, "typesafe/jev"}} {
		p, err := FromPreset(id)
		if err != nil || !p.Decides() || p.DecideVia() != want[0] || p.Jev() != want[1] || p.decideModels()[0].ID != want[1] {
			t.Errorf("%s: %+v %v", id, p, err)
		}
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			if strings.HasPrefix(r.URL.Path, "/client/") {
				http.Error(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`, 403)
			} else {
				http.Error(w, `{"error":{"message":"Invalid API key"}}`, 401)
			}
			return
		}
		switch r.URL.Path {
		case "/v1/credits":
			w.Write([]byte(`{"balance":"5.00","total_used":"0.00"}`))
		case "/typesafe/v1/models":
			w.Write([]byte(`{"models":[{"name":"typesafe-ai/jev"}]}`))
		case "/client/v4/accounts":
			w.Write([]byte(`{"success":true,"result":[{"id":"acc9"}]}`))
		case "/client/v4/accounts/acc7/ai/models/search":
			w.Write([]byte(`{"success":true,"result":[{"name":"typesafe/jev"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	for _, c := range []struct{ decide, bad string }{
		{up.URL + "/typesafe", "Invalid API key"},
		{up.URL + "/v4/ai", "Invalid API key"},
		{up.URL + "/client/v4/", "Authentication error"},
		{up.URL + "/client/v4/accounts/acc7/ai/run", "Authentication error"},
	} {
		p := Provider{ID: "g", Name: "G", Key: "good", Decide: c.decide}
		if r := p.Test(context.Background()); len(r) != 1 || !r[0].OK || r[0].Model != p.Jev() {
			t.Errorf("%s: %+v", c.decide, r)
		}
		p.Key = "bad"
		if r := p.Test(context.Background()); len(r) != 1 || r[0].OK || !strings.Contains(r[0].Error, c.bad) {
			t.Errorf("%s bad key: %+v", c.decide, r)
		}
	}
	for decide, want := range map[string]string{
		// Vercel's TypeSafe API as its docs give it, or near it; /v4/ai as before
		"https://ai-gateway.vercel.sh/typesafe":              "https://ai-gateway.vercel.sh/typesafe/v1/systemone",
		"https://ai-gateway.vercel.sh/typesafe/v1/":          "https://ai-gateway.vercel.sh/typesafe/v1/systemone",
		"https://ai-gateway.vercel.sh/typesafe/v1/systemone": "https://ai-gateway.vercel.sh/typesafe/v1/systemone",
		"https://ai-gateway.vercel.sh/v1":                    "https://ai-gateway.vercel.sh/typesafe/v1/systemone",
		"https://ai-gateway.vercel.sh":                       "https://ai-gateway.vercel.sh/typesafe/v1/systemone",
		"https://ai-gateway.vercel.sh/v4/ai":                 "https://ai-gateway.vercel.sh/v4/ai/evaluation-model",
		// Workers AI with the account in it, as Cloudflare's docs give it
		"https://api.cloudflare.com/client/v4/accounts/acc7/ai/run": "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run",
		"https://api.cloudflare.com/client/v4/accounts/acc7/":       "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run",
		up.URL + "/client/v4": up.URL + "/client/v4/accounts/acc9/ai/run",
		up.URL + "/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/ai/run": up.URL + "/client/v4/accounts/acc9/ai/run",
	} {
		p := Provider{ID: "g", Name: "G", Key: "good", Decide: decide}
		if u, err := p.DecideURL(context.Background()); err != nil || u != want {
			t.Errorf("%s: %s %v", decide, u, err)
		}
	}
}

// A token Cloudflare won't list accounts for is told to name its account
// in the endpoint.
func TestCloudflareNoAccount(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`, 403)
	}))
	defer up.Close()
	p := Provider{ID: "g", Name: "G", Key: "workers-ai-only", Decide: up.URL + "/client/v4"}
	if _, err := p.DecideURL(context.Background()); err == nil || !strings.Contains(err.Error(), "/accounts/<account ID>/ai/run") {
		t.Fatal(err)
	}
}

// TypeSafe names every model in {"models":[{"name"}]}. A gateway in
// front of it answers OpenAI's {"data":[{"id"}]} instead, which is
// every model it serves; an id is kept only as jev* or */jev*.
func TestFetchDecideOpenAIJevIDs(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	var auth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"object":"list","data":[
			{"id":"gemini-3.8-flash"},
			{"id":"jev-latest"},
			{"id":"jev-preview"},
			{"id":"Jev-1.13.0"},
			{"id":"typesafe/jev"},
			{"id":"typesafe-ai/jev"},
			{"id":"org/typesafe/jev-preview"},
			{"id":"notjev"},
			{"id":"foo/notjev"},
			{"id":"foo/jevx"},
			{"id":"jevons"},
			{"id":"my-jev"},
			{"id":"jev-latest"}
		]}`))
	}))
	defer up.Close()
	p := Provider{ID: "gptload-jev", Name: "TypeSafe Jev", Key: "sk-test", Decide: up.URL + "/v1"}
	ms, err := p.fetchDecide(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(ms))
	for i, m := range ms {
		got[i] = m.ID
		if m.Name != m.ID {
			t.Errorf("name %q id %q", m.Name, m.ID)
		}
	}
	want := []string{"jev-latest", "jev-preview", "Jev-1.13.0", "typesafe/jev", "typesafe-ai/jev", "org/typesafe/jev-preview"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("auth %q", auth)
	}
	if r := p.Test(context.Background()); len(r) != 1 || !r[0].OK || r[0].Model != "jev-latest" {
		t.Fatalf("test %+v", r)
	}

	names, err := listedDecide([]byte(`{"models":[{"name":"jev-latest"},{"name":"other-model"}],"data":[{"id":"gemini-3"}]}`), false)
	if err != nil || len(names) != 2 || names[0].ID != "jev-latest" || names[1].ID != "other-model" {
		t.Fatalf("names %+v %v", names, err)
	}
	byID, err := listedDecide([]byte(`{"models":[{"id":"jev-preview"},{"id":"gemini-3"}]}`), false)
	if err != nil || len(byID) != 1 || byID[0].ID != "jev-preview" {
		t.Fatalf("id %+v %v", byID, err)
	}
	if ms, err := listedDecide([]byte(`{"data":[{"id":"gemini-3.8-flash"}]}`), false); err != nil || len(ms) != 0 {
		t.Fatalf("unrelated %+v %v", ms, err)
	}
	if _, err := listedDecide([]byte(`<html>`), false); err == nil || err.Error() != "not a model list" {
		t.Fatal(err)
	}
	if ms, err := listedDecide([]byte(`{"data":[{"id":"jevons"},{"id":"foo/jevx"}]}`), false); err != nil || len(ms) != 0 {
		t.Fatalf("jevons %+v %v", ms, err)
	}
}

// A System One model's prefix is the Jev provider. The rest is what that
// provider is asked. A bare Jev id needs exactly one provider that can
// answer it.
func TestRouteDecider(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if _, _, err := RouteDecider(""); err == nil || !strings.Contains(err.Error(), "names no model") {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("jev-latest"); err == nil || !strings.Contains(err.Error(), "no Jev provider") {
		t.Fatal(err)
	}
	for _, p := range []Provider{
		{ID: "gptload-jev", Name: "Load", Key: "k", Decide: "http://127.0.0.1:9/v1"},
		{ID: "typesafe", Name: "TypeSafe", Key: "k2", Decide: "https://api.typesafe.ai/v1"},
		{ID: "off-jev", Name: "Off", Key: "k3", Decide: "http://127.0.0.1:9/v1", Off: true},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.SaveLive("typesafe", "", []catalog.Model{
		{ID: "sys1-mini", Name: "Mini"},
		{ID: JevLatest, Name: "Jev"},
		{ID: "jev-preview", Name: "Jev (preview)"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("gptload-jev", "", []catalog.Model{
		{ID: JevLatest, Name: "Jev"},
		{ID: "jev-1.13.0", Name: "Jev 1.13"},
		{ID: "typesafe-ai/jev", Name: "Jev"},
	}); err != nil {
		t.Fatal(err)
	}
	p, model, err := RouteDecider("gptload-jev/jev-1.13.0")
	if err != nil || p.ID != "gptload-jev" || model != "jev-1.13.0" {
		t.Fatalf("%s %s %v", p.ID, model, err)
	}
	p, model, err = RouteDecider("gptload-jev/typesafe-ai/jev")
	if err != nil || p.ID != "gptload-jev" || model != "typesafe-ai/jev" {
		t.Fatalf("nested %s %s %v", p.ID, model, err)
	}
	p, model, err = RouteDecider("typesafe/sys1-mini")
	if err != nil || p.ID != "typesafe" || model != "sys1-mini" {
		t.Fatalf("listed %s %s %v", p.ID, model, err)
	}
	if _, _, err := RouteDecider("gptload-jev/gemini-3.8-flash"); err == nil || !strings.Contains(err.Error(), "is not a model of") || DecideRouteStatus(err) != http.StatusBadRequest {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("gptload-jev/jev-bogus-9"); err == nil || !strings.Contains(err.Error(), "is not a model of") || DecideRouteStatus(err) != http.StatusBadRequest {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("gptload-jev/jev-preview"); err == nil || !strings.Contains(err.Error(), "is not a model of") {
		t.Fatal(err)
	}
	p, model, err = RouteDecider("typesafe")
	if err != nil || p.ID != "typesafe" || model != JevLatest {
		t.Fatalf("bare provider %s %s %v", p.ID, model, err)
	}
	_, _, err = RouteDecider("jev-latest")
	if err == nil || DecideRouteStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "more than one") || !strings.Contains(err.Error(), "gptload-jev/jev-latest") || !strings.Contains(err.Error(), "typesafe/jev-latest") {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("off-jev/jev-latest"); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("missing/jev-latest"); err == nil || !strings.Contains(err.Error(), "knows no Jev model") {
		t.Fatal(err)
	}
}

// A lone Vercel Jev is asked as it names Jev, even when the request said
// TypeSafe's jev-latest.
func TestRouteDeciderVercelAlias(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := Save(Provider{ID: "vercel-jev", Name: "Vercel", Key: "k", Decide: "https://ai-gateway.vercel.sh/typesafe"}); err != nil {
		t.Fatal(err)
	}
	p, model, err := RouteDecider("jev-latest")
	if err != nil || p.ID != "vercel-jev" || model != "typesafe-ai/jev" {
		t.Fatalf("bare %s %s %v", p.ID, model, err)
	}
	p, model, err = RouteDecider("vercel-jev/jev-latest")
	if err != nil || p.ID != "vercel-jev" || model != "typesafe-ai/jev" {
		t.Fatalf("prefix %s %s %v", p.ID, model, err)
	}
	p, model, err = RouteDecider("vercel-jev/typesafe-ai/jev")
	if err != nil || model != "typesafe-ai/jev" {
		t.Fatalf("own name %s %v", model, err)
	}
	if _, _, err := RouteDecider("vercel-jev/jev-preview"); err == nil || !strings.Contains(err.Error(), "is not a model of") {
		t.Fatal(err)
	}
	if _, _, err := RouteDecider("vercel-jev/jev-bogus-9"); err == nil || !strings.Contains(err.Error(), "is not a model of") {
		t.Fatal(err)
	}
}

// Cloudflare's one Jev is typesafe/jev; jev-latest is that name, preview is not.
func TestRouteDeciderCloudflareAlias(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := Save(Provider{ID: "cloudflare-jev", Name: "CF", Key: "k", Decide: "https://api.cloudflare.com/client/v4"}); err != nil {
		t.Fatal(err)
	}
	p, model, err := RouteDecider("jev-latest")
	if err != nil || p.ID != "cloudflare-jev" || model != "typesafe/jev" {
		t.Fatalf("bare %s %s %v", p.ID, model, err)
	}
	if _, _, err := RouteDecider("cloudflare-jev/jev-preview"); err == nil || !strings.Contains(err.Error(), "is not a model of") {
		t.Fatal(err)
	}
}

// Two gateways that have not fetched a list both take jev-latest as their
// one Jev; a bare id must not pick one of them (404) as if none served it.
func TestRouteDeciderUnfetchedAmbiguous(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	for _, p := range []Provider{
		{ID: "vercel-a", Name: "VA", Key: "k", Decide: "https://ai-gateway.vercel.sh/typesafe"},
		{ID: "vercel-b", Name: "VB", Key: "k2", Decide: "https://ai-gateway.vercel.sh/typesafe"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := RouteDecider("jev-latest")
	if err == nil || DecideRouteStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "more than one") {
		t.Fatal(err)
	}
	// two System One providers, catalogs not fetched, both default to jev-latest
	h2 := t.TempDir()
	t.Setenv("HOME", h2)
	t.Setenv("USERPROFILE", h2)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h2, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h2, ".cache"))
	for _, p := range []Provider{
		{ID: "s1a", Name: "A", Key: "k", Decide: "http://127.0.0.1:9/v1"},
		{ID: "s1b", Name: "B", Key: "k2", Decide: "http://127.0.0.1:8/v1"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = RouteDecider("jev-latest")
	if err == nil || DecideRouteStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "more than one") {
		t.Fatal(err)
	}
}

// A vendor's words are shown whatever shape they come in: Tencent's
// {code, msg}, and a long body in no shape known cut short, not dropped.
func TestAPIErrorShapes(t *testing.T) {
	if got := APIError([]byte(`{"code":11001,"msg":"model not supported"}`), "400 Bad Request"); got != "model not supported" {
		t.Fatal(got)
	}
	long := `{"code":400,"data":null,"trace":"` + strings.Repeat("x", 400) + `"}`
	got := APIError([]byte(long), "400 Bad Request")
	if !strings.HasPrefix(got, `400 Bad Request: {"code":400`) || !strings.HasSuffix(got, "…") || len([]rune(got)) > 320 {
		t.Fatal(got)
	}
	if got := APIError([]byte("<html><body>bad</body></html>"), "400 Bad Request"); got != "400 Bad Request" {
		t.Fatal(got)
	}
	if got := APIError(nil, "400 Bad Request"); got != "400 Bad Request" {
		t.Fatal(got)
	}
}

// A backend that only streams is tested with a streamed request: WorkBuddy
// refuses any other with 400 (#124).
func TestTinyStreamsStreamOnly(t *testing.T) {
	p := Provider{Chat: "https://x/v1", Responses: "https://x/v1", Anthropic: "https://x"}
	for _, proto := range []Protocol{Chat, Responses, Anthropic} {
		if _, b := tiny(p, proto, "m"); strings.Contains(b, "stream") {
			t.Errorf("%s: %s", proto, b)
		}
		p.Account = &Account{Stream: true}
		_, b := tiny(p, proto, "m")
		var v map[string]any
		if err := json.Unmarshal([]byte(b), &v); err != nil || v["stream"] != true || v["model"] != "m" {
			t.Errorf("%s: %s", proto, b)
		}
		p.Account = nil
	}
}

// Workers AI's own decision models, Clef and Clef Flash, are offered
// beside Jev, routed to by name, and each asked at its own run.
func TestCloudflareClef(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	p, err := FromPreset("cloudflare-jev")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Decide = "k", "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run"
	var ids []string
	for _, m := range p.Available() {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, " ") != "typesafe/jev @cf/cloudflare/clef @cf/cloudflare/clef-flash" || p.Jev() != "typesafe/jev" {
		t.Fatalf("offered %v, default %s", ids, p.Jev())
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cloudflare-jev/@cf/cloudflare/clef-flash", "@cf/cloudflare/clef"} {
		got, model, err := RouteDecider(name)
		if err != nil || got.ID != "cloudflare-jev" || !strings.HasSuffix(name, model) {
			t.Fatalf("%s: %s %s %v", name, got.ID, model, err)
		}
	}
	if !IsDecider("cloudflare-jev/@cf/cloudflare/clef") {
		t.Fatal("Clef is not a classifier")
	}
	for model, want := range map[string]string{
		"typesafe/jev":              "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run",
		"@cf/cloudflare/clef":       "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run/@cf/cloudflare/clef",
		"@cf/cloudflare/clef-flash": "https://api.cloudflare.com/client/v4/accounts/acc7/ai/run/@cf/cloudflare/clef-flash",
	} {
		if u, err := p.DecideModelURL(context.Background(), model); err != nil || u != want {
			t.Errorf("%s at %s %v", model, u, err)
		}
	}
	// another System One provider's model named so is no Clef
	o := Provider{ID: "o", Name: "O", Key: "k", Decide: "https://api.typesafe.ai/v1", Chat: "https://api.typesafe.ai/v1"}
	if o.DecidesModel("@cf/cloudflare/clef") {
		t.Fatal("a Clef id outside Workers AI decides")
	}
}
