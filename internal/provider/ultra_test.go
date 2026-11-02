package provider

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// #656: Codex's Ultra on Copilot's gpt-6.1-sol. Codex offers it on a model
// whose entry lists "ultra", and OpenAI's catalog lists it for GPT-6.1 Sol
// (and the other Sols, Astra, Terra), not GPT-6 Luna: magpie's entry for a
// model OpenAI offers it on, served by another vendor up to max, lists it
// too and says multi-agent V2, so Codex hands work to its agents; Luna's
// stops at max. A ChatGPT account's entry is the backend's, and a group with
// one in it isn't told V2 (its lead's subagent tasks come sealed, #141).
func TestUltraOnModelsThatOfferIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	for id, want := range map[string]bool{
		"gpt-6.1-sol": true, "openai/gpt-6.1-sol": true, "GPT-6.1-Sol": true, "gpt-6.1-sol-2026-09-14": true,
		"gpt-6-astra": true, "gpt-6-sol": true, "gpt-5.6-sol": true, "gpt-5.6-terra": true,
		"gpt-6.1-terra": true, "relay/gpt-6.1-terra": true,
		"gpt-6-luna": false, "gpt-5.6-luna": false, "gpt-6.1-sol-mini": false, "claude-opus-5-5": false, "": false,
	} {
		if OffersUltra(id) != want {
			t.Errorf("OffersUltra(%q) = %v", id, !want)
		}
	}

	upToMax := []string{"none", "low", "medium", "high", "xhigh", "max"}
	s := settings.Load()
	copilot := Provider{ID: "copilot", Account: &Account{Agent: "copilot"}}
	sol := entryFor(copilot, catalog.Model{ID: "gpt-6.1-sol", Efforts: upToMax}, s)
	if !slices.Equal(sol.Efforts, append(slices.Clone(upToMax), "ultra")) || !sol.AgentsV2 {
		t.Fatalf("copilot/gpt-6.1-sol: %v v2=%v", sol.Efforts, sol.AgentsV2)
	}
	// gpt-6.1-terra carries Ultra in Codex's catalog now (#4909), like the
	// Sols and Astra: another vendor's entry up to max lists it too
	terra := entryFor(copilot, catalog.Model{ID: "gpt-6.1-terra", Efforts: upToMax}, s)
	if !slices.Equal(terra.Efforts, append(slices.Clone(upToMax), "ultra")) || !terra.AgentsV2 {
		t.Fatalf("copilot/gpt-6.1-terra: %v v2=%v", terra.Efforts, terra.AgentsV2)
	}
	luna := entryFor(copilot, catalog.Model{ID: "gpt-6-luna", Efforts: upToMax}, s)
	if !slices.Equal(luna.Efforts, upToMax) || luna.AgentsV2 {
		t.Fatalf("copilot/gpt-6-luna: %v v2=%v", luna.Efforts, luna.AgentsV2)
	}
	// short of max: no Ultra
	if e := entryFor(copilot, catalog.Model{ID: "gpt-6.1-sol", Efforts: []string{"low", "medium", "high"}}, s); slices.Contains(e.Efforts, "ultra") || e.AgentsV2 {
		t.Fatalf("gpt-6.1-sol up to high: %v v2=%v", e.Efforts, e.AgentsV2)
	}
	// a ChatGPT account's: as the backend lists it
	codex := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	own := entryFor(codex, catalog.Model{ID: "gpt-6.1-sol", Efforts: append(slices.Clone(upToMax[1:]), "ultra")}, s)
	if own.AgentsV2 || slices.Index(own.Efforts, "ultra") != len(own.Efforts)-1 || len(own.Efforts) != 6 {
		t.Fatalf("codex/gpt-6.1-sol: %v v2=%v", own.Efforts, own.AgentsV2)
	}

	relay := entryFor(Provider{ID: "relay"}, catalog.Model{ID: "gpt-6.1-sol", Efforts: upToMax}, s)
	group := func(entries ...Entry) Entry {
		t.Helper()
		gs := groupEntries(entries)
		if len(gs) != 1 {
			t.Fatalf("groups %+v", gs)
		}
		return gs[0]
	}
	if g := group(sol, relay); !slices.Contains(g.Efforts, "ultra") || !g.AgentsV2 {
		t.Fatalf("group of copilot and a relay: %v v2=%v", g.Efforts, g.AgentsV2)
	}
	if g := group(sol, own); !slices.Contains(g.Efforts, "ultra") || g.AgentsV2 {
		t.Fatalf("group with a ChatGPT account's: %v v2=%v", g.Efforts, g.AgentsV2)
	}
}
