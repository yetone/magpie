package agent

import (
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// OpenCode 2 reads OpenCode 1's config and turns it into its own words as
// it loads it (packages/core/src/config/normalize.ts at v2.2.6): provider
// into providers, small_model into agents.title.model. A model's reasoning,
// which OpenCode 2 doesn't read, it drops with a WARN on every load, one per
// model ("omitted unsupported legacy setting", kaising00 on X). Where the
// OpenCode the config is for is 2.x, magpie writes the file in OpenCode 2's
// words: magpie's provider under providers only (openCodeNative), its small
// model under agents.title.model. For OpenCode 1, or a version not known,
// it writes OpenCode 1's, which every OpenCode reads.

// openCodeVersion is the version of the opencode on PATH, "" when not
// known; a var so tests can fake it.
var openCodeVersion = func() string { return (&Agent{ID: "opencode", Bin: "opencode"}).InstalledVersion() }

// openCodeTwo is whether the OpenCode at a place is 2.x: the one on this
// machine's PATH, or the distro's, as its probe found it.
func openCodeTwo(at place) bool {
	v := at.version
	if at.spell == nil {
		v = openCodeVersion()
	}
	major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 2
}

// openCodeTitleKey is where OpenCode 2 keeps the model it titles sessions
// with, which OpenCode 1's small_model became.
const openCodeTitleKey = "agents.title.model"

// ocFile is an OpenCode config magpie writes its provider into: two when
// the OpenCode reading it is 2.x, block magpie's provider in OpenCode 1's
// words (magpieProviderJSONAt).
type ocFile struct {
	path  string
	two   bool
	block func() any
}

// sync brings magpie's provider in the file up to date, and writes it
// where it is missing when put (a model of magpie's is chosen there). For
// OpenCode 2 a provider of magpie's under provider, and a small_model of
// magpie's, move to OpenCode 2's words first.
func (f ocFile) sync(put bool) error {
	if !f.two {
		v1 := openCodeKept(f.path, f.block)
		var err error
		if _, ok := edit.GetJSON(f.path, "provider."+magpieID); !ok && put {
			err = edit.SetJSON(f.path, edit.KV{Path: "provider." + magpieID, Value: v1()})
		} else {
			err = syncJSONInOrder(f.path, "provider."+magpieID, v1)
		}
		// and where the file keeps it under providers too, which OpenCode
		// 2 reads instead, there as well (#3883)
		return errors.Join(err, syncOpenCodeNative(f.path, f.block))
	}
	if err := f.moveProvider(); err != nil {
		return err
	}
	if err := f.moveSmall(); err != nil {
		return err
	}
	native := openCodeNativeKept(f.path, f.outputsKept)
	if _, ok := edit.GetJSON(f.path, openCodeNativeKey); !ok && put {
		return edit.SetJSON(f.path, edit.KV{Path: openCodeNativeKey, Value: native()})
	}
	return syncJSONInOrder(f.path, openCodeNativeKey, native)
}

// outputsKept is block with a limit.output the user typed into one of
// magpie's models kept (openCodeOutputsKept).
func (f ocFile) outputsKept() any {
	v := f.block()
	if b, ok := v.(map[string]any); ok {
		if ms, ok := b["models"].(orderedJSON); ok {
			openCodeOutputsKept(f.path, ms)
		}
	}
	return v
}

// moveProvider moves magpie's provider under provider to providers, in
// OpenCode 2's words, as OpenCode 2 itself reads it (openCodeFromV1): what
// the user added to it goes along. Where providers has magpie's already,
// OpenCode 2 reads that one whole and not the other (mergeMaps), so the
// other goes.
func (f ocFile) moveProvider() error {
	raw, ok := edit.GetJSONRaw(f.path, "provider."+magpieID)
	if !ok {
		return nil
	}
	if _, has := edit.GetJSON(f.path, openCodeNativeKey); !has {
		v, ok := openCodeFromV1([]byte(raw))
		if !ok {
			// not a provider OpenCode reads: left as it is
			return nil
		}
		if err := edit.SetJSON(f.path, edit.KV{Path: openCodeNativeKey, Value: v}); err != nil {
			return err
		}
	}
	if err := edit.DelJSON(f.path, "provider."+magpieID); err != nil {
		return err
	}
	// and a provider left with nothing in it
	return f.dropEmpty("provider")
}

// moveSmall moves a small_model of magpie's to agents.title.model, where
// OpenCode 2 reads it, unless the title agent has a model of its own
// already, which OpenCode 2 reads instead.
func (f ocFile) moveSmall() error {
	sm, ok := edit.GetJSON(f.path, "small_model")
	if !ok || !usesMagpie(sm) {
		return nil
	}
	if _, has := edit.GetJSON(f.path, openCodeTitleKey); has {
		return nil
	}
	if err := edit.SetJSON(f.path, edit.KV{Path: openCodeTitleKey, Value: sm}); err != nil {
		return err
	}
	return edit.DelJSON(f.path, "small_model")
}

// small is the file's small model as the OpenCode reading it takes it:
// for OpenCode 2 its title agent's, else small_model.
func (f ocFile) small() string {
	if f.two {
		if v, ok := edit.GetJSONRaw(f.path, openCodeTitleKey); ok {
			return openCodeSelection(v)
		}
	}
	v, _ := edit.GetJSON(f.path, "small_model")
	return v
}

// setSmall sets the file's small model: for OpenCode 2 its title agent's,
// with a small_model, which it would read only without one, gone.
func (f ocFile) setSmall(v string) error {
	if !f.two {
		if v == "" {
			return edit.DelJSON(f.path, "small_model")
		}
		return edit.SetJSON(f.path, edit.KV{Path: "small_model", Value: v})
	}
	if v == "" {
		if err := edit.DelJSON(f.path, "small_model", openCodeTitleKey); err != nil {
			return err
		}
		return f.dropEmpty("agents.title", "agents")
	}
	if err := edit.SetJSON(f.path, edit.KV{Path: openCodeTitleKey, Value: v}); err != nil {
		return err
	}
	return edit.DelJSON(f.path, "small_model")
}

// dropEmpty takes out each of keys, in order, that is an empty object.
func (f ocFile) dropEmpty(keys ...string) error {
	for _, k := range keys {
		if raw, ok := edit.GetJSONRaw(f.path, k); ok {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &m) == nil && m != nil && len(m) == 0 {
				if err := edit.DelJSON(f.path, k); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// openCodeSelection is a model as OpenCode 2 has one, raw: "provider/model"
// (#variant), or {providerID, model, variant}, as "provider/model".
func openCodeSelection(raw string) string {
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s
	}
	var o struct {
		ProviderID string `json:"providerID"`
		Model      string `json:"model"`
	}
	if json.Unmarshal([]byte(raw), &o) != nil || o.ProviderID == "" || o.Model == "" {
		return ""
	}
	return o.ProviderID + "/" + o.Model
}

// openCodeFromV1 is a provider in OpenCode 1's words, raw, in OpenCode 2's,
// as OpenCode 2.2.6 reads it (packages/core/src/v1/config/migrate.ts,
// migrateStandardProvider): npm is package "aisdk:<npm>", api a baseURL in
// settings, options settings but for headers and body, which are the
// provider's own, and setCacheKey, which is compatibility's
// supportsPromptCacheKey. Of a model it keeps the parts magpie's models
// leave to the user, as openCodeNativeKept reads them: its name, limit and
// variants (openCodeNativeVariant); magpie writes the rest of its models
// whole. What OpenCode 2 doesn't read of OpenCode 1's (id, whitelist,
// blacklist) goes, as it goes there.
func openCodeFromV1(raw []byte) (orderedJSON, bool) {
	var p struct {
		API     string                     `json:"api"`
		Name    json.RawMessage            `json:"name"`
		Env     json.RawMessage            `json:"env"`
		NPM     string                     `json:"npm"`
		Options map[string]json.RawMessage `json:"options"`
		Models  json.RawMessage            `json:"models"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	out := orderedJSON{}
	if p.Name != nil {
		out = append(out, jsonPair{"name", p.Name})
	}
	if p.Env != nil {
		out = append(out, jsonPair{"env", p.Env})
	}
	if p.NPM != "" {
		out = append(out, jsonPair{"package", "aisdk:" + p.NPM})
	}
	if ck, ok := p.Options["setCacheKey"]; ok {
		out = append(out, jsonPair{"compatibility", map[string]any{"supportsPromptCacheKey": ck}})
	}
	if p.Options != nil || p.API != "" {
		settings := maps.Clone(p.Options)
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		for _, k := range []string{"setCacheKey", "headers", "body"} {
			delete(settings, k)
		}
		if p.API != "" {
			b, _ := json.Marshal(p.API)
			settings["baseURL"] = b
		}
		out = append(out, jsonPair{"settings", settings})
	}
	for _, k := range []string{"headers", "body"} {
		if v, ok := p.Options[k]; ok {
			out = append(out, jsonPair{k, v})
		}
	}
	if p.Models != nil {
		ms := orderedJSON{}
		for _, mp := range objectPairs(p.Models) {
			var m struct {
				Name     json.RawMessage `json:"name"`
				Limit    json.RawMessage `json:"limit"`
				Variants json.RawMessage `json:"variants"`
			}
			if json.Unmarshal(mp.v.(json.RawMessage), &m) != nil {
				continue
			}
			e := orderedJSON{}
			if m.Name != nil {
				e = append(e, jsonPair{"name", m.Name})
			}
			if m.Limit != nil {
				e = append(e, jsonPair{"limit", m.Limit})
			}
			if m.Variants != nil {
				list := []any{}
				for _, vp := range objectPairs(m.Variants) {
					var opts map[string]any
					if json.Unmarshal(vp.v.(json.RawMessage), &opts) != nil {
						continue
					}
					list = append(list, openCodeNativeVariant(vp.k, opts))
				}
				e = append(e, jsonPair{"variants", list})
			}
			ms = append(ms, jsonPair{mp.k, e})
		}
		out = append(out, jsonPair{"models", ms})
	}
	return out, true
}
