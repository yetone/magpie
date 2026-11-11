package provider

import "testing"

// GMI Cloud is a preset (aiandcloud on X): its OpenAI-compatible endpoint
// at api.gmi-serving.com/v1, a key, and an entry imported at that address
// is the preset.
func TestGMICloudPreset(t *testing.T) {
	p, err := FromPreset("gmicloud")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "GMI Cloud" || p.Icon != "gmicloud" || p.Chat != "https://api.gmi-serving.com/v1" || p.KeysURL == "" {
		t.Fatalf("%+v", p)
	}
	if p.Responses != "" || p.Anthropic != "" {
		t.Fatalf("endpoints its docs don't name are set: %q %q", p.Responses, p.Anthropic)
	}
	if pr := Preset("gmicloud"); pr == nil || pr.Kind != KindRelay || pr.NoKey {
		t.Fatalf("%+v", pr)
	}
	im, _ := imported("x", "k", endpoints{chat: "https://api.gmi-serving.com/v1/"}, nil)
	if im.Preset != "gmicloud" {
		t.Fatalf("imported: %+v", im)
	}
}
