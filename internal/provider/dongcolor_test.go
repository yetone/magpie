package provider

import "testing"

// DongColor is JD's internal LLM gateway: OpenAI-compatible chat at
// llm-gw.jd.local, key pasted by the user, everything else from the preset.
func TestDongColorPreset(t *testing.T) {
	pr := Preset("dongcolor")
	if pr == nil {
		t.Fatal("no dongcolor preset")
	}
	if pr.Name != "DongColor" || pr.Kind != KindRelay || pr.NoKey || pr.Icon != "dongcolor-color" {
		t.Fatalf("%+v", pr)
	}
	if pr.Chat != "http://llm-gw.jd.local/v1" || pr.Responses != "" || pr.Anthropic != "" {
		t.Fatalf("endpoints: chat=%q responses=%q anthropic=%q", pr.Chat, pr.Responses, pr.Anthropic)
	}
	p, err := FromPreset("dongcolor")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "dongcolor" || p.Preset != "dongcolor" || p.Chat != pr.Chat || p.Name != "DongColor" {
		t.Fatalf("%+v", p)
	}
	im, why := imported("x", "k", endpoints{chat: "http://llm-gw.jd.local/v1"}, nil)
	if why != "" {
		t.Fatal(why)
	}
	if im.Preset != "dongcolor" || im.Icon != "dongcolor-color" {
		t.Fatalf("imported: %+v", im)
	}
}
