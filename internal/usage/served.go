package usage

import (
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Which model answered: a vendor may serve a request with another model
// than the one asked for — a cheaper one when it's busy — and its reply
// says so in its model field. Most echo the name asked for, or its dated
// or pinned version (gpt-5 as gpt-5-2025-08-07, claude-sonnet-4-5 as
// claude-sonnet-4-5-20250929, gemini-2.5-pro as models/gemini-2.5-pro-001),
// which is the same model.

// versionTail is what a vendor puts after a model's name for the version
// it answered with: a date, a build number, Bedrock's v1:0, latest,
// preview. Not v3 alone: deepseek-v3 is another model than deepseek-v2.
var versionTail = regexp.MustCompile(`(?:[-_@:](?:\d{4}-\d{2}-\d{2}|\d{2}-\d{2}|\d{6,8}|\d{3,4}|v\d+:\d+|latest|preview|exp))+$`)

// vendorDot is Bedrock's region and maker before a model's name
// (us.anthropic.claude-…).
var vendorDot = regexp.MustCompile(`^(?:[a-z]{2,4}\.)?(?:anthropic|amazon|meta|mistral|cohere|ai21|deepseek|qwen|openai|google|moonshotai|minimax|zai)\.`)

// contextTail is what Claude Code writes after a model's name for the size of
// its context: claude-opus-5[1m].
var contextTail = regexp.MustCompile(`\[[^\]]*\]$`)

// bareModel is a model's name without its maker or path, its version, the
// size of its context or its case.
func bareModel(m string) string {
	m = contextTail.ReplaceAllString(strings.ToLower(strings.TrimSpace(m)), "")
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	m = vendorDot.ReplaceAllString(m, "")
	if loc := versionTail.FindStringIndex(m); loc != nil && loc[0] > 0 {
		m = m[:loc[0]]
	}
	return m
}

// Swapped reports whether served is another model than sent: not the same
// name, however dated, pinned or prefixed. A call that went out as one level
// of a model (an Antigravity account's gemini-3.8-flash-medium) answered
// under the model's own name is that model at that level: Antigravity's
// reply names the family, not the variant (#462). Another level is another
// model, as AntigravitySentID has it. A vendor's "auto" (Copilot's,
// Cursor's) asked it to pick, so whichever answers wasn't swapped in, nor
// is the member another magpie's routing group sent it to (GroupRouted),
// nor Google's own name for the deployment serving a Gemini model
// (GeminiServing).
func Swapped(sent, served string) bool {
	a, b := bareModel(sent), bareModel(served)
	return a != "" && b != "" && squash(a) != squash(b) && a != "auto" && squash(bareModel(provider.EffortFamily(a))) != squash(b) && !GroupRouted(sent, served) && !GeminiServing(sent, served)
}

// GeminiServing reports whether served is the name Google's backend gives
// the deployment that serves the Gemini model sent: the model's own name
// with "-n" after it. Antigravity answers every level of gemini-3.8-flash
// (-low, -medium, -high) with modelVersion gemini-3.8-flash-n, an id no
// request can ask for (404) and fetchAvailableModels never lists, so it is
// that model, not another one swapped in (dumplings on Discord). It holds
// for Gemini models only: gpt-5 answered as gpt-5-n is still a swap, as is
// gemini-3.8-flash answered as gemini-3.8-flash-lite or gemini-3.7-flash-n.
func GeminiServing(sent, served string) bool {
	a := squash(bareModel(provider.EffortFamily(bareModel(sent))))
	b, ok := strings.CutSuffix(bareModel(served), "-n")
	return ok && strings.HasPrefix(a, "gemini") && squash(b) == a
}

// squash is a bare name without its separators, so a vendor that writes
// the dots of a version as hyphens (Volcengine Ark: deepseek-v4.1-flash
// answered as deepseek-v4-1-flash, vincentzhang on Discord) or underscores
// names the same model.
var squash = strings.NewReplacer(".", "", "-", "", "_", "", " ", "").Replace

// SameSpelled reports whether served is sent spelled with other separators
// or case: a row or route kept marked swapped before Swapped knew it is
// read as not swapped.
func SameSpelled(sent, served string) bool {
	a := squash(bareModel(sent))
	return a != "" && a == squash(bareModel(served))
}

// GroupRouted reports whether sent is a routing group of another magpie
// (a remote magpie's "group/<id>", the one name only a magpie answers
// to) and served the model its reply named: the member the group routed
// the request to, which is the group doing its job, not the vendor
// serving another model than asked (莫 on Discord: group/auto-… answered
// by deepseek/deepseek-v4.1-flash, marked a swap).
func GroupRouted(sent, served string) bool {
	return strings.TrimSpace(served) != "" && strings.HasPrefix(strings.TrimSpace(sent), provider.GroupPrefix)
}
