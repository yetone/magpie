package provider

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestQianfanTokenPlan(t *testing.T) {
	p, err := FromPreset("qianfan-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	// the plan's own endpoints, one base per protocol family; the plan has
	// no model list, so NoList keeps the preset's models as its list
	if p.Chat != "https://qianfan.baidubce.com/v2/tokenplan/personal" || p.Responses != p.Chat ||
		p.Anthropic != "https://qianfan.baidubce.com/anthropic/tokenplan/personal" || !Preset("qianfan-token-plan").NoList {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	for _, id := range Preset("qianfan-token-plan").Models {
		if id != strings.ToLower(id) {
			t.Fatalf("model ids are lowercase on the plan: %q", id)
		}
	}
	// the plan's models, qianfan-code-latest following the console's pick first
	if got := p.planModels(nil); len(got) != len(Preset("qianfan-token-plan").Models) || got[0].ID != "qianfan-code-latest" {
		t.Fatalf("plan's: %+v", got)
	}
	// a list the plan gave is kept whole
	if got := p.planModels([]catalog.Model{{ID: "glm-5.3"}}); len(got) != 1 || got[0].ID != "glm-5.3" {
		t.Fatalf("listed: %+v", got)
	}
	// an entry imported from another app at the plan's endpoints is the preset
	im, _ := imported("Qianfan", "bce-v3/x", endpoints{anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/personal"}, nil)
	if im.Preset != "qianfan-token-plan" || im.Icon != "baiducloud-color" {
		t.Fatalf("imported: %+v", im)
	}
}
