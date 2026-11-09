package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ark's System One answers, as ark.cn-beijing.volces.com gave them on
// 2026-11-01 to an Ark API key: magpie's smallest question (AskSystemOne's)
// answered, and the replies for a model that doesn't serve System One and
// for a request without the beta header (a bare 404).
const (
	arkPing        = `{"model":"doubao-seed-2-1-lite-260915","answers":{"ok":{"type":"noul","noul":0.998451238905712}},"usage":{"input_tokens":297,"output_tokens":0}}`
	arkUnsupported = `{"error":{"code":"InvalidParameter","message":"The parameter ` + "`model`" + ` specified in the request is not valid: does not support the SystemOne API. Request id: 021793512605442ffbfb0a147800028083cadce647c2","param":"model","type":"BadRequest"}}`
)

// fakeArk answers for ark.cn-beijing.volces.com: POST /api/v1/systemone
// and /api/v3/systemone with the beta header, as Ark does; everything else
// 404 with no body. It returns the path, header and model of each request.
func fakeArk(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q struct {
			Model string `json:"model"`
		}
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+" "+r.Header.Get("ark-beta-systemone-api")+" "+q.Model)
		mu.Unlock()
		if r.Method != http.MethodPost || r.Header.Get("ark-beta-systemone-api") != "true" ||
			r.URL.Path != "/api/v1/systemone" && r.URL.Path != "/api/v3/systemone" || r.Header.Get("Authorization") != "Bearer ark-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if q.Model != ArkDecision {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, arkUnsupported)
			return
		}
		io.WriteString(w, arkPing)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = tr
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func arkHome(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
}

// axelburks on Discord: Volcengine Ark serves System One in beta, at
// /api/v1/systemone behind the ark-beta-systemone-api header. Its preset
// asks doubao-seed-2-1-lite-260915 there with the header, and is offered
// to routing groups as their classifier.
func TestArkDecisionPreset(t *testing.T) {
	arkHome(t)
	asked := fakeArk(t)
	p, err := FromPreset("volcengine-decision")
	if err != nil {
		t.Fatal(err)
	}
	if !p.DecideOnly() || p.Decide != "https://ark.cn-beijing.volces.com/api/v1" || p.DecideVia() != ViaSystemOne || p.Jev() != ArkDecision {
		t.Fatalf("preset: %+v, asked as %s", p, p.Jev())
	}
	p.Key = "ark-1"
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if rs := saved.Test(context.Background()); len(rs) != 1 || !rs[0].OK || rs[0].Model != ArkDecision {
		t.Fatalf("test: %+v", rs)
	}
	if got := asked(); len(got) != 1 || got[0] != "POST /api/v1/systemone true "+ArkDecision {
		t.Fatalf("Ark asked: %q", got)
	}
	if ds := Deciders(); len(ds) != 1 || ds[0].ID != id+"/"+ArkDecision {
		t.Fatalf("classifiers: %+v", ds)
	}
	ms := saved.decideModels()
	if len(ms) != 1 || ms[0].ImageInput == nil || *ms[0].ImageInput {
		t.Fatalf("decision models: %+v", ms)
	}
	for _, e := range providerEntries() {
		if e.Provider.ID == id {
			t.Fatalf("an agent is offered %s", e.ID)
		}
	}
	if got, m, err := RouteDecider(ArkDecision); err != nil || got.ID != id || m != ArkDecision {
		t.Fatalf("route: %s %s %v", got.ID, m, err)
	}
	// the editor's probe, its base pasted whole, and a model Ark doesn't
	// serve System One with, its refusal said as Ark said it
	q := Provider{Key: "ark-1", Decide: "https://ark.cn-beijing.volces.com/api/v3/systemone"}
	if d := q.DetectDecide(context.Background(), ""); !d.OK || d.Model != ArkDecision || d.Base != "https://ark.cn-beijing.volces.com/api/v3" {
		t.Fatalf("probe: %+v", d)
	}
	if d := q.DetectDecide(context.Background(), "doubao-seed-2-1-pro-260628"); d.OK || !strings.Contains(d.Error, "does not support the SystemOne API") {
		t.Fatalf("probe of another model: %+v", d)
	}
}

// The beta header goes to Ark's host alone, and an Ark provider that
// serves conversations too keeps its decision model apart from them.
func TestArkHeaderScopedToArk(t *testing.T) {
	for base, want := range map[string]string{
		"https://ark.cn-beijing.volces.com/api/v1":          "true",
		"https://ark.ap-southeast.volces.com/api/v3":        "true",
		"https://api.typesafe.ai/v1":                        "",
		"https://openrouter.ai/api/v1":                      "",
		"https://token-plan.cn-beijing.maas.aliyuncs.com/x": "",
	} {
		h := http.Header{}
		Provider{Decide: base}.DecideHeaders(h)
		if got := h.Get(ArkSystemOneHeader); got != want {
			t.Errorf("%s: header %q", base, got)
		}
	}
	mixed := Provider{ID: "ark", Chat: "https://ark.cn-beijing.volces.com/api/v3", Decide: "https://ark.cn-beijing.volces.com/api/v3", Models: []string{"glm-4-7-251222"}}
	if !mixed.DecidesModel(ArkDecision) || mixed.DecidesModel("glm-4-7-251222") {
		t.Fatal("an Ark chat model taken for its decision model, or the other way")
	}
	if ms := mixed.DecisionModels(); len(ms) != 1 || ms[0].ID != ArkDecision {
		t.Fatalf("decision models: %+v", ms)
	}
}

// A user on Discord found no System One endpoint for Volcengine Ark, its
// preset's or a custom one (OpenAI Responses), and none of its models in
// a routing group's classifier picker. Ark's pay as you go answers System
// One on the same key at …/api/v1: a provider asked there, of either
// protocol, offers Ark's decision model with no URL typed, its chat models
// picked or not. The Coding and Agent Plans answer it nothing, and a URL
// the user gave is kept.
func TestArkPayAsYouGoDecides(t *testing.T) {
	arkHome(t)
	const v3, v1 = "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v1"
	for _, p := range []Provider{
		{ID: "ark-responses", Name: "Ark Responses", Responses: v3 + "/", Key: "ark-1", Models: []string{"doubao-seed-2-1-pro-260628"}},
		{ID: "ark-chat", Name: "Ark Chat", Chat: v3, Key: "ark-1"},
		{ID: "ark-preset", Name: "Volcengine Ark", Preset: "volcengine", Chat: v3, Responses: v3, Key: "ark-1", Models: []string{"glm-4-7-251222"}},
		{ID: "ark-coding", Name: "Ark Coding", Preset: "volcengine", Chat: "https://ark.cn-beijing.volces.com/api/coding/v3", Responses: "https://ark.cn-beijing.volces.com/api/coding/v3", Key: "ark-1", Models: []string{"glm-4-7-251222"}},
		{ID: "ark-agent", Name: "Ark Agent", Chat: "https://ark.cn-beijing.volces.com/api/plan/v3", Key: "ark-1"},
		{ID: "ark-own", Name: "Ark Own", Chat: v3, Decide: "https://ark.ap-southeast.volces.com/api/v3", Key: "ark-1"},
		{ID: "other", Name: "Other", Responses: "https://api.example.com/api/v3", Key: "k"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"ark-responses": v1, "ark-chat": v1, "ark-preset": v1, "ark-coding": "", "ark-agent": "", "ark-own": "https://ark.ap-southeast.volces.com/api/v3", "other": ""}
	for id, d := range want {
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Decide != d {
			t.Errorf("%s: System One at %q, want %q", id, p.Decide, d)
		}
	}
	var got []string
	for _, e := range OfferedDeciders(nil) {
		got = append(got, e.ID)
	}
	slices.Sort(got)
	if w := []string{"ark-chat/" + ArkDecision, "ark-own/" + ArkDecision, "ark-preset/" + ArkDecision, "ark-responses/" + ArkDecision}; !slices.Equal(got, w) {
		t.Fatalf("classifiers offered: %q, want %q", got, w)
	}
}
