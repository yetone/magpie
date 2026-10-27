package agent

import (
	"encoding/json"
	"slices"

	"github.com/yetone/magpie/internal/edit"
)

// OpenCode 2 keeps a provider under providers, in words of its own, and
// still reads OpenCode 1's provider: a provider named under both is the one
// under providers, whole, and the other only a line in its log
// (packages/core/src/config/normalize.ts, mergeMaps, at v2.1.4). magpie
// writes its provider under provider, which both read. OpenChamber writes a
// provider its form saves under providers and takes it out of provider
// (packages/web/server/lib/opencode/config-v2.js, writeSectionEntry), so
// once it had saved magpie's, magpie's next sync put magpie's back under
// provider while OpenCode 2 went on reading OpenChamber's copy: a model
// added since was missing from OpenCode 2 and OpenChamber (#3883). Where
// the file keeps magpie's provider under providers, magpie writes it there
// too, in OpenCode 2's words; a file without one doesn't get one.
const openCodeNativeKey = "providers." + magpieID

// openCodeNative is magpie's provider block for an OpenCode config, in
// OpenCode 1's words (magpieProviderJSONAt's "opencode" shape), as OpenCode
// 2 reads it from provider (packages/core/src/v1/config/migrate.ts,
// migrateStandardProvider and migrateModel; checked against what 2.1.4's
// opencode debug config makes of it): npm is package "aisdk:<npm>",
// options (magpie's baseURL and apiKey) are settings, a model's modalities
// are capabilities beside tools (true, OpenCode's default), and its
// variants a list of {id, settings, body} (openCodeNativeVariant).
// reasoning, which OpenCode 2 doesn't read, goes.
func openCodeNative(block any) any {
	b, _ := block.(map[string]any)
	out := map[string]any{"name": b["name"]}
	if npm, _ := b["npm"].(string); npm != "" {
		out["package"] = "aisdk:" + npm
	}
	if o, ok := b["options"]; ok {
		out["settings"] = o
	}
	// in the order magpie lists them (#4575): OpenCode lists a provider's
	// models in the file's key order
	ms := orderedJSON{}
	in, _ := b["models"].(orderedJSON)
	for _, p := range in {
		m, _ := p.v.(map[string]any)
		e := map[string]any{"name": m["name"]}
		if mod, ok := m["modalities"].(map[string]any); ok {
			e["capabilities"] = map[string]any{"tools": true, "input": mod["input"], "output": mod["output"]}
		}
		if l, ok := m["limit"]; ok {
			e["limit"] = l
		}
		// a cost is a list of tiers in OpenCode 2, its cache prices
		// under cache (migrateModel's costs)
		if c, ok := m["cost"].(map[string]any); ok {
			e["cost"] = []any{map[string]any{"input": c["input"], "output": c["output"],
				"cache": map[string]any{"read": c["cache_read"], "write": c["cache_write"]}}}
		}
		if vs, ok := m["variants"].(orderedJSON); ok {
			list := []any{}
			for _, p := range vs {
				list = append(list, openCodeNativeVariant(p.k, p.v))
			}
			e["variants"] = list
		}
		ms = append(ms, jsonPair{p.k, e})
	}
	out["models"] = ms
	return out
}

// openCodeSettings are the options of an @ai-sdk/openai-compatible model
// that OpenCode 2 reads itself; any other a variant has goes in the
// request's body as it is (packages/core/src/v1/config/provider-options.ts,
// COMPATIBLE_KEYS).
var openCodeSettings = []string{"apiKey", "baseURL", "headers", "body", "extraBody", "timeout",
	"chunkTimeout", "headerTimeout", "disabled", "reasoningEffort", "textVerbosity", "user", "strictJsonSchema"}

// openCodeNativeVariant is the variant named id, opts its options in
// OpenCode 1's words, as OpenCode 2 has it: the options it reads itself in
// settings, the rest in body, there only when there are some (a fast
// variant's service_tier, beside its extraBody).
func openCodeNativeVariant(id string, opts any) orderedJSON {
	o, _ := opts.(map[string]any)
	settings, body := map[string]any{}, map[string]any{}
	for k, v := range o {
		if slices.Contains(openCodeSettings, k) {
			settings[k] = v
		} else {
			body[k] = v
		}
	}
	out := orderedJSON{{"id", id}, {"settings", settings}}
	if len(body) > 0 {
		out = append(out, jsonPair{"body", body})
	}
	return out
}

// openCodeNativeKept is openCodeKept for magpie's provider under providers:
// magpie's models are its own, written whole, and what else the block holds
// stays (theirsKept), as does a variant the user put on one of them, where
// it is or after magpie's. A variant that says what magpie's of its name
// says is magpie's (openCodeOwnVariant): one the model has no longer goes.
func openCodeNativeKept(path string, block func() any) func() any {
	return theirsKept(path, openCodeNativeKey, func() any {
		v := openCodeNative(block())
		raw, ok := edit.GetJSON(path, openCodeNativeKey+".models")
		if !ok {
			return v
		}
		var have map[string]struct {
			Variants []json.RawMessage `json:"variants"`
		}
		ms, _ := v.(map[string]any)["models"].(orderedJSON)
		if json.Unmarshal([]byte(raw), &have) != nil {
			return v
		}
		idOf := func(x any) string {
			var e struct {
				ID string `json:"id"`
			}
			b, _ := json.Marshal(x)
			json.Unmarshal(b, &e)
			return e.ID
		}
		for i, p := range ms {
			h, ok := have[p.k]
			if !ok {
				continue
			}
			e, _ := p.v.(map[string]any)
			ours, ok := e["variants"].([]any)
			if !ok {
				continue
			}
			for _, rv := range h.Variants {
				vid, own := openCodeOwnVariant(rv)
				if own || vid == "" {
					continue
				}
				if i := slices.IndexFunc(ours, func(o any) bool { return idOf(o) == vid }); i >= 0 {
					ours[i] = rv
				} else {
					ours = append(ours, rv)
				}
			}
			e["variants"] = ours
			ms[i].v = e
		}
		return v
	}, "models")
}

// openCodeOwnVariant is the id of a variant under providers, and whether
// it is magpie's: it says what magpie's of that name says (openCodeVariant)
// and nothing else, its options split as OpenCode 2 splits them or all in
// settings, as OpenChamber writes a variant it carries over from provider
// (config-v2.js, toModelVariants).
func openCodeOwnVariant(raw json.RawMessage) (string, bool) {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	var id string
	json.Unmarshal(v["id"], &id)
	opts := map[string]json.RawMessage{}
	for k, part := range v {
		switch k {
		case "id":
		case "settings", "body":
			var m map[string]json.RawMessage
			if json.Unmarshal(part, &m) != nil {
				return id, false
			}
			for ok, ov := range m {
				if _, twice := opts[ok]; twice {
					return id, false
				}
				opts[ok] = ov
			}
		default:
			return id, false
		}
	}
	b, err := json.Marshal(opts)
	return id, err == nil && id != "" && sameJSON(string(b), openCodeVariant(id))
}

// syncOpenCodeNative brings magpie's provider under providers in the
// OpenCode config at path up to block, magpie's provider in OpenCode 1's
// words, where the file keeps it there.
func syncOpenCodeNative(path string, block func() any) error {
	return syncJSONInOrder(path, openCodeNativeKey, openCodeNativeKept(path, block))
}

// openCodeWiringOff is wiringOff for magpie's provider in the OpenCode
// config at path: under provider, and under providers where the file keeps
// it there, which OpenCode 2 reads instead.
func openCodeWiringOff(name, path, v1, key string) string {
	under := func(prefix string) func(string) (string, bool) {
		return func(k string) (string, bool) { return edit.GetJSON(path, prefix+k) }
	}
	if off := wiringOff(name, path, under("provider."+magpieID+".options."), "baseURL", v1, "apiKey", key); off != "" {
		return off
	}
	if _, ok := edit.GetJSON(path, openCodeNativeKey); !ok {
		return ""
	}
	return wiringOff(name, path, under(openCodeNativeKey+".settings."), "baseURL", v1, "apiKey", key)
}
