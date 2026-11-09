package gateway

import (
	"net/http"
	"slices"
	"strings"

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
//
// The app's picker (2.29.0, the model selector in its main.js) has an
// effort of its own: it gathers the models whose names end "(Low)",
// "(Medium)", "(High)", "(XHigh)" or "(Max)" into one row with a menu of
// those levels, as Google's own catalog names a model's variants (Gemini
// 3.1 Pro (High), (Low)). A model listed once had no effort to pick
// (YujunT46308 on X). So a model with two or more of those levels is
// listed once a level, "<id>:<level>" named "<name> (<Level>)", and the
// app sends the id of the one picked, which the gateway asks for that
// level as it does any model asked at an effort (askedAt).
func antigravityModels(w http.ResponseWriter, r *http.Request) {
	shown := catalogFor(r)
	labels := provider.Labels(shown)
	list := make([]map[string]any, 0, len(shown))
	for i, e := range shown {
		levels := antigravityLevels(e.Efforts)
		if len(levels) == 0 {
			list = append(list, antigravityEntry(e, e.ID, labels[i]))
		}
		for _, l := range levels {
			list = append(list, antigravityEntry(e, e.ID+":"+l, labels[i]+" ("+antigravityLevelNames[l]+")"))
		}
	}
	writeJSON(w, 200, map[string]any{"list": list})
}

// antigravityLevelNames are the levels the app's picker gathers a model's
// variants by, weakest first as it lists them, each as a name ends.
var antigravityLevelNames = map[string]string{"low": "Low", "medium": "Medium", "high": "High", "xhigh": "XHigh", "max": "Max"}

// antigravityLevels are the levels of efforts the app's picker offers,
// weakest first: none unless two or more (one is no choice, and the app
// shows a model with one by its name, level and all).
func antigravityLevels(efforts []string) []string {
	var out []string
	for _, l := range ByStrength(efforts) {
		l = strings.ToLower(l)
		if antigravityLevelNames[l] != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// AntigravityModel is the id Antigravity's app is started on for ref, a
// model or routing group of magpie's (without a level): ref at want, fitted
// to the levels the app's picker offers it (antigravityLevels), else at the
// level the picker itself starts it on, medium or its weakest; ref as it
// is when the picker offers it none, as its catalog lists it so.
func AntigravityModel(ref, want string) string {
	shown, _ := provider.CatalogFor("antigravity")
	i := slices.IndexFunc(shown, func(e provider.Entry) bool { return e.ID == ref })
	if i < 0 {
		return ref
	}
	levels := antigravityLevels(shown[i].Efforts)
	if len(levels) == 0 {
		return ref
	}
	l := ""
	if want != "" {
		l = fitEffort(strings.ToLower(want), levels)
	}
	if !slices.Contains(levels, l) {
		l = levels[0]
		if slices.Contains(levels, "medium") {
			l = "medium"
		}
	}
	return ref + ":" + l
}

// antigravityEntry is e as the app's catalog lists it, by id and name.
func antigravityEntry(e provider.Entry, id, name string) map[string]any {
	m := map[string]any{"modelId": id, "displayName": name}
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
	return m
}
