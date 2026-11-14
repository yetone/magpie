package gateway

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code lists a gateway's models in /model when the user sets
// CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY (its Gateway Model Discovery,
// 2.1.316), but it keeps only the ids that say claude or anthropic. Its
// check (Tvi in its bundle, and its gateway login's pt) is
//
//	te.data.data.filter((se) => /(claude|anthropic)/i.test(se.id))
//
// on GET <ANTHROPIC_BASE_URL>/v1/models?limit=1000, sent with
// "anthropic-version: 2023-06-01" and its own User-Agent
// ("claude-code/2.1.316", Xs(), not the claude-cli/ one its turns carry).
// A remote magpie's Kimi, GLM and DeepSeek were left out of /model whole
// (zent00 on X). Shown to that request, a model whose id says neither is
// listed by the alias Claude Desktop is shown it by, anthropic/magpie-<n>
// (aliasFor), which no Claude model's id says and which unprefixed
// serves as the model for every client, before any provider's model of
// that spelling. "anthropic/" put in front of magpie's id would read better,
// but a provider named anthropic (the Anthropic preset's id) serves any
// model asked of it by that spelling. Its label is display_name, the name
// the agents' lists give the model. Every other client, and Claude Code
// asking any other way, is shown the ids as they are.
var claudeCodeKept = lazyre.New(`(?i)(claude|anthropic)`)

// claudeCodeDiscovers is Claude Code's Gateway Model Discovery asking for
// the list: its User-Agent (claude-code/, or claude-cli/ as its turns
// say it) and the anthropic-version header it sends.
func claudeCodeDiscovers(r *http.Request) bool {
	if r.Header.Get("anthropic-version") == "" {
		return false
	}
	ua := agentUA(r.Context(), r.Header)
	return strings.HasPrefix(ua, "claude-code/") || claudeCodeUA(ua)
}

// claudeCodeListed is the id Claude Code's discovery is shown a row by,
// id being the row's id and e the model it is: id itself when Claude Code
// keeps it or the calling key gives it a name (the user's own, put there
// by aliasNames), else e's alias. A row by a bare id
// (settings.BareModelIDs) that Claude Code wouldn't keep is left out
// (""): it is the model its full id's row is listed by already.
func claudeCodeListed(who access.Identity, e provider.Entry, id string, bare bool) string {
	if claudeCodeKept.MatchString(id) {
		return id
	}
	if _, ok := who.AliasOf(id); ok {
		return id
	}
	if bare {
		return ""
	}
	return aliasFor(e.ID)
}
