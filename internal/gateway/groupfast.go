package gateway

import "github.com/yetone/magpie/internal/provider"

// claudeFastBeta is the beta Claude's fast mode (speed "fast") is asked
// under.
const claudeFastBeta = "fast-mode-2026-02-01"

// withFast asks for the vendor's fast mode in the agent's own protocol, as
// a group's member sent fast is (provider.Group.Fast): priority processing
// in OpenAI's words, speed "fast" in Anthropic's. A request carried on in
// another protocol keeps it (Request.Fast), and goes out with it only
// where the vendor and model have one (buildResponses, buildChat, build,
// cursorModelID).
func withFast(proto provider.Protocol, body []byte) []byte {
	switch proto {
	case provider.Chat, provider.Responses:
		return withFields(body, map[string]any{"service_tier": "priority"})
	case provider.Anthropic:
		return withFields(body, map[string]any{"speed": "fast"})
	}
	return body
}
