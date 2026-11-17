package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// desktopSubagentPick is what is done with the model of a Claude Desktop
// request: nil when it isn't a subagent's tool turn or no model is set
// for Desktop's subagents (provider.DesktopSubagentModel), so it goes on
// as asked. Desktop's Claude Code marks a subagent's requests with
// x-claude-code-agent-id (subagentOf), and its lead's never. A subagent
// also sends tool-less helper calls, which stay where they are, and
// WebSearch's forced server tool. Search is a helper only when Desktop's
// haiku pick takes it; with no served pick, its old routing stays. named
// is the catalog id the request asked for, which the trace and ledger keep;
// resolved is where it would go now, after desktopTurn, the tiers and the
// groups, so a turn already on the model set isn't marked as moved. The
// model set must still be enabled, served by its exact id and allowed to
// the gateway key; when it isn't, the request goes on as asked and the
// pick says why. The cheap checks come first, so an unset model costs a
// held settings read.
func desktopSubagentPick(r *http.Request, agent, named, resolved string, body []byte) *SubagentPick {
	to := provider.DesktopSubagentModel()
	if to == "" || agent != "claude-desktop" || to == named || to == resolved {
		return nil
	}
	if sub, _ := subagentOf(r.Header); sub == "" {
		return nil
	}
	if !hasTools(body) || desktopSearchHelper(agent, body) {
		return nil
	}
	pick := &SubagentPick{Asked: named, To: to}
	if _, off := idOff(to); off {
		pick.Kept = "it is disabled in magpie"
		return pick
	}
	if why := provider.DesktopSubagentUnserved(to); why != "" {
		pick.Kept = why
		return pick
	}
	// a key held to some models (#882) reads as Desktop only with
	// Desktop's own client; its Code tab with a named key is Claude Code
	if who, held := keyHolds(r); held || withGatewayKey(r) {
		g, ms, isGroup := provider.FindGroup(to)
		p, model, ok := provider.Resolve(to)
		if isGroup && !groupGiven(r, who, held, g, ms) || held && !isGroup && (!ok || !modelAllowed(who, p, model)) {
			pick.Kept = "the gateway key may not use it"
		}
	}
	return pick
}

// desktopSubagentEffort is independent of a model replacement: effort-only
// picks, an unchanged model and a kept model all take it. A non-nil Desktop
// model pick already checked the agent id and tools, so the body isn't decoded
// twice. With no effort set, even that check is left out.
func desktopSubagentEffort(r *http.Request, agent string, body []byte, pick *SubagentPick) string {
	level := provider.DesktopSubagentEffort()
	if level == "" || agent != "claude-desktop" {
		return ""
	}
	if pick == nil {
		if sub, _ := subagentOf(r.Header); sub == "" || !hasTools(body) || desktopSearchHelper(agent, body) {
			return ""
		}
	}
	return level
}

// subagentTurnOf is the Claude Code subagent whose turn r is, for the
// Routing page's tag (Route.SubagentID): its x-claude-code-agent-id on a
// request that offers tools and names no kind, the turn
// desktopSubagentPick and desktopSubagentEffort take. Claude Code sends the
// id from a terminal as from Desktop, so both are tagged; a subagent's
// tool-less helpers aren't its turns. agent keeps Desktop's haiku search
// helper rule off terminal Claude Code. known is a Desktop pick or effort
// that found the turn already, so the body isn't decoded again, and a
// request without the id isn't decoded at all.
func subagentTurnOf(r *http.Request, agent, kind string, body []byte, known bool) string {
	sub, _ := subagentOf(r.Header)
	if sub == "" || kind != "" || !known && (!hasTools(body) || desktopSearchHelper(agent, body)) {
		return ""
	}
	return sub
}
