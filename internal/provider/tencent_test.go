package provider

import (
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
