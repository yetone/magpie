package codexcat

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// brethhbbm on X: with "Codex subagents on any model" on, a lead on
// relay/gpt-6-astra (a relay of the ChatGPT backend, listing Codex's own
// entry for the model, multi_agent_version "v2" and all) still had its
// subagents' tasks sealed, so the subagent model set was never used. A
// model a provider that seals serves (Seals) now says V1 with the setting,
// listed by its relay or made by magpie; with the setting off it keeps what
// its vendor said, and a model of a provider that seals nothing (Copilot's
// Ultra, #656) keeps V2.
func TestCodexAgentsV1SealingRelay(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[]}`)
	listed := json.RawMessage(`{"slug":"gpt-6-astra","display_name":"GPT-6 Astra","description":"x","base_instructions":"y",` +
		`"multi_agent_version":"v2","supported_reasoning_levels":[{"effort":"low","description":"l"},{"effort":"ultra","description":"u"}],` +
		`"shell_type":"shell_command","visibility":"list"}`)
	ultra := []string{"low", "ultra"}
	ms := []catalog.Model{
		{ID: "relay/gpt-6-astra", Name: "gpt-6-astra", Efforts: ultra, Codex: listed, Seals: true},
		{ID: "cpa/gpt-6.1-terra", Name: "gpt-6.1-terra", Efforts: ultra, AgentsV2: true, Seals: true},
		{ID: "copilot/gpt-6.1-sol", Name: "GPT-6.1 Sol", Efforts: ultra, AgentsV2: true},
	}
	setV1(t, false)
	off := versions(t, ms)
	if off["relay/gpt-6-astra"] != "v2" || off["cpa/gpt-6.1-terra"] != "v2" || off["copilot/gpt-6.1-sol"] != "v2" {
		t.Fatalf("off: %v", off)
	}
	setV1(t, true)
	on := versions(t, ms)
	if on["relay/gpt-6-astra"] != "v1" || on["cpa/gpt-6.1-terra"] != "v1" || on["copilot/gpt-6.1-sol"] != "v2" {
		t.Fatalf("on: %v", on)
	}
}
