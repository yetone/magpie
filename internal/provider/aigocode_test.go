package provider

import (
	"strings"
	"testing"
)

// AIGoCode is a preset that takes a key (MindfulReturn on X, 11-14: "do I
// have to add the provider myself?"): its endpoints are the ones its own
// docs give (www.aigocode.net/docs/api: OpenAI SDKs at /v1, Anthropic's at
// the root), it is a relay, so a key's balance is looked for at sub2api's
// /v1/usage, and its keys draw on a plan before the balance, so magpie
// says neither.
func TestAIGoCodePresetTakesAKey(t *testing.T) {
	pr := Preset("aigocode")
	if pr == nil {
		t.Fatal("no aigocode preset")
	}
	if pr.Kind != KindRelay || pr.NoKey || pr.Sponsored {
		t.Errorf("kind %s, noKey %v, sponsored %v: want a relay taking a key, listed as any other", pr.Kind, pr.NoKey, pr.Sponsored)
	}
	if pr.Chat != "https://api.aigocode.dev/v1" || pr.Responses != "https://api.aigocode.dev/v1" || pr.Anthropic != "https://api.aigocode.dev" {
		t.Errorf("endpoints %q %q %q", pr.Chat, pr.Responses, pr.Anthropic)
	}
	if pr.KeysURL != "https://www.aigocode.net/dashboard/keys" {
		t.Errorf("keys page %q", pr.KeysURL)
	}
	p := Provider{ID: "aigocode", Preset: "aigocode", Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic, Key: "sk-k"}
	if !probesBalance(p) {
		t.Error("its balance is not looked for")
	}
	if got := strings.Join(probeOrigins(p), ","); got != "https://api.aigocode.dev" {
		t.Errorf("balance looked for at %q", got)
	}
	if b := p.BillingKind(); b != "" {
		t.Errorf("billing %q, want unsaid (plan first, then balance, on one key)", b)
	}
}
