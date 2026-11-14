package provider

import (
	"bytes"

	"github.com/yetone/magpie/internal/lazyre"
)

// OpenRouter serves some free endpoints only to the agent apps it lists
// (openrouter.ai/apps), as it attributes a request to an app (its
// HTTP-Referer). Any other request is answered 403 "thinkingmachines/
// inkling-2-small:free is only available on agentic harnesses. Try
// plugging it into a coding agent or productivity app listed on
// https://openrouter.ai/apps" (Wade111999 on X), its routing step "Gate
// Free Endpoints by Agentic Harness". The key is fine, and so is the
// model: the app the request came from is what is turned away, so a
// magpie test, which is magpie's own request and names no app, is
// refused, and an agent's may be served. magpie never names an app it
// isn't.

// harnessOnly matches that refusal's words.
var harnessOnly = lazyre.New(`(?i)only available (?:on|with|via|in|to|for use with) agentic harness`)

// HarnessOnlyHint says what the refusal means and what to do.
const HarnessOnlyHint = "OpenRouter serves this free model only to requests from an agent app it lists at openrouter.ai/apps, which magpie's own requests (a model test) are not; the model without :free is served to any request"

// HarnessOnly says whether an error body is OpenRouter's refusal of a
// request not from an agent app it lists.
func HarnessOnly(b []byte) bool {
	return harnessOnly.Match(b) || bytes.Contains(b, []byte(HarnessOnlyHint))
}
