package provider

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// Codex's Ultra is not a level an API takes: Codex sends the model its max
// (or the entry's multi_agent_reasoning_effort) and, on a thread at
// multi-agent V2, hands parts of the task to agents of its own unasked
// (codex-rs core/src/session/multi_agents.rs). Codex offers it on a model
// whose entry lists "ultra", and its own catalog (models-manager/
// models.json) lists it on every GPT Sol, Astra and Terra, from GPT-5.6 Sol
// to GPT-6.2 Sol and Astra, and on no Luna. So magpie offers it on a model
// of those families wherever it is served (Copilot's, a key's, a relay's:
// #656), never on every model that reaches max. It was a list of ids, and
// each new model went without until it was added by hand: GPT-6.1 Terra
// (#4909), then GPT-6.2 Sol and Astra (forkhouhou on X).
var ultraFamilies = map[string]bool{"gpt-sol": true, "gpt-astra": true, "gpt-terra": true}

// ultraFamily reports whether a bare id (lowercase, no vendor's prefix)
// names a version of a family Codex offers Ultra on: gpt-6.2-sol and its
// dated snapshots, not gpt-6.1-sol-mini, gpt-6-sol-fast or gpt-6-luna.
func ultraFamily(bare string) bool {
	f := catalog.FamilyOf(bare)
	return len(f.Version) > 0 && ultraFamilies[f.Name]
}

// datedSuffix is a snapshot's date after a model's id (-2026-09-14, -20260914).
var datedSuffix = lazyre.New(`-(\d{4}-\d{2}-\d{2}|\d{8})$`)

// OffersUltra reports whether Codex offers its Ultra on the model, by the id
// a vendor serves it under: openai/gpt-6.1-sol on a relay, a dated snapshot,
// and a relay's rename the catalog reads as one of them (codex-gpt-6.1-sol,
// "[A]gpt-6.1-sol", one the user said is gpt-6.1-sol), whose levels are
// that model's already (provider.borrowedEfforts) and stopped at max.
func OffersUltra(model string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if id == "" {
		return false
	}
	return catalog.IsOneLike(id, ultraFamily)
}

// withUltra adds Codex's ultra to the levels of a model that offers it and
// reaches max; the gateway sends max for it (fitEffort).
func withUltra(model string, efforts []string) []string {
	if !slices.Contains(efforts, "max") || slices.Contains(efforts, "ultra") || !OffersUltra(model) {
		return efforts
	}
	return append(slices.Clone(efforts), "ultra")
}
