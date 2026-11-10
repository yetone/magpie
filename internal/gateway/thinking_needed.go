package gateway

import (
	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// thinkingNeededRefusal is an upstream turning away a Messages request
// whose thinking isn't "enabled": thinking left out, or turned off as
// magpie does for a vendor's model asked without it
// (thinkingOffUnlessAsked). A relay in front of such a model answered
// Claude Desktop's connection check ({"max_tokens":1,"messages":[…"."]},
// no thinking) with `thinking.type must be "enabled"` (cybercaius on
// Discord, v0.1.1862, his own 天翼云 provider). Read off the reply, not
// the provider: any upstream that says it is asked again thinking.
var thinkingNeededRefusal = lazyre.New(`(?i)thinking[._ ]type\W+(?:must|should|has to) be\W+enabled\b`)

// thinkingNeeded is how unfit remembers an upstream that takes model only
// with thinking enabled (thinkingNeededRefusal).
func thinkingNeeded(model string) string { return "thinking needed\x00" + model }

// neededBudget is the thinking budget a request that asked for none is
// sent at, for a model that needs thinking: Anthropic's least, which the
// reply limit is raised past (withThinkingEnabled).
const neededBudget = 1024

// withThinkingEnabled is a Messages body that asked for no thinking, or
// turned it off, sent thinking at the least budget instead, for a model
// that takes no other: max_tokens is raised past the budget by what was
// asked (a budget must be below it), and the sampling a thinking request
// is refused with (temperature, top_p, top_k) left out. false for a body
// that asks to think already, or isn't JSON.
func withThinkingEnabled(body []byte) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return nil, false
	}
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled", "adaptive":
		return nil, false
	}
	maxTokens := gjson.GetBytes(body, "max_tokens").Int()
	if maxTokens <= neededBudget {
		maxTokens = neededBudget + max(maxTokens, 1)
	}
	out := withoutFields(body, "temperature", "top_p", "top_k")
	out = withFields(out, map[string]any{
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": neededBudget},
		"max_tokens": maxTokens,
	})
	return out, true
}

// markThinkingNeeded remembers that p takes model only thinking, by the
// name magpie knows it and the vendor's (wire), which forwardOnce reads
// off the body: thinking enabled isn't made adaptive for it there, as
// "enabled" is the one type it said it takes.
func (s *Server) markThinkingNeeded(p provider.Provider, to provider.Protocol, model, wire string) {
	s.markUnfit(p.ID, thinkingNeeded(model), to)
	if wire != "" && wire != model {
		s.markUnfit(p.ID, thinkingNeeded(wire), to)
	}
}
