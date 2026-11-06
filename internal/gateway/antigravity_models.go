package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// AntigravityFormat is the /v1/models format Antigravity's app reads as its
// remote model catalog (its settings.json's models.listSource).
const AntigravityFormat = "antigravity"

// antigravityModels answers /v1/models?format=antigravity: the models the
// key asking may use, as Antigravity's app (2.29.0) reads a remote catalog,
// {"list": [{"modelId", "displayName", "maxTokens", "maxOutputTokens",
// "supportsImages", "supportsThinking"}]}, fetched with the gateway key as
// a bearer token.
//
// Its gateway discovery (<url>/v1/models) reads a model's id alone and
// gives one its built-in catalog doesn't know the defaults of its family,
// a window of 200K tokens whatever the model's (YujunT46308 on X): no
// field of the OpenAI list is read for it, context_window and
// context_length included. Its catalog (models.listSource, or models.list
// inline) is read first and takes a window per model as maxTokens, and a
// name as displayName, which its picker shows. A field magpie doesn't know
// is left out, and the app's family default stands for it.
func antigravityModels(w http.ResponseWriter, r *http.Request) {
	shown := catalogFor(r)
	labels := provider.Labels(shown)
	list := make([]map[string]any, 0, len(shown))
	for i, e := range shown {
		m := map[string]any{"modelId": e.ID, "displayName": labels[i]}
		if e.Context > 0 {
			m["maxTokens"] = e.Context
		}
		if out := e.PublishedOutput(); out > 0 {
			m["maxOutputTokens"] = out
		}
		if e.Images {
			m["supportsImages"] = true
		}
		if e.Thinks() {
			m["supportsThinking"] = true
		}
		list = append(list, m)
	}
	writeJSON(w, 200, map[string]any{"list": list})
}
