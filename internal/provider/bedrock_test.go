package provider

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestBedrockPreset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p, err := FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	// the runtime's own endpoints: /openai/v1 for chat completions, and
	// /anthropic under which the gateway's /v1/messages is Bedrock's
	if p.Chat != "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1" ||
		p.Anthropic != "https://bedrock-runtime.us-east-1.amazonaws.com/anthropic" || p.Responses != "" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if !p.IsBedrock() || p.Icon != "bedrock-color" {
		t.Fatalf("not taken for Bedrock: %+v", p)
	}
	pr := Preset("bedrock")
	if pr.Regions[0].Chat != pr.Chat || pr.Regions[0].Anthropic != pr.Anthropic {
		t.Fatalf("the first region isn't the default: %+v", pr.Regions[0])
	}
	i := slices.IndexFunc(pr.Regions, func(r Region) bool { return r.ID == "ap-southeast-1" })
	if i < 0 || pr.Regions[i].Anthropic+"/v1/messages" != "https://bedrock-runtime.ap-southeast-1.amazonaws.com/anthropic/v1/messages" ||
		pr.Regions[i].Chat+"/chat/completions" != "https://bedrock-runtime.ap-southeast-1.amazonaws.com/openai/v1/chat/completions" {
		t.Fatalf("Singapore: %+v", pr.Regions)
	}

	// Claude on messages alone, whatever its profile's geography; the
	// rest on chat completions alone
	for model, want := range map[string]Protocol{
		"global.anthropic.claude-opus-5-5":                Anthropic,
		"apac.anthropic.claude-opus-5-5":                  Anthropic,
		"us.anthropic.claude-opus-4-8":                    Anthropic,
		"anthropic.claude-haiku-4-5-20251001-v1:0":        Anthropic,
		"openai.gpt-oss-120b-1:0":                         Chat,
		"qwen.qwen3-coder-480b-a35b-v1:0":                 Chat,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": Anthropic,
	} {
		if got := p.APIs(model); len(got) != 1 || got[0] != want {
			t.Errorf("%s: APIs %v, want %s", model, got, want)
		}
		if got := p.Native(model); got != want {
			t.Errorf("%s: Native %s, want %s", model, got, want)
		}
	}

	// no list to ask: the preset's models, without a request
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != len(pr.Models) || ms[0].ID != "global.anthropic.claude-opus-5-5" {
		t.Fatalf("fetched: %v %+v", err, ms)
	}
	if got := p.Available(); len(got) != len(pr.Models) {
		t.Fatalf("available: %+v", got)
	}
	for _, id := range pr.Models {
		if strings.Contains(id, "claude") && !strings.HasPrefix(id, "global.anthropic.claude-") {
			t.Errorf("a Claude model not as its global profile: %s", id)
		}
	}

	// a provider typed in at Bedrock's host is Bedrock too; another isn't
	if !(Provider{Anthropic: "https://bedrock-runtime.eu-west-1.amazonaws.com/anthropic"}).IsBedrock() {
		t.Error("custom provider at a Bedrock host")
	}
	if (Provider{Anthropic: "https://api.anthropic.com"}).IsBedrock() {
		t.Error("Anthropic taken for Bedrock")
	}

	// an entry imported from another app at the preset's endpoints is the
	// preset; at another region's it keeps its URL, and is Bedrock by host
	im, _ := imported("Bedrock", "k", endpoints{anthropic: "https://bedrock-runtime.us-east-1.amazonaws.com/anthropic"}, nil)
	if im.Preset != "bedrock" {
		t.Fatalf("imported: %+v", im)
	}
	im, _ = imported("Bedrock SG", "k", endpoints{anthropic: "https://bedrock-runtime.ap-southeast-1.amazonaws.com/anthropic"}, nil)
	if im.Anthropic != "https://bedrock-runtime.ap-southeast-1.amazonaws.com/anthropic" || !im.IsBedrock() || im.Native("apac.anthropic.claude-opus-5-5") != Anthropic {
		t.Fatalf("imported at Singapore: %+v", im)
	}
}

// A Bedrock key signs the Anthropic endpoint with x-api-key alone, since
// Bedrock turns away a request that also carries a Bearer (#176), and chat
// completions with a Bearer.
func TestBedrockAuth(t *testing.T) {
	p, _ := FromPreset("bedrock")
	p.Key = "ABSK-key"
	if h := AuthHeaders(p, Anthropic); h["x-api-key"] != "ABSK-key" || len(h) != 1 {
		t.Fatalf("anthropic: %v", h)
	}
	if h := AuthHeaders(p, Chat); h["Authorization"] != "Bearer ABSK-key" {
		t.Fatalf("chat: %v", h)
	}
}

// Bedrock's GPT models turn max_tokens away (#176): its chat test asks
// the reply's length as max_completion_tokens.
func TestBedrockTestAsksCompletionTokens(t *testing.T) {
	p, err := FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	_, body := tinyBody(p, Chat, "global.openai.gpt-6-sol")
	if !strings.Contains(body, `"max_completion_tokens":16`) || strings.Contains(body, `"max_tokens"`) {
		t.Fatalf("body: %s", body)
	}
	_, body = tinyBody(p, Anthropic, "global.anthropic.claude-opus-5-5")
	if !strings.Contains(body, `"max_tokens":16`) {
		t.Fatalf("anthropic body: %s", body)
	}
}
