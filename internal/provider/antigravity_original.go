package provider

import (
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// antigravityOriginal is what Antigravity counts Gemini 4 Carbon and Gemini
// 4 Argon at: their original price, not the introductory one the Gemini API
// charges until 2026-11-30. Google's own page
// (https://ai.google.dev/gemini-api/docs/pricing, read 2026-11-19) lists both
// at "$2.00 through November 30, 2026. $4.00 starting December 1, 2026" in,
// $10 then $20 out and $0.10 then $0.20 cached, and models.dev lists the
// introductory $2 / $10 / $0.10. ZhouYimingsjtu on X ran an Ultra 20x plan
// through about $200 of calls and measured Carbon : Neon : Hydrogen at
// 1.64 : 1 : 0.39 ("Carbon在antigravity的订阅其实还是按照原价，并没有用打折价"),
// which the introductory price (0.67 : 1 : 0.5 of a call's cost) can't
// give. Gemini 4 Neon ($3 / $15) and Hydrogen ($1.50 / $7.50) have no
// introductory price. Only Antigravity's route: the Gemini API key, Vertex
// and OpenRouter charge what their own pages list.
var antigravityOriginal = map[string]catalog.Price{
	"gemini-4-carbon": {Input: 4, Output: 20, CacheRead: 0.2},
	"gemini-4-argon":  {Input: 4, Output: 20, CacheRead: 0.2},
}

// atLeastOriginal is pr with no field under Antigravity's original price of
// the Gemini 4 model model names (at an effort, under a route's prefix, as a
// role id), else pr as it is. A catalog that has moved to the original price
// (Google's page says so from 2026-12-01) is left as it lists it.
func atLeastOriginal(model string, pr catalog.Price) catalog.Price {
	model = model[strings.LastIndexByte(model, '/')+1:]
	for _, n := range pricedNames(model) {
		o, ok := antigravityOriginal[strings.ToLower(n)]
		if !ok {
			continue
		}
		pr.Input = max(pr.Input, o.Input)
		pr.Output = max(pr.Output, o.Output)
		pr.CacheRead = max(pr.CacheRead, o.CacheRead)
		return pr
	}
	return pr
}

// agyOnlyID is whether model is a name only Antigravity gives: one of its
// Orchestrator role ids (antigravityRoles), or any id under its own route
// (agy-cli/, antigravity/).
func agyOnlyID(model string) bool {
	l := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(l, "agy-cli/") || strings.HasPrefix(l, "antigravity/") {
		return true
	}
	_, ok := antigravityRole(l)
	return ok
}

// agyScoped is pr, a price found for model without saying who charges it,
// at Antigravity's original price when the name is Antigravity's alone
// (agyOnlyID).
func agyScoped(model string, pr catalog.Price) catalog.Price {
	if !agyOnlyID(model) {
		return pr
	}
	return atLeastOriginal(model, pr)
}

// AgyScoped is agyScoped for the callers that price by a model's name alone.
func AgyScoped(model string, pr catalog.Price) catalog.Price { return agyScoped(model, pr) }

// AgyPriced is pr, a price found for model, at what Antigravity counts it
// at: for a call known to be Antigravity's (the agy CLI's or the app's own
// session log).
func AgyPriced(model string, pr catalog.Price) catalog.Price { return atLeastOriginal(model, pr) }

// servedByAntigravity is whether p is an Antigravity route: the built-in's
// accounts, or the agy-cli plugin's.
func (p Provider) servedByAntigravity() bool {
	return p.ID == "antigravity" || p.ID == agyCLIID ||
		p.Account != nil && p.Account.Agent == "antigravity" ||
		p.PluginProvider() == "antigravity" || p.PluginProvider() == agyCLIID
}
