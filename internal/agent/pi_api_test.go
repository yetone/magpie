package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Pi asks each model on the API its provider serves it on, so the gateway
// relays the request as it is: a Responses-only provider's models and GPT
// on OpenAI's API go on openai-responses; a Messages-only provider's on
// anthropic-messages at the gateway; a Chat provider's stay on the
// provider's openai-completions. A group goes on Responses when a member
// is best there and every other serves it too (#1594, zyh6036 on X); any
// other group stays on Chat.
func TestPiModelsAskedOnTheirNativeAPI(t *testing.T) {
	home := syncHome(t)
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5", "gpt-5.5"}},
		{ID: "openai", Name: "OpenAI", Key: "k", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-5.5", "whisper-x"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5", "kimi-k3"}},
		{ID: "gem", Name: "Gemini", Key: "k", Gemini: "http://127.0.0.1:1/v1beta", Models: []string{"gemini-3-flash"}},
		{ID: "hybrid", Name: "Hybrid", Key: "k", Chat: "http://127.0.0.1:1/v1", Gemini: "http://127.0.0.1:1/v1beta", Models: []string{"gemini-3-flash", "gemini-2.5-pro"}},
		// a relay serving Chat and Responses, best on Chat
		{ID: "both", Name: "Both", Key: "k", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelAPI("hybrid/gemini-3-flash", "gemini"); err != nil {
		t.Fatal(err)
	}
	// a group goes on Responses when a member is best served there and
	// every other serves it too (#1594): one with a Chat-only member, or of
	// Messages ones, stays on Chat
	for _, g := range []provider.Group{
		{ID: "gpts", Name: "GPTs", Members: []string{"resp/gpt-5.5", "openai/gpt-5.5", "resp/grok-5"}},
		{ID: "mixed", Name: "Mixed", Members: []string{"resp/grok-5", "relay/glm-4.6"}},
		{ID: "claudes", Name: "Claudes", Members: []string{"anth/claude-sonnet-5", "anth/kimi-k3"}},
		{ID: "outer", Name: "Outer", Members: []string{"group/gpts", "openai/gpt-5.5"}},
		{ID: "geminis", Name: "Geminis", Members: []string{"gem/gemini-3-flash"}},
		// zyh6036 on X: every member serves Responses, one is best there
		{ID: "subs", Name: "Subs", Members: []string{"both/gpt-5.5", "resp/gpt-5.5"}},
		// none is best there: each stays relayed on Chat
		{ID: "chatfirst", Name: "Chat first", Members: []string{"both/gpt-5.5"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := pi(home).Field("model").Set("magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal([]byte(readFile(filepath.Join(home, ".pi", "agent", "models.json"))), &file); err != nil {
		t.Fatal(err)
	}
	pm := file["providers"].(map[string]any)["magpie"].(map[string]any)
	if pm["api"] != "openai-completions" || pm["baseUrl"] != gatewayV1() {
		t.Fatalf("provider: %v", pm)
	}
	apis, entries := map[string]any{}, map[string]map[string]any{}
	for _, raw := range pm["models"].([]any) {
		m := raw.(map[string]any)
		apis[m["id"].(string)], entries[m["id"].(string)] = m["api"], m
	}
	for id, want := range map[string]any{
		"resp/grok-5":          "openai-responses",
		"resp/gpt-5.5":         "openai-responses",
		"openai/gpt-5.5":       "openai-responses",
		"openai/whisper-x":     nil,
		"relay/glm-4.6":        nil,
		"anth/claude-sonnet-5": "anthropic-messages",
		"anth/kimi-k3":         "anthropic-messages",
		// Pi's folder keeps Gemini on Chat: Raft runs Pi's SDK on it with
		// a fetch pi-ai's Google adapter refuses (piAsked); OmO, below,
		// asks Gemini's API
		"gem/gemini-3-flash":    nil,
		"hybrid/gemini-3-flash": nil,
		"hybrid/gemini-2.5-pro": nil,
		"auto/gpt-5.5":          "openai-responses",
		"group/gpts":            "openai-responses",
		"group/outer":           "openai-responses",
		"group/mixed":           nil,
		"group/claudes":         nil,
		"group/geminis":         nil,
		"both/gpt-5.5":          nil,
		"group/subs":            "openai-responses",
		"group/chatfirst":       nil,
	} {
		got, ok := apis[id]
		if !ok {
			t.Errorf("%s not listed: %v", id, apis)
		} else if got != want {
			t.Errorf("%s: api %v, want %v", id, got, want)
		}
	}
	// Anthropic's SDK posts to baseUrl + /v1/messages; only the Claude
	// that refuses a thinking budget is told to think adaptively
	for id, adaptive := range map[string]bool{"anth/claude-sonnet-5": true, "anth/kimi-k3": false} {
		e := entries[id]
		if e["baseUrl"] != gateway.URL() {
			t.Errorf("%s: baseUrl %v, want %s", id, e["baseUrl"], gateway.URL())
		}
		c, _ := e["compat"].(map[string]any)
		if got := c["forceAdaptiveThinking"] == true; got != adaptive {
			t.Errorf("%s: compat %v", id, e["compat"])
		}
	}
	if _, ok := entries["resp/grok-5"]["baseUrl"]; ok {
		t.Errorf("a Responses model keeps the provider's baseUrl: %v", entries["resp/grok-5"])
	}
	for _, id := range []string{"gem/gemini-3-flash", "hybrid/gemini-3-flash"} {
		if e := entries[id]; e["baseUrl"] != nil {
			t.Errorf("Pi: %s baseUrl: %v", id, e)
		}
	}
	if err := omo(home).Field("model").Set("magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	var omoFile map[string]any
	if err := json.Unmarshal([]byte(readFile(filepath.Join(home, ".omo", "agent", "models.json"))), &omoFile); err != nil {
		t.Fatal(err)
	}
	omoAPIs := map[string]map[string]any{}
	for _, raw := range omoFile["providers"].(map[string]any)["magpie"].(map[string]any)["models"].([]any) {
		m := raw.(map[string]any)
		omoAPIs[m["id"].(string)] = m
	}
	for id, want := range map[string]any{"gem/gemini-3-flash": "google-generative-ai", "hybrid/gemini-3-flash": "google-generative-ai", "hybrid/gemini-2.5-pro": nil, "group/geminis": nil} {
		e, ok := omoAPIs[id]
		if !ok {
			t.Errorf("OmO: %s not listed", id)
			continue
		}
		if e["api"] != want {
			t.Errorf("OmO: %s: api %v, want %v", id, e["api"], want)
		}
		if want != nil && e["baseUrl"] != gateway.URL()+"/v1beta" {
			t.Errorf("OmO: %s baseUrl: %v", id, e)
		}
	}
}
