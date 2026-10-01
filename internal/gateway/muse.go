package gateway

import (
	"cmp"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// museContext and museOutput are what Muse is told of a model whose window
// or reply size magpie doesn't know.
const (
	museContext = 128000
	museOutput  = 32000
)

// museModels serves Muse Code its model list. Muse asks for it at
// /muse-code/models on its endpoint's host, whatever path the endpoint has,
// before every session, and starts none when it isn't there, whatever model
// it is told to use; a model it lists is one whose metadata["muse-code"]
// says how Muse is to treat it (a row without it is one Muse hides, and a
// list of none it refuses). The ids are the catalog's, which Muse sends back
// as the model it asks /v1/responses for.
func (s *Server) museModels(w http.ResponseWriter, r *http.Request) {
	shown := catalogFor(r)
	labels := provider.Labels(shown)
	data := []map[string]any{}
	for i, e := range shown {
		input := []string{"text"}
		if e.Images {
			input = append(input, "image")
		}
		meta := map[string]any{"name": labels[i], "family": e.Provider.ID, "is_hidden": false,
			"attachment": e.Images, "reasoning": len(e.Efforts) > 0, "temperature": false, "tool_call": true,
			"modalities": map[string]any{"input": input, "output": []string{"text"}},
			"options":    map[string]any{"include": []string{}}, "variants": map[string]any{},
			"description": labels[i] + " via magpie"}
		// the window and the most a reply may hold: Muse hides a model
		// whose limit lacks either, and without a limit asks for replies
		// of 128K tokens, more than most models give; one magpie doesn't
		// know is told 128K and Muse's own 32K
		ctx, out := cmp.Or(e.Context, museContext), e.Output
		if out <= 0 {
			out = min(museOutput, ctx)
		}
		meta["limit"] = map[string]any{"context": ctx, "output": out}
		data = append(data, map[string]any{"id": e.ID, "object": "model", "created": 0, "owned_by": e.Provider.ID,
			"metadata": map[string]any{"muse-code": meta}})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}
