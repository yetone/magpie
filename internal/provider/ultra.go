package provider

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Codex's Ultra is not a level an API takes: Codex sends the model its max
// (or the entry's multi_agent_reasoning_effort) and, on a thread at
// multi-agent V2, hands parts of the task to agents of its own unasked
// (codex-rs core/src/session/multi_agents.rs). Codex offers it on a model
// whose entry lists "ultra"; its own catalog (models-manager/models.json)
// lists it for these alone — GPT-6 Luna and GPT-5.6 Luna stop at max — so
// magpie offers it on them wherever they are served (Copilot's, a key's, a
// relay's: #656), never on every model that reaches max. GPT-6.1 Terra
// carries it since #4909.
var ultraModels = []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6.1-terra"}

// ultraSet is ultraModels as catalog.IsOneOf takes them.
var ultraSet = func() map[string]bool {
	m := map[string]bool{}
	for _, id := range ultraModels {
		m[id] = true
	}
	return m
}()

// datedSuffix is a snapshot's date after a model's id (-2026-09-14, -20260914).
var datedSuffix = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2}|\d{8})$`)

// OffersUltra reports whether Codex offers its Ultra on the model, by the id
// a vendor serves it under: openai/gpt-6.1-sol on a relay, a dated snapshot,
// and a relay's rename the catalog reads as one of them (codex-gpt-6.1-sol,
// "[A]gpt-6.1-sol", one the user said is gpt-6.1-sol), whose levels are
// that model's already (provider.borrowedEfforts) and stopped at max.
func OffersUltra(model string) bool {
	id := strings.ToLower(model)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = datedSuffix.ReplaceAllString(id, "")
	return ultraSet[id] || id != "" && catalog.IsOneOf(id, ultraSet)
}

// withUltra adds Codex's ultra to the levels of a model that offers it and
// reaches max; the gateway sends max for it (fitEffort).
func withUltra(model string, efforts []string) []string {
	if !slices.Contains(efforts, "max") || slices.Contains(efforts, "ultra") || !OffersUltra(model) {
		return efforts
	}
	return append(slices.Clone(efforts), "ultra")
}
