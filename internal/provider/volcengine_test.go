package provider

import (
	"strings"
	"testing"
)

func TestVolcengineArkPlans(t *testing.T) {
	p, err := FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	// a Coding Plan's own endpoints by default: its quota isn't spent at
	// Ark's pay-as-you-go /api/v3
	if p.Chat != "https://ark.cn-beijing.volces.com/api/coding/v3" || p.Responses != p.Chat || p.Anthropic != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	pr := Preset("volcengine")
	if len(pr.Regions) != 3 || pr.Regions[0].Chat != pr.Chat || pr.Regions[0].Anthropic != pr.Anthropic ||
		pr.Regions[1].ID != "agent" || pr.Regions[1].Chat != "https://ark.cn-beijing.volces.com/api/plan/v3" || pr.Regions[1].Anthropic != "https://ark.cn-beijing.volces.com/api/plan" ||
		pr.Regions[2].Chat != "https://ark.cn-beijing.volces.com/api/v3" || pr.Regions[2].Anthropic != "" {
		t.Fatalf("plans: %+v", pr.Regions)
	}
	for _, id := range pr.Models {
		if id != strings.ToLower(id) {
			t.Fatalf("model ids are lowercase, as the quick-start page has them: %q", id)
		}
	}
	if got := p.planModels(nil); len(got) != len(pr.Models) || got[0].ID != "ark-code-latest" {
		t.Fatalf("plan's: %+v", got)
	}
	// an entry imported from another app at an Ark endpoint is known as Ark
	im, _ := imported("Ark", "k", endpoints{chat: "https://ark.cn-beijing.volces.com/api/plan/v3"}, nil)
	if im.Icon != "volcengine-color" || im.Chat != "https://ark.cn-beijing.volces.com/api/plan/v3" {
		t.Fatalf("imported: %+v", im)
	}
}
