package agent

import (
	"cmp"

	"github.com/yetone/magpie/internal/provider"
)

// decisionEndpoints are the gateway paths a decision model is asked on:
// System One, its own API, and OpenAI's Decisions API, which the gateway
// translates to System One for it (decisions_systemone.go).
var decisionEndpoints = []string{"/v1/systemone", "/v1/decisions"}

// pluggedDecisionModels are magpie's decision models as an agent plugin's
// connect is given them, apart from models: the models a routing group's
// pickers offer (provider.OfferedDeciders, what /v1/models lists under
// X-Magpie-Deciders), then the routing groups of them, each with the
// paths it is asked on. models is chat models only, which an agent offers
// for a conversation; a decision model there would be offered for one
// and fail (baranwang on X: a plugin wiring magpie's judging models into
// its agent had no way to learn them). [] for none, never nil.
func pluggedDecisionModels(agent string) []map[string]any {
	ds := provider.OfferedDeciders(nil)
	for i := range ds {
		ds[i].Name = cmp.Or(ds[i].Name, ds[i].Model)
	}
	labels := provider.LabelsFor(agent, ds)
	out := make([]map[string]any, 0, len(ds))
	for i, e := range ds {
		out = append(out, map[string]any{"id": e.ID, "name": labels[i], "provider": e.Provider.ID, "endpoints": decisionEndpoints})
	}
	for _, g := range provider.Groups() {
		if g.Hidden || g.Disabled || !g.Decides() {
			continue
		}
		out = append(out, map[string]any{"id": g.Ref(), "name": cmp.Or(g.Name, g.ID), "group": true, "endpoints": decisionEndpoints})
	}
	return out
}
