package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group whose members name their models in another case than
// the provider lists them by (DeepSeek-V4-Pro for deepseek-v4-pro) is in
// every agent's model picker: it was in none, without a word, while the
// Routing page listed it on (@CHENGJIAHEGOXIS on X, Codex's picker).
func TestGroupOfMembersInAnotherCaseInEveryPicker(t *testing.T) {
	home, _ := codexHome(t, "", "")
	for _, p := range []provider.Provider{
		{ID: "deepseek-agents", Name: "DeepSeek-Agents", Key: "k", Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
			Models: []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}},
		{ID: "xai", Name: "xAI", Preset: "xai", Chat: "https://api.x.ai/v1", Responses: "https://api.x.ai/v1", Key: "k", Models: []string{"grok-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "x-grok-agent", Name: "X-Grok-Agent", Members: []string{"xai/grok-5"}},
		{ID: "deepseek-api-agents", Name: "DeepSeek-API-Agents", Members: []string{"deepseek-agents/DeepSeek-V4.1-Flash", "deepseek-agents/DeepSeek-V4-Pro"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []*Agent{codex(home), claude(home), opencode(home, filepath.Join(home, ".config"))} {
		var groups []string
		for _, o := range a.Picks(a.Fields[0], nil) {
			if o.Group == RoutingGroups {
				groups = append(groups, o.Label)
			}
		}
		if got := strings.Join(groups, ", "); !strings.Contains(got, "DeepSeek-API-Agents") || !strings.Contains(got, "X-Grok-Agent") {
			t.Errorf("%s's %s picker offers groups %q, want both", a.Name, a.Fields[0].Label, got)
		}
	}
}
