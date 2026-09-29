package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestQianfanTokenPlan(t *testing.T) {
	p, err := FromPreset("baidu-qianfan")
	if err != nil {
		t.Fatal(err)
	}
	// the plan's own endpoints, one base per protocol family; the plans
	// serve no model list, so the preset's models are the picker's list
	if p.Chat != "https://qianfan.baidubce.com/v2/tokenplan/personal" || p.Responses != p.Chat ||
		p.Anthropic != "https://qianfan.baidubce.com/anthropic/tokenplan/personal" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	// the personal and the enterprise plan and pay as you go, each plan's
	// bases under its own path; pay as you go at the v2 root serves its
	// model list, and its keys are made on the IAM page
	for _, r := range Preset("baidu-qianfan").Regions {
		var chat, anthropic string
		lists := r.ID == "api"
		switch r.ID {
		case "personal":
			chat, anthropic = "https://qianfan.baidubce.com/v2/tokenplan/personal", "https://qianfan.baidubce.com/anthropic/tokenplan/personal"
		case "team":
			chat, anthropic = "https://qianfan.baidubce.com/v2/tokenplan/team", "https://qianfan.baidubce.com/anthropic/tokenplan/team"
		case "api":
			chat, anthropic = "https://qianfan.baidubce.com/v2", "https://qianfan.baidubce.com/anthropic"
		default:
			t.Fatalf("region: %+v", r)
		}
		if r.Chat != chat || r.Responses != chat || r.Anthropic != anthropic || r.Lists != lists {
			t.Fatalf("region %s: %q %q %q lists=%v", r.ID, r.Chat, r.Responses, r.Anthropic, r.Lists)
		}
		if r.ID == "api" && r.KeysURL != "https://console.bce.baidu.com/iam/#/iam/apikey/list" {
			t.Fatalf("pay as you go keys: %q", r.KeysURL)
		}
	}
	if len(Preset("baidu-qianfan").Regions) != 3 {
		t.Fatalf("regions: %d", len(Preset("baidu-qianfan").Regions))
	}
	for _, id := range Preset("baidu-qianfan").Models {
		if id != strings.ToLower(id) {
			t.Fatalf("model ids are lowercase on the plan: %q", id)
		}
	}
	// the plan's models, qianfan-code-latest following the console's pick first
	if got := p.planModels(nil); len(got) != len(Preset("baidu-qianfan").Models) || got[0].ID != "qianfan-code-latest" {
		t.Fatalf("plan's: %+v", got)
	}
	// a list the plan gave is kept whole
	if got := p.planModels([]catalog.Model{{ID: "glm-5.3"}}); len(got) != 1 || got[0].ID != "glm-5.3" {
		t.Fatalf("listed: %+v", got)
	}
	// an entry imported from another app at the plan's endpoints is the preset
	im, _ := imported("Qianfan", "bce-v3/x", endpoints{anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/personal"}, nil)
	if im.Preset != "baidu-qianfan" || im.Icon != "baiducloud-color" {
		t.Fatalf("imported: %+v", im)
	}
	// the id the preset carried its first day still names it, and a
	// provider saved under it is the preset since renamed
	old, err := FromPreset("qianfan-token-plan")
	if err != nil || old.ID != "baidu-qianfan" {
		t.Fatalf("old id: %v %+v", err, old)
	}
	if p := normalize(Provider{Preset: "qianfan-token-plan"}); p.Preset != "baidu-qianfan" {
		t.Fatalf("normalized: %+v", p)
	}
	// a plan's Get-a-key link is the preset's own page, pay as you go's
	// the IAM page its keys are made on, taken from the region its
	// endpoints sit at
	planAt := normalize(Provider{Preset: "baidu-qianfan",
		Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal", Anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/personal"})
	if planAt.KeysURL != Preset("baidu-qianfan").KeysURL {
		t.Fatalf("plan keys: %q", planAt.KeysURL)
	}
	apiAt := normalize(Provider{Preset: "baidu-qianfan",
		Chat: "https://qianfan.baidubce.com/v2", Anthropic: "https://qianfan.baidubce.com/anthropic"})
	if apiAt.KeysURL != "https://console.bce.baidu.com/iam/#/iam/apikey/list" {
		t.Fatalf("pay as you go keys: %q", apiAt.KeysURL)
	}
}

// The plans answer no /models of their own, so a provider at their
// endpoints gets the preset's models with no request and no error, while
// pay as you go at the v2 root is asked and its list kept whole.
func TestQianfanPlansListNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		if r.URL.Path == "/v2/models" {
			w.Write([]byte(`{"object":"list","data":[{"id":"ernie-x1.1"},{"id":"glm-5.3"},{"id":"kimi-k2.6"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	base, err := FromPreset("baidu-qianfan")
	if err != nil {
		t.Fatal(err)
	}
	base.Key = "bce-v3/x"

	plan := base
	plan.Chat, plan.Responses = srv.URL+"/v2/tokenplan/personal", srv.URL+"/v2/tokenplan/personal"
	plan.Anthropic = srv.URL+"/anthropic/tokenplan/personal"
	ms, err := plan.Fetch(context.Background())
	if err != nil {
		t.Fatalf("plan fetch: %v", err)
	}
	if len(ms) != len(Preset("baidu-qianfan").Models) || asked != 0 {
		t.Fatalf("plan: %d models, %d requests", len(ms), asked)
	}

	api := base
	api.Chat, api.Responses = srv.URL+"/v2", srv.URL+"/v2"
	api.Anthropic = srv.URL+"/anthropic"
	if ms, err := api.Fetch(context.Background()); err != nil || len(ms) != 3 || atomic.LoadInt32(&asked) == 0 {
		t.Fatalf("pay as you go: %d models, %d requests, err %v", len(ms), asked, err)
	}
}
