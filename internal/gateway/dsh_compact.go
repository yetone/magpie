package gateway

import (
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// movedDshCompact is the reason DSH's compaction went on the model set for
// it (provider.DshCompactModel) rather than the one it asked for.
const movedDshCompact = "dsh-compact"

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
	why := ""
	if _, off := idOff(to); off {
		why = "it is disabled in magpie"
	} else if w := provider.Unserved(to); w != "" {
		why = w
	} else if who, held := keyHolds(r); held || withGatewayKey(r) {
		g, ms, isGroup := provider.FindGroup(to)
		p, model, ok := provider.Resolve(to)
		if isGroup && !groupGiven(r, who, held, g, ms) || held && !isGroup && (!ok || !modelAllowed(who, p, model)) {
			why = "the gateway key may not use it"
		}
	}
	if why != "" {
		log.Printf("gateway: DeepSeek Harness's compaction stays on %s, not %s: %s", asked, to, why)
		return ""
	}
	return to
}
