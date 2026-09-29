package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A decision provider (TypeSafe's Jev) is only ever a group's classifier:
// its models aren't in the catalog nor a group's members, and a group's
// effort is picked only by it.
func TestDecider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
