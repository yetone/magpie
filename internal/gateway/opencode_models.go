package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// The /v1/models formats OpenCode's magpie plugin reads (the agent
// package's opencode_models.js): each of magpie's models for OpenCode as
// opencode.json would list it under magpie's provider, in OpenCode 1's
// words (OpenCodeFormat) or OpenCode 2's (OpenCode2Format). The plugin
// fills magpie's provider with them as OpenCode starts, so OpenCode
// follows magpie's models without a list of them in its config
// (chawyehsu on X).
const (
	OpenCodeFormat  = "opencode"
	OpenCode2Format = "opencode2"
)

// OpenCodeModel is one model of OpenCode's list: its id and its entry as
// the config would have it.
type OpenCodeModel struct {
	ID    string `json:"id"`
	Entry any    `json:"entry"`
}

// OpenCodeModels is magpie's models for OpenCode, in OpenCode 2's words
// when two; set by the agent package, which writes OpenCode's config.
var OpenCodeModels func(two bool) []OpenCodeModel

// openCodeModels answers /v1/models?format=opencode and =opencode2:
// {"models": [{"id", "entry"}]}, a list so the order stays magpie's (a
// JSON object's integer-like keys come first in JavaScript). Only the
// models of OpenCode's catalog the key asking may use (keyListed).
func openCodeModels(w http.ResponseWriter, r *http.Request, two bool) {
	list := []OpenCodeModel{}
	if OpenCodeModels != nil {
		list = OpenCodeModels(two)
	}
	shown, _ := provider.CatalogFor("opencode")
	may := map[string]bool{}
	for _, e := range keyListed(r, shown, provider.ModelOff("opencode")) {
		may[e.ID] = true
	}
	out := make([]OpenCodeModel, 0, len(list))
	for _, m := range list {
		if may[m.ID] {
			out = append(out, m)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"models": out})
}
