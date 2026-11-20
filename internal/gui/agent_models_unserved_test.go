package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group an agent's model picker doesn't offer comes with the
// field, with why, as the agent's picker would take it: Codex's and
// Claude Code's group/<id>, OpenCode's magpie/group/<id>. One switched off can't
// be picked; one only the agent's lists leave out (shown only its picks)
// can, and is put back in them (@CHENGJIAHEGOXIS on X: DeepSeek-API-Agents
// was in no section of Codex's picker, without a word).
func TestModelFieldSaysWhichGroupsItLacks(t *testing.T) {
	sandboxHome(t)
	for _, p := range []provider.Provider{
		{ID: "deepseek-agents", Name: "DeepSeek-Agents", Key: "k", Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
			Models: []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}},
		{ID: "xai", Name: "xAI", Preset: "xai", Chat: "https://api.x.ai/v1", Responses: "https://api.x.ai/v1", Anthropic: "https://api.x.ai", Key: "k", Models: []string{"grok-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "x-grok-agent", Name: "X-Grok-Agent", Members: []string{"xai/grok-5"}},
		{ID: "deepseek-api-agents", Name: "DeepSeek-API-Agents", Members: []string{"deepseek-agents/deepseek-v4.1-flash"}},
		{ID: "open-chatgpt", Name: "Open-ChatGPT", Members: []string{"xai/grok-5"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SwitchGroup("open-chatgpt", false); err != nil {
		t.Fatal(err)
	}
	// Codex shown only its picks, made before the DeepSeek group was
	if err := provider.SetOnlyPicked("codex", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetPickedModels("codex", []string{"group/x-grok-agent", "xai/grok-5"}); err != nil {
		t.Fatal(err)
	}
	unserved := func(id string) map[string]unservedJSON {
		t.Helper()
		a, err := agent.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		fs := agentFields(a, a.Values())
		if len(fs) == 0 {
			t.Fatalf("%s has no fields", id)
		}
		out := map[string]unservedJSON{}
		for _, u := range fs[0].Unserved {
			out[u.Ref] = u
		}
		return out
	}
	cx := unserved("codex")
	if u := cx["group/deepseek-api-agents"]; u.Value != "group/deepseek-api-agents" || u.Why != provider.WhyGroupNotPicked || !u.Pick {
		t.Errorf("Codex's DeepSeek-API-Agents: %+v", u)
	}
	if u := cx["group/open-chatgpt"]; u.Value != "group/open-chatgpt" || u.Why != provider.WhyGroupOff || u.Pick {
		t.Errorf("Codex's Open-ChatGPT: %+v", u)
	}
	if _, ok := cx["group/x-grok-agent"]; ok {
		t.Error("Codex's picker offers X-Grok-Agent, yet says it lacks it")
	}
	cl := unserved("claude")
	if u := cl["group/open-chatgpt"]; u.Value != "group/open-chatgpt" || u.Why != provider.WhyGroupOff {
		t.Errorf("Claude Code's Open-ChatGPT: %+v", u)
	}
	if _, ok := cl["group/deepseek-api-agents"]; ok {
		t.Error("Claude Code isn't shown only its picks, yet lacks DeepSeek-API-Agents")
	}
	if u := unserved("opencode")["group/open-chatgpt"]; u.Value != "magpie/group/open-chatgpt" || u.Why != provider.WhyGroupOff {
		t.Errorf("OpenCode's Open-ChatGPT: %+v", u)
	}
}
