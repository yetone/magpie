package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestTencentTokenPlan(t *testing.T) {
	p, err := FromPreset("tencent-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	// the plan's own endpoints, not TokenHub's pay-as-you-go ones; no
	// Responses, which the plan doesn't serve
	if p.Chat != "https://api.lkeap.cloud.tencent.com/plan/v3" || p.Anthropic != "https://api.lkeap.cloud.tencent.com/plan/anthropic" || p.Responses != "" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	for _, id := range Preset("tencent-token-plan").Models {
		if id != strings.ToLower(id) {
			t.Fatalf("model ids are lowercase on the plan: %q", id)
		}
	}
	// before its list is fetched: the plan's models
	if got := p.planModels(nil); len(got) != len(Preset("tencent-token-plan").Models) || got[0].ID != "tc-code-latest" {
		t.Fatalf("plan's: %+v", got)
	}
	// a list the plan gave is kept whole
	if got := p.planModels([]catalog.Model{{ID: "glm-5.3"}, {ID: "hy3"}}); len(got) != 2 || got[0].ID != "glm-5.3" {
		t.Fatalf("listed: %+v", got)
	}
	// an entry imported from another app at the plan's endpoints is the preset
	im, _ := imported("Tencent", "sk-tp-x", endpoints{anthropic: "https://api.lkeap.cloud.tencent.com/plan/anthropic"}, nil)
	if im.Preset != "tencent-token-plan" || im.Icon != "tencentcloud-color" {
		t.Fatalf("imported: %+v", im)
	}
}

// TokenHub pay as you go (Jorben on Discord): its own hosts, China's and
// Singapore's, each serving chat completions, Responses and Anthropic
// messages, a pair the add sheet shows as one row; the plan stays where it
// was.
func TestTencentTokenHub(t *testing.T) {
	for _, c := range []struct{ id, host string }{
		{"tencent-tokenhub", "https://tokenhub-intl.tencentmaas.com"},
		{"tencent-tokenhub-cn", "https://tokenhub.tencentmaas.com"},
	} {
		p, err := FromPreset(c.id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Chat != c.host+"/v1" || p.Responses != c.host+"/v1" || p.Anthropic != c.host {
			t.Fatalf("%s endpoints: %q %q %q", c.id, p.Chat, p.Responses, p.Anthropic)
		}
		pr := Preset(c.id)
		if pr.Kind != KindVendor || pr.NoKey || pr.NoList || !pr.Hosts || pr.Icon != "tencentcloud-color" || pr.Catalog != "tencent-tokenhub" {
			t.Fatalf("%s: %+v", c.id, pr)
		}
		// an entry imported from another app at its endpoints is the preset,
		// whichever API it was set up on
		for _, e := range []endpoints{{chat: c.host + "/v1/"}, {responses: c.host + "/v1"}, {anthropic: c.host}} {
			im, _ := imported("TokenHub", "k", e, nil)
			if im.Preset != c.id || im.Icon != "tencentcloud-color" {
				t.Fatalf("%s imported from %+v: %+v", c.id, e, im)
			}
		}
	}
	// the China one is the global one's twin, id-cn, as the add sheet pairs them
	if Preset("tencent-tokenhub-cn").Name != Preset("tencent-tokenhub").Name+" (China)" {
		t.Fatal("not a pair")
	}
	// the plan is untouched: its own endpoints, its own models
	if p, _ := FromPreset("tencent-token-plan"); p.Chat != "https://api.lkeap.cloud.tencent.com/plan/v3" || p.Responses != "" {
		t.Fatalf("plan moved: %+v", p)
	}
	if Preset("tencent-tokenhub").Models != nil || Preset("tencent-tokenhub-cn").Models != nil {
		t.Fatal("pay as you go lists its models: none are given")
	}
}

// TokenHub's /v1/models, as its docs show it, asked with the key as a
// Bearer: its language models are kept, the embedding one left out.
func TestTencentTokenHubList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Write([]byte(`{"object":"list","data":[
{"id":"hy3","object":"model","name":"Hy3","created":1783267200,"status":"online"},
{"id":"deepseek-v4-pro","object":"model","name":"DeepSeek-V4-Pro","created":1776960000,"status":"online"},
{"id":"kinfra-text-embedding-0.6b","object":"model","name":"KInfra Embedding","created":1776960000,"status":"online"}]}`))
	}))
	defer srv.Close()
	ms, _, err := catalog.FetchAt(context.Background(), srv.URL+"/v1", "sk-test", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "hy3" || ms[1].ID != "deepseek-v4-pro" {
		t.Fatalf("%+v", ms)
	}
}
