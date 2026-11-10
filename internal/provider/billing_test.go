package provider

import (
	"strings"
	"testing"
)

// billAt is a provider of preset id sitting at its region rid's endpoints.
func billAt(t *testing.T, id, rid string) Provider {
	t.Helper()
	pr := Preset(id)
	if pr == nil {
		t.Fatalf("no preset %s", id)
	}
	p := Provider{ID: id, Name: pr.Name, Preset: id, Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic, Key: "k"}
	for _, r := range pr.Regions {
		if r.ID == rid {
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
			return p
		}
	}
	if rid != "" {
		t.Fatalf("%s has no region %s", id, rid)
	}
	return p
}

// A provider says whether it is on a plan or pay as you go (EV_John_Liu
// on X): by the endpoints it sits at, a signed-in account, a plan's own
// preset, or the user's word, which stands.
func TestBillingKind(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want string
	}{
		{"GLM Coding Plan", billAt(t, "zhipu", "coding"), BillingPlan},
		{"GLM pay as you go", billAt(t, "zhipu", "api"), BillingPAYG},
		{"Step Plan", billAt(t, "stepfun", "plan"), BillingPlan},
		{"Kimi Code", billAt(t, "kimi-code", ""), BillingPlan},
		{"OpenRouter", billAt(t, "openrouter", ""), BillingPAYG},
		{"DeepSeek", billAt(t, "deepseek", ""), BillingPAYG},
		{"MiniMax: one endpoint for both", billAt(t, "minimax", ""), ""},
		{"Ollama on this computer", billAt(t, "ollama", ""), ""},
		{"added by its address", Provider{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k"}, ""},
		{"signed-in account", Provider{ID: "codex", Name: "Codex", Account: &Account{}}, BillingPlan},
		{"user says pay as you go", func() Provider { p := billAt(t, "zhipu", "coding"); p.Billing = BillingPAYG; return p }(), BillingPAYG},
		{"user says plan of a relay", func() Provider { p := billAt(t, "openrouter", ""); p.Billing = BillingPlan; return p }(), BillingPlan},
		{"user's word on an unknown one", Provider{ID: "x", Name: "X", Chat: "https://x.example/v1", Billing: BillingPlan}, BillingPlan},
	}
	for _, c := range cases {
		if got := c.p.BillingKind(); got != c.want {
			t.Errorf("%s: BillingKind = %q, want %q", c.name, got, c.want)
		}
	}
}

// Volcengine at its Coding Plan with a pay-as-you-go key beside (yckonnnn
// on X) is both; with only another plan's key, a plan still.
func TestBillingKindMixedKeys(t *testing.T) {
	if got := volcMixed().BillingKind(); got != BillingMixed {
		t.Fatalf("coding plan + pay-as-you-go key: %q, want mixed", got)
	}
	p := volcMixed()
	p.Keys = []KeyAccount{{Key: "k-agent", Plan: "agent"}}
	if got := p.BillingKind(); got != BillingPlan {
		t.Fatalf("coding + agent plan keys: %q, want plan", got)
	}
	p.Keys = []KeyAccount{{Key: "k-api", Plan: "api", Off: true}}
	if got := p.BillingKind(); got != BillingPlan {
		t.Fatalf("pay-as-you-go key switched off: %q, want plan", got)
	}
}

// Every preset region named as a plan or as pay as you go reads so at its
// own endpoints, and the lists name presets that exist.
func TestBillingEveryRegion(t *testing.T) {
	for id := range planPresets {
		if Preset(id) == nil {
			t.Errorf("planPresets names no preset %s", id)
		}
	}
	for id := range unsaidPresets {
		if Preset(id) == nil {
			t.Errorf("unsaidPresets names no preset %s", id)
		}
	}
	for _, pr := range Presets() {
		if unsaidPresets[pr.ID] || strings.HasSuffix(pr.ID, "-decision") || (pr.Kind != KindVendor && pr.Kind != KindRelay) {
			continue
		}
		for _, r := range pr.Regions {
			p := billAt(t, pr.ID, r.ID)
			if p.regionOf(&pr) == nil || p.regionOf(&pr).ID != r.ID {
				continue // two regions at one endpoint: told apart elsewhere
			}
			want := regionBilling(&r)
			if want == "" {
				want = BillingPAYG
			}
			if planPresets[pr.ID] {
				want = BillingPlan
			}
			if got := p.BillingKind(); got != want {
				t.Errorf("%s at %s (%q): %q, want %q", pr.ID, r.ID, r.Name, got, want)
			}
		}
	}
}
