package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/codexcat"
)

// Two providers both called Codex, each serving GPT-6.1-Sol (nb5p on
// Discord), are named apart in Codex's picker by their provider's id, and
// two of one provider by their own id. Only display_name says it: the
// description and Name, which go upstream in spawn_agent's list of models,
// stay as they were, and an entry no other shares a name with is shown as
// before.
func TestCodexPickerNamesSameNamesApart(t *testing.T) {
	a := Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex", User: "a@example.com"}}
	b := Provider{ID: "codex-b", Name: "Codex", Account: &Account{Agent: "codex", User: "b@example.com"}}
	r := Provider{ID: "relay", Name: "Relay"}
	ms := codexListed([]Entry{
		{ID: "codex/gpt-6.1-sol", Model: "gpt-6.1-sol", Name: "GPT-6.1-Sol", Provider: a},
		{ID: "codex-b/gpt-6.1-sol", Model: "gpt-6.1-sol", Name: "GPT-6.1-Sol", Provider: b},
		{ID: "relay/glm-5.3", Model: "glm-5.3", Name: "GLM-5.3", Provider: r},
		{ID: "relay/glm-5.3-old", Model: "glm-5.3-old", Name: "GLM-5.3", Provider: r},
		{ID: "relay/kimi-k3", Model: "kimi-k3", Name: "Kimi K3", Provider: r},
	}, func(string) []Member { return nil }, true)
	type shown struct{ name, desc string }
	want := []shown{
		{"GPT-6.1-Sol · Codex · codex", "GPT-6.1-Sol · Codex via magpie"},
		{"GPT-6.1-Sol · Codex · codex-b", "GPT-6.1-Sol · Codex via magpie"},
		{"GLM-5.3 · Relay · relay/glm-5.3", "GLM-5.3 · Relay via magpie"},
		{"GLM-5.3 · Relay · relay/glm-5.3-old", "GLM-5.3 · Relay via magpie"},
		{"Kimi K3 · Relay", "Kimi K3 · Relay via magpie"},
	}
	es := codexcat.Entries(ms, 0)
	if len(es) != len(want) {
		t.Fatalf("%d entries, want %d", len(es), len(want))
	}
	for i, e := range es {
		b, _ := json.Marshal(e)
		var got struct {
			Name string `json:"display_name"`
			Desc string `json:"description"`
		}
		json.Unmarshal(b, &got)
		// the text-only note is beside the point here
		got.Desc = strings.Replace(got.Desc, " (text only: Accepts images is in its provider's Model settings in magpie)", "", 1)
		if (shown{got.Name, got.Desc}) != want[i] {
			t.Errorf("%s: shown as %q, %q; want %q, %q", ms[i].ID, got.Name, got.Desc, want[i].name, want[i].desc)
		}
		// what goes upstream in spawn_agent's list (spawnChoices) is Name
		if ms[i].Name != strings.TrimSuffix(want[i].desc, " via magpie") {
			t.Errorf("%s: Name %q changed", ms[i].ID, ms[i].Name)
		}
	}
}
