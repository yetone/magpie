package provider

import (
	"net/http"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// anthropicNoCredit is Anthropic's refusal of a request whose API key's
// Console organization has no credit it can be paid with: 400
// invalid_request_error "Your credit balance is too low to access the
// Anthropic API. Please go to Plans & Billing to upgrade or purchase
// credits." (avenger on X: a key tested green, and Claude Code through
// magpie said "Credit balance too low · Add funds"; they had thought their
// Claude Max plan could be used so).
var anthropicNoCredit = lazyre.New(`(?i)credit balance is too low to access the (anthropic|claude) api`)

// AnthropicNoCreditHint says what that refusal means for an API key at
// Anthropic's own API, and where a Claude plan goes in magpie instead. It
// says only what Anthropic's reply and its billing do, and nothing of
// what a plan includes.
const AnthropicNoCreditHint = "Anthropic pays an API key's requests from the API credit of the key's Console organization, which it says is too low for this request; credit is bought under Plans & Billing in the Console. A Claude Pro or Max plan is used in magpie as the Claude subscription (Add a provider › Claude, signed in in magpie), not as an API key"

// anthropicOwnHosts are the hosts of Anthropic's own API, where a key's
// requests are paid from its organization's credit: a var, for a test's
// server.
var anthropicOwnHosts = map[string]bool{"api.anthropic.com": true}

// NoCreditHint is AnthropicNoCreditHint for Anthropic's refusal of p's
// key (anthropicNoCredit), when p is a key at Anthropic's own API; "" for
// any other reply or provider. A relay passing the same words on speaks of
// its own upstream's credit, and a Claude subscription's account isn't
// paid from a Console organization's: neither is told it.
func (p Provider) NoCreditHint(status int, body []byte) string {
	if status != http.StatusBadRequest || p.Account != nil || !anthropicOwnHosts[HostOf(p.Base(Anthropic))] {
		return ""
	}
	if !anthropicNoCredit.Match(catalog.Words(body)) {
		return ""
	}
	return AnthropicNoCreditHint
}
