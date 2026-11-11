package gateway

import (
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// codexCompactTo is the model a Codex compaction that magpie makes (the
// session's model is one of magpie's, asked for the summary as a turn)
// goes on: the one set for Codex's compactions, "" to leave it on asked.
// A compaction sealed by a ChatGPT account (native) is only read by the
// account that wrote it, so neither it nor one put on such a model is
// moved: the summary magpie asks for is plain text any model reads back.
// The model set must be enabled, served by its exact id and allowed to
// the gateway key; when it isn't, the compaction goes on as asked and the
// log says why. nativeOf says whether a model compacts natively.
func codexCompactTo(r *http.Request, asked string, native bool, nativeOf func(string) bool) string {
	if native {
		return ""
	}
	to := provider.CodexCompactModel()
	if to == "" || to == asked {
		return ""
	}
	if nativeOf(to) {
		log.Printf("gateway: Codex's compaction stays on %s, not %s: %s compacts natively, and its summary only its own account reads", asked, to, to)
		return ""
	}
	if why := compactRefused(r, to); why != "" {
		log.Printf("gateway: Codex's compaction stays on %s, not %s: %s", asked, to, why)
		return ""
	}
	return to
}
