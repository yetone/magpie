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

// Alibaba Cloud's Token Plan (Jeremy on Discord): its own host, apart from
// DashScope's pay as you go, with chat completions and Anthropic messages.
func TestQwenTokenPlan(t *testing.T) {
	p, err := FromPreset("qwen-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1" || p.Anthropic != "https://token-plan.maas.qianwenaiapi.com/apps/anthropic" || p.Responses != "" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if got := p.planModels(nil); len(got) != len(Preset("qwen-token-plan").Models) || got[1].ID != "qwen4-max" {
		t.Fatalf("plan's: %+v", got)
	}
	im, _ := imported("Qwen", "sk-sp-x", endpoints{chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"}, nil)
	if im.Preset != "qwen-token-plan" {
		t.Fatalf("imported: %+v", im)
	}
}

// Bailian's Token Plan (#1506, IamMiao), the same plan as the Qwen AI
// platform's but sold on Alibaba Cloud, at its own host: chat completions
// and Anthropic messages as token-plan-team-quickstart gives them, and its
// decision model on System One beside them, as token-plan-decision-model's
// curl asks it (POST …/compatible-mode/v1/systemone, Bearer sk-sp-…,
// "model": "decision-model-preview"). The host is a stand-in serving those
// docs' bytes: no real Bailian Token Plan key was tried.
func TestBailianTokenPlan(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	p, err := FromPreset("bailian-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	if p.Chat != root+"/compatible-mode/v1" || p.Anthropic != root+"/apps/anthropic" || p.Responses != "" || p.Decide != root+"/compatible-mode/v1" || p.DecideOnly() {
		t.Fatalf("endpoints: %+v", p)
	}
	if u, err := p.DecideModelURL(context.Background(), BailianDecision); err != nil || u != root+"/compatible-mode/v1/systemone" {
		t.Fatalf("asked at %q %v", u, err)
	}
	if pr := Preset("bailian-token-plan"); pr.Icon != "bailian-color" || !slices.Equal(pr.Models, Preset("qwen-token-plan").Models) {
		t.Fatalf("preset: %+v", pr)
	}
	if got := p.planModels(nil); len(got) != len(alibabaPlanModels) || got[1].ID != "qwen4-max" {
		t.Fatalf("plan's: %+v", got)
	}
	im, _ := imported("Bailian", "sk-sp-x", endpoints{chat: root + "/compatible-mode/v1"}, nil)
	if im.Preset != "bailian-token-plan" {
		t.Fatalf("imported: %+v", im)
	}

	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Host") != "token-plan.cn-beijing.maas.aliyuncs.com" || r.Method != http.MethodPost || r.URL.Path != "/compatible-mode/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-sp-1" {
			// token-plan-personal-faq's for a key of another plan
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"Invalid API-key provided.","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		}
		var q struct {
			Model     string                    `json:"model"`
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil || q.Model != BailianDecision || len(q.State) == 0 || len(q.Questions) == 0 {
			// the docs: 422 is a body that fails validation
			w.WriteHeader(422)
			return
		}
		asked++
		// answers under the questions' own keys, each with choice,
		// probabilities and confidence, as the docs read them back
		w.Write([]byte(`{"answers":{"ok":{"choice":"yes","probabilities":{"yes":0.97,"no":0.03},"confidence":0.97}}}`))
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	defer func() { http.DefaultClient.Transport = old }()

	p.Key = "sk-sp-1"
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Jev() != BailianDecision || !saved.DecidesModel(BailianDecision) || saved.DecidesModel("qwen3.8-max") || saved.DecidesModel(JevLatest) {
		t.Fatalf("decides: %s", saved.Jev())
	}
	if ms := saved.DecisionModels(); len(ms) != 1 || ms[0].ID != BailianDecision {
		t.Fatalf("decision models: %+v", ms)
	}
	ms, err := saved.fetchDecide(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != BailianDecision || asked != 1 {
		t.Fatalf("asked: %+v %v (%d)", ms, err, asked)
	}
	if ds := Deciders(); len(ds) != 1 || ds[0].ID != id+"/"+BailianDecision {
		t.Fatalf("classifiers: %+v", ds)
	}
	if got, m, err := RouteDecider(id); err != nil || got.ID != id || m != BailianDecision {
		t.Fatalf("route %s: %s %s %v", id, got.ID, m, err)
	}
	for _, e := range providerEntries() {
		if e.Provider.ID == id && e.Model == BailianDecision {
			t.Fatalf("an agent is offered %s", e.ID)
		}
	}
	saved.Key = "sk-sp-2"
	if _, err := saved.fetchDecide(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid API-key") {
		t.Fatalf("a wrong key: %v", err)
	}
}

// The decision model on the Qwen AI platform's pay as you go (#1506), at
// maas.qianwenaiapi.com as decision-model-preview's page gives it, apart
// from Bailian's, which shows Bailian's icon.
func TestQwenDecisionPreset(t *testing.T) {
	p, err := FromPreset("qwen-decision")
	if err != nil {
		t.Fatal(err)
	}
	if !p.DecideOnly() || p.Jev() != BailianDecision || p.DecideVia() != ViaSystemOne {
		t.Fatalf("preset: %+v", p)
	}
	if u, err := p.DecideModelURL(context.Background(), BailianDecision); err != nil || u != "https://maas.qianwenaiapi.com/compatible-mode/v1/systemone" {
		t.Fatalf("asked at %q %v", u, err)
	}
	if Preset("qwen-decision").Icon != "qwen-color" || Preset("bailian-decision").Icon != "bailian-color" {
		t.Fatal("icons")
	}
}

// Qwen Cloud (judi0713 on Discord), Alibaba's international Qwen platform:
// pay as you go at {region}.maas.qwencloudapi.com in six regions and its
// Token Plan at token-plan.maas.qwencloudapi.com, each on keys of its own,
// as docs.qwencloud.com's "Regions and endpoints" gives them. No real Qwen
// Cloud key was tried.
func TestQwenCloud(t *testing.T) {
	p, err := FromPreset("qwencloud-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://token-plan.maas.qwencloudapi.com/compatible-mode/v1" || p.Anthropic != "https://token-plan.maas.qwencloudapi.com/apps/anthropic" {
		t.Fatalf("plan's endpoints: %q %q", p.Chat, p.Anthropic)
	}
	if got := p.planModels(nil); len(got) != len(Preset("qwencloud-token-plan").Models) || !slices.ContainsFunc(got, func(m catalog.Model) bool { return m.ID == "qwen3.8-max" }) {
		t.Fatalf("plan's: %+v", got)
	}
	im, _ := imported("Qwen Cloud", "sk-sp-x", endpoints{chat: "https://token-plan.maas.qwencloudapi.com/compatible-mode/v1"}, nil)
	if im.Preset != "qwencloud-token-plan" {
		t.Fatalf("plan imported as %q", im.Preset)
	}

	p, err = FromPreset("qwencloud")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://ap-southeast-1.maas.qwencloudapi.com/compatible-mode/v1" || p.Anthropic != "https://ap-southeast-1.maas.qwencloudapi.com/apps/anthropic" {
		t.Fatalf("pay as you go's endpoints: %q %q", p.Chat, p.Anthropic)
	}
	pr := Preset("qwencloud")
	var ids []string
	for _, r := range pr.Regions {
		ids = append(ids, r.ID)
		if r.Chat != "https://"+r.ID+".maas.qwencloudapi.com/compatible-mode/v1" || r.Anthropic != "https://"+r.ID+".maas.qwencloudapi.com/apps/anthropic" {
			t.Errorf("region %s: %q %q", r.ID, r.Chat, r.Anthropic)
		}
	}
	if want := []string{"ap-southeast-1", "us-east-1", "eu-central-1", "ap-northeast-1", "cn-hongkong", "cn-beijing"}; !slices.Equal(ids, want) {
		t.Fatalf("regions %v, want %v", ids, want)
	}
	if pr.Regions[0].Chat != pr.Chat {
		t.Fatal("the first region isn't the default")
	}
	for _, chat := range []string{"https://eu-central-1.maas.qwencloudapi.com/compatible-mode/v1", "https://ap-southeast-1.maas.qwencloudapi.com/compatible-mode/v1"} {
		im, _ := imported("Qwen Cloud", "sk-ws-x", endpoints{chat: chat}, nil)
		if im.Preset != "qwencloud" || im.Chat != chat {
			t.Fatalf("%s imported as %q at %q", chat, im.Preset, im.Chat)
		}
	}
	q, err := ParseImport("magpie://import?preset=qwencloud&region=us-east-1")
	if err != nil || q.Chat != "https://us-east-1.maas.qwencloudapi.com/compatible-mode/v1" {
		t.Fatalf("import link: %+v (%v)", q, err)
	}
}
