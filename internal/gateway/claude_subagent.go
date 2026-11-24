package gateway

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// ClaudeSubagent is the model set for Claude Code's subagents, as its
// settings write it ("<model>[:<effort>]"), "" when none is set or the
// agent isn't routed through magpie. Set by main (agent.ClaudeSubagent).
var ClaudeSubagent func(agent string) string

// claudeInheritPrompts open the system prompts of Claude Code's built-in
// subagents whose definitions say model "inherit": Explore and Plan.
// Claude Code reads a definition's model before CLAUDE_CODE_SUBAGENT_MODEL,
// so these two ran on the lead's model while general-purpose, which names
// none, followed the variable (#7176, Claude Code 2.1.327; on a provider
// other than Anthropic's own, Explore is pinned to "inherit" outright).
// A custom agent has a prompt of its own, and its model: line, whatever
// it names, stays Claude Code's to apply, as do the variable's other
// readers. Claude Code's CLAUDE_CODE_SUBAGENT_MODEL_FORCE would move
// those too, so it is not written.
var claudeInheritPrompts = []string{
	"You are a file search specialist for Claude Code",
	"You are a software architect and planning specialist for Claude Code",
}

// claudeSubagentPick is what is done with the model of a Claude Code
// request: nil unless it is a tool turn of an Explore or Plan subagent
// (x-claude-code-agent-id, claudeInheritPrompts) and a model is set for
// Claude Code's subagents (ClaudeSubagent) that isn't the one it is on
// already. Then To is that model and effort its effort, if it names one.
// As with Desktop's pick (desktopSubagentPick), named is the catalog id
// the request asked for, resolved where it would go now; a model set
// that is disabled, not served or not the gateway key's to use leaves
// the request as asked, and the pick says why. The cheap checks come
// first: a lead's turn costs a header read, a subagent's a scan for the
// two prompts before the settings are read.
func claudeSubagentPick(r *http.Request, agent, named, resolved string, body []byte) (pick *SubagentPick, effort string) {
	if agent != "claude" || ClaudeSubagent == nil {
		return nil, ""
	}
	if sub, _ := subagentOf(r.Header); sub == "" || !claudeInherits(body) {
		return nil, ""
	}
	set := ClaudeSubagent(agent)
	if set == "" {
		return nil, ""
	}
	to, effort := askedAt(set)
	if to == named || to == resolved {
		return nil, ""
	}
	pick = &SubagentPick{Asked: named, To: to}
	if _, off := idOff(to); off {
		pick.Kept = "it is disabled in magpie"
		return pick, ""
	}
	if why := provider.Unserved(strings.TrimSuffix(to, "[1m]")); why != "" {
		pick.Kept = why
		return pick, ""
	}
	if who, held := keyHolds(r); held || withGatewayKey(r) {
		g, ms, isGroup := provider.FindGroup(to)
		p, model, ok := provider.Resolve(to)
		if isGroup && !groupGiven(r, who, held, g, ms) || held && !isGroup && (!ok || !modelAllowed(who, p, model)) {
			pick.Kept = "the gateway key may not use it"
			return pick, ""
		}
	}
	return pick, effort
}

// claudeInherits says whether body is a tool turn whose system prompt is
// Explore's or Plan's (claudeInheritPrompts).
func claudeInherits(body []byte) bool {
	found := false
	for _, p := range claudeInheritPrompts {
		if bodyHas(body, p) {
			found = true
			break
		}
	}
	if !found || !hasTools(body) {
		return false
	}
	sys := gjson.GetBytes(body, "system")
	texts := []string{sys.String()}
	if sys.IsArray() {
		texts = texts[:0]
		for _, b := range sys.Array() {
			texts = append(texts, b.Get("text").String())
		}
	}
	for _, t := range texts {
		for _, p := range claudeInheritPrompts {
			if strings.Contains(t, p) {
				return true
			}
		}
	}
	return false
}
