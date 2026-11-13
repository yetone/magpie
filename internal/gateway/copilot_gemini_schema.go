package gateway

import (
	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// copilotGemini is whether body goes to a Gemini model served by a Copilot
// account.
func copilotGemini(p provider.Provider, body []byte) bool {
	return p.Account != nil && p.Account.Agent == "copilot" && geminiNamed(gjson.GetBytes(body, "model").String())
}

// copilotGrokFlat is a Responses body to Copilot's Grok with Codex's
// namespaced functions flat and their parameters objects at the root: the
// backend behind Copilot turns a request with either away, and Copilot's
// word for it is a bare status (#4374), so nothing teaches magpie to do it
// after a refusal. The calls come back under their namespaces
// (namespacedIn). Another vendor's tools go as they came.
func copilotGrokFlat(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Responses || p.Account == nil || p.Account.Agent != "copilot" ||
		!grokModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	return provider.GrokFlatBody(body)
}
