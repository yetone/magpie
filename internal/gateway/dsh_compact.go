package gateway

import (
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// movedDshCompact is the reason DSH's compaction went on the model set for
// it (provider.DshCompactModel) rather than the one it asked for.
const movedDshCompact = "dsh-compact"

// movedDshCompactRested is the reason DSH's compaction went on the same
// model from another provider: the one it asked for rests out of its
// limit (dshCompactElsewhere).
const movedDshCompactRested = "dsh-compact-rested"

// dshCompactTo is the model a DeepSeek Harness compaction (kind, as
// serve recorded it: DSH's mark, or its compaction ask on a route that
// sends no mark) goes on: the one set for DSH's compactions, "" to leave
// it on asked. A switch away from a model whose provider is out of its
// limit compacts on that model first, and failed there (#5395). The model
// set must be enabled, served by its exact id and allowed to the gateway
// key; when it isn't, the compaction goes on as asked and the log says
// why. The cheap checks come first, so an unset model costs a held
// settings read.
func dshCompactTo(r *http.Request, agent, kind, asked string) string {
	if agent != "dsh" || kind != KindCompact {
		return ""
	}
	to := provider.DshCompactModel()
	if to == "" || to == asked {
		return ""
	}
	if why := compactRefused(r, to); why != "" {
		log.Printf("gateway: DeepSeek Harness's compaction stays on %s, not %s: %s", asked, to, why)
		return ""
	}
	return to
}

// compactRefused says why a compaction can't be put on to, a model or
// routing group: disabled, not served by that exact id, or not the
// gateway key's to use; "" when it can.
func compactRefused(r *http.Request, to string) string {
	if _, off := idOff(to); off {
		return "it is disabled in magpie"
	}
	if w := provider.Unserved(to); w != "" {
		return w
	}
	if who, held := keyHolds(r); held || withGatewayKey(r) {
		g, ms, isGroup := provider.FindGroup(to)
		p, model, ok := provider.Resolve(to)
		if isGroup && !groupGiven(r, who, held, g, ms) || held && !isGroup && (!ok || !modelAllowed(who, p, model)) {
			return "the gateway key may not use it"
		}
	}
	return ""
}

// dshCompactElsewhere is the model a DeepSeek Harness compaction asked on
// asked goes on instead, "" to leave it: when every account of asked's
// provider rests out of its limit, the same model (as magpie's found
// groups merge models, provider.AutoGroupOf) from another provider that
// isn't resting, the largest window first. DSH compacts on the session's
// previous model before a switch to one with a smaller window, so a
// switch away from a provider out of its limit failed on that provider's
// refusal, and the model switched to was never asked (qtwaiter on X:
// WorkBuddy's deepseek-v4.1-flash out of its rate, Trae CN's
// deepseek-v4.1-flash picked, "Cannot switch to magpie/trae-cn/
// deepseek-v4.1-flash: context compaction failed: 429 ... WorkBuddy").
// Without magpie DSH's own DeepSeek routes all have 1M windows and never
// compact on a switch. A routing group's request is left: the group
// already goes past a resting member. The cheap checks come first: only
// a DSH compaction on a resting model reads the served list.
func (s *Server) dshCompactElsewhere(r *http.Request, agent, kind, asked string, from provider.Protocol) string {
	if agent != "dsh" || kind != KindCompact {
		return ""
	}
	if _, _, isGroup := provider.FindGroup(asked); isGroup {
		return ""
	}
	p, model, ok := provider.Resolve(asked)
	if !ok || !s.allResting(p, model, from) {
		return ""
	}
	key := provider.AutoGroupOf(p.ID, model)
	best, window := "", -1
	for _, e := range provider.Served() {
		if e.Group != "" || e.Provider.ID == p.ID || e.Context <= window || provider.AutoGroupOf(e.Provider.ID, e.Model) != key {
			continue
		}
		if compactRefused(r, e.ID) != "" || s.allResting(e.Provider, e.Model, from) {
			continue
		}
		best, window = e.ID, e.Context
	}
	if best == "" {
		log.Printf("gateway: DeepSeek Harness's compaction stays on %s, out of its limit: no other provider serves %s", asked, model)
	}
	return best
}

// allResting says whether every candidate a request for p's model would
// try rests out of its limit, so it would be answered with the refusal
// held for it (quotaHeld, timedHeld) without asking anyone.
func (s *Server) allResting(p provider.Provider, model string, from provider.Protocol) bool {
	cs := s.candidates(p, model, from)
	if _, _, ok := quotaHeld(cs); ok {
		return true
	}
	_, _, ok := timedHeld(cs)
	return ok
}
