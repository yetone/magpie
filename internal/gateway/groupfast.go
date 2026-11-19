package gateway

import (
	"context"

	"github.com/yetone/magpie/internal/provider"
)

// claudeFastBeta is the beta Claude's fast mode (speed "fast") is asked
// under.
const claudeFastBeta = "fast-mode-2026-02-01"

// withFast asks for the vendor's fast mode in the agent's own protocol, as
// a group's member sent fast is (provider.Group.Fast): priority processing
// in OpenAI's words, speed "fast" in Anthropic's. A request carried on in
// another protocol keeps it (Request.Fast), and goes out with it only
// where the vendor and model have one (buildResponses, buildChat, build,
// cursorModelID). A client's own ask for Ultrafast, faster than priority,
// is kept: sent fast, it isn't sent slower than it asked (#1784,
// zhenghaowen0967: OMP with fastPicks asked Ultrafast and went as
// priority). An account that doesn't offer it is asked for priority in
// its place, as any is (tierMate).
func withFast(proto provider.Protocol, body []byte) []byte {
	switch proto {
	case provider.Chat, provider.Responses:
		if askedTier(body) == "ultrafast" {
			return body
		}
		return withFields(body, map[string]any{"service_tier": "priority"})
	case provider.Anthropic:
		return withFields(body, map[string]any{"speed": "fast"})
	}
	return body
}

// fastPick sends the model an agent picked to be sent fast (#954) so:
// every account and key of it, not a fallback to another. Ultra sends it
// as Ultrafast where the account offers that (#1954).
func fastPick(cands []candidate, pl planned, providerID, model string, ultra bool) {
	for i, c := range cands {
		if c.p.ID == providerID && c.model == model {
			cands[i].fast, cands[i].ultra = true, ultra
		}
	}
	for _, ws := range [][]Weighed{pl.order, pl.left} {
		for i, w := range ws {
			if w.Provider == providerID && w.Model == model {
				ws[i].Fast, ws[i].Ultra = true, ultra
			}
		}
	}
}

// ownFastKey marks a try of a member or pick the user sent fast on a
// provider they added by its address (provider.OwnFast, #1615): it is asked for service_tier
// "priority" on its OpenAI API (forwardTranslated) whatever API the agent
// spoke. An agent on Chat or Responses says it in its own body as well
// (withFast), which a relayed request carries as it is.
type ownFastKey struct{}

func withOwnFast(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownFastKey{}, true)
}

func isOwnFast(ctx context.Context) bool {
	v, _ := ctx.Value(ownFastKey{}).(bool)
	return v
}

// extraUltra reports whether p's extra body asks for Ultrafast itself
// (provider.ExtraTier), which a member or pick sent fast on it is sent at.
func extraUltra(p provider.Provider) bool {
	tier, _ := p.ExtraTier()
	return tier == "ultrafast"
}

// chatNoUltra reports whether a Chat Completions request goes to p on
// OpenAI's own API, which answers service_tier "ultrafast" there with 400
// "Invalid service_tier argument" (11-18), on every model; its Responses
// take it (provider.OpenAIUltra).
func chatNoUltra(p provider.Provider) bool {
	return p.Chat != "" && provider.HostOf(p.Chat) == "api.openai.com"
}

// ultraKey marks a try of a pick sent Ultrafast (#1954) from an agent on
// Claude's API: its speed "fast" goes to the ChatGPT backend as Ultrafast
// (forwardTranslated), on an account that offers it.
type ultraKey struct{}

func withUltra(ctx context.Context) context.Context {
	return context.WithValue(ctx, ultraKey{}, true)
}

func isUltra(ctx context.Context) bool {
	v, _ := ctx.Value(ultraKey{}).(bool)
	return v
}
