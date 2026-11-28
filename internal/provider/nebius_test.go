package provider

import "testing"

// Nebius Token Factory is a preset (pinmmis on X): its OpenAI-compatible
// endpoint at api.tokenfactory.nebius.com/v1, a key, models.dev's nebius
// entry for names, and an entry imported at that address, or OpenCode's
// nebius key, is the preset.
func TestNebiusPreset(t *testing.T) {
	p, err := FromPreset("nebius")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Nebius Token Factory" || p.Icon != "nebius" || p.Chat != "https://api.tokenfactory.nebius.com/v1" ||
		p.KeysURL != "https://tokenfactory.nebius.com/project/api-keys" {
		t.Fatalf("%+v", p)
	}
	if p.Responses != "" || p.Anthropic != "" {
		t.Fatalf("endpoints its docs don't vouch for are set: %q %q", p.Responses, p.Anthropic)
	}
	pr := Preset("nebius")
	if pr == nil || pr.Kind != KindRelay || pr.NoKey || pr.Catalog != "nebius" || pr.Short != "Nebius" {
		t.Fatalf("%+v", pr)
	}
	if p.BillingKind() != BillingPAYG {
		t.Fatalf("billing: %q, want pay as you go", p.BillingKind())
	}
	im, _ := imported("x", "k", endpoints{chat: "https://api.tokenfactory.nebius.com/v1/"}, nil)
	if im.Preset != "nebius" {
		t.Fatalf("imported: %+v", im)
	}
	if openCodePresets["nebius"] != "nebius" {
		t.Fatalf("OpenCode's nebius id: %q", openCodePresets["nebius"])
	}
}
