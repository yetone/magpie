package agent

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/yetone/magpie/internal/edit"
)

// The API Pi asks magpie's models on, set by hand in Pi's models.json,
// stays (xiaomanweiman on X): T3 Code's Pi runs a provider on
// openai-completions with all of its 71 tools in the context and on
// openai-responses with 3, loading the rest when asked, so they set
// providers.magpie.api to openai-responses, and the next sync wrote
// openai-completions back. Pi reads an api for the provider and one for
// each model (docs/models.md), both theirs to set.
//
// As with a reply limit (output_kept.go), a value in the file is told
// apart by what magpie has wanted there: the stash keeps, per file, the
// APIs magpie handed the provider and each model (apisWantedKey), and one
// in the file that is none of them is the user's. A model's api the user
// set keeps its baseUrl and compat as the file has them, which go with it
// (anthropic-messages is asked at the gateway's root, not /v1). With no
// record, a model's is magpie's (unknown); the provider's has only ever
// been openai-completions, so a record of that is taken as given. Only an
// api the file names can be the user's.

// apisWantedKey is where the stash keeps the APIs magpie has wanted in
// the Pi models.json at path, as JSON: {model: [api, …]}, the provider's
// under piProviderAPI, a model left on the provider's as "".
func apisWantedKey(path string) string { return "api:" + path + ":wanted" }

// piProviderAPI is the provider's own api in apisWantedKey's record.
const piProviderAPI = "*"

// piAPIsKept keeps in v, the block magpie writes for its provider in the
// Pi models.json at path, an api the user set there for the provider or a
// model, and records what v wants as wanted.
func piAPIsKept(path string, v any) {
	block, _ := v.(map[string]any)
	if block == nil {
		return
	}
	ms, _ := block["models"].([]map[string]any)
	var file struct {
		API    *string                      `json:"api"`
		Models []map[string]json.RawMessage `json:"models"`
	}
	raw, inFile := edit.GetJSON(path, "providers."+magpieID)
	if !inFile || json.Unmarshal([]byte(raw), &file) != nil {
		inFile = false
	}
	have := map[string]map[string]json.RawMessage{}
	for _, m := range file.Models {
		var id string
		if json.Unmarshal(m["id"], &id) == nil && id != "" {
			have[id] = m
		}
	}

	rec := map[string][]string{}
	if s := stashShared()[apisWantedKey(path)]; s != "" && json.Unmarshal([]byte(s), &rec) != nil {
		rec = map[string][]string{}
	}
	next := map[string][]string{}
	want := func(id, w string) {
		was := rec[id]
		if !slices.Contains(was, w) {
			was = append(slices.Clone(was), w)
			if len(was) > outputsRemembered {
				was = was[len(was)-outputsRemembered:]
			}
		}
		next[id] = was
	}

	// the provider's
	if _, known := rec[piProviderAPI]; !known {
		rec[piProviderAPI] = []string{"openai-completions"}
	}
	w, _ := block["api"].(string)
	if inFile && file.API != nil && *file.API != "" && *file.API != w && !slices.Contains(rec[piProviderAPI], *file.API) {
		block["api"] = *file.API
	}
	want(piProviderAPI, w)
	provAPI, _ := block["api"].(string)

	// each model's
	for _, e := range ms {
		id, _ := e["id"].(string)
		if id == "" {
			continue
		}
		w, _ := e["api"].(string)
		f, ok := have[id]
		var fAPI string
		if ok && f["api"] != nil && json.Unmarshal(f["api"], &fAPI) != nil {
			ok = false
		}
		// an api the file lacks is magpie's: an older magpie's file, or one
		// taken out by hand, which leaves the model on the provider's
		if was, known := rec[id]; ok && known && fAPI != "" && fAPI != w && !slices.Contains(was, fAPI) {
			for _, k := range []string{"api", "baseUrl", "compat"} {
				delete(e, k)
				var x any
				if f[k] != nil && json.Unmarshal(f[k], &x) == nil {
					e[k] = x
				}
			}
		}
		want(id, w)
		// a model left on a provider the user put on Responses is asked
		// there too: pi-ai says strict: false on a tool only when the
		// model's compat says so, as magpie's own Responses models do
		// (piModelJSON, #3055)
		if e["api"] == nil && provAPI == "openai-responses" {
			c, _ := e["compat"].(map[string]any)
			if _, set := c["supportsStrictMode"]; !set {
				c = maps.Clone(c)
				if c == nil {
					c = map[string]any{}
				}
				c["supportsStrictMode"] = true
				e["compat"] = c
			}
		}
	}

	if b, _ := json.Marshal(next); string(b) != stashShared()[apisWantedKey(path)] {
		stash(map[string]string{apisWantedKey(path): string(b)})
	}
}
