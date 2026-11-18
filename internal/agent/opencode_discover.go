package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// OpenCode finds magpie's models itself (chawyehsu on X, after
// yuhp/opencode-models-discovery): opencode.json keeps magpie's provider
// without a list of its models, and a plugin magpie writes beside it,
// plugins/magpie-models.js in OpenCode's config folder, fills the list
// from the gateway's /v1/models?format=opencode (opencode2 for OpenCode 2)
// each time OpenCode starts. So OpenCode follows magpie's models without a
// sync, and a model the user puts under magpie's provider in the file is
// theirs: the plugin lays its fields over magpie's, and lists one magpie
// doesn't. With magpie not answering, the plugin lists the models magpie
// had when it last wrote the file, which it keeps in the file.
//
// OpenCode loads every {plugin,plugins}/*.{js,ts} in its config folders
// (OpenCode 1.19.6's config/plugin.ts, OpenCode 2's plugin loader). A file
// whose default export is {id, server} is a plugin of OpenCode 1 from
// 1.4.0 (plugin/shared.ts, readV1Plugin), whose config hook runs before
// OpenCode reads its providers (provider.ts: "load plugins first so
// config() hook runs before reading cfg.provider"); before 1.4 each export
// was called as a function, and an object was an error. OpenCode 2 reads
// {id, setup} and its setup's ctx.model.transform from 2.0.5 (ctx.catalog
// before). An OpenCode older than those, or of a version not known, gets
// the list in opencode.json, as before.
//
// Moving a config that has the list: the first sync that finds it takes
// out what magpie wrote there and leaves what the user added to a model
// of magpie's (a variant, a limit.output of theirs), as only the fields
// that say something other than magpie's (modelsLeft).

//go:embed opencode_models.js
var openCodePluginSource string

// openCodePluginMark opens the plugin magpie writes; a file at its path
// without it is the user's own, never written over or removed.
const openCodePluginMark = "// magpie: written by magpie"

// openCodePluginPath is magpie's plugin beside the OpenCode config at
// path.
func openCodePluginPath(path string) string {
	return filepath.Join(filepath.Dir(path), "plugins", "magpie-models.js")
}

// openCodeVersionAt is the version of the OpenCode at a place, "" when not
// known.
func openCodeVersionAt(at place) string {
	if at.spell == nil {
		return openCodeVersion()
	}
	return at.version
}

// openCodeVersionParts is v's major, minor and patch; ok false for a v
// that doesn't start with them.
func openCodeVersionParts(v string) (n [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return n, false
	}
	for i, p := range parts {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		x, err := strconv.Atoi(p[:end])
		if err != nil {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// openCodeDiscovers says whether the OpenCode of version v loads magpie's
// plugin: OpenCode 1 from 1.4.0, OpenCode 2 from 2.0.5.
func openCodeDiscovers(v string) bool {
	n, ok := openCodeVersionParts(v)
	if !ok {
		return false
	}
	at := func(a, b, c int) bool {
		return n[0] > a || n[0] == a && (n[1] > b || n[1] == b && n[2] >= c)
	}
	switch n[0] {
	case 1:
		return at(1, 4, 0)
	case 0:
		return false
	}
	return at(2, 0, 5)
}

// openCodeFileAt is the OpenCode config at path as the OpenCode at a place
// reads it, for agent id (opencode, or a fork that keeps its shape, which
// gets neither OpenCode 2's words nor the plugin).
func openCodeFileAt(at place, path, id string, block func() any) ocFile {
	if id != "opencode" {
		return ocFile{path: path, block: block}
	}
	return ocFile{path: path, two: openCodeTwo(at), discover: openCodeDiscovers(openCodeVersionAt(at)), block: block}
}

// withoutModels is block without its models, which the plugin lists.
func withoutModels(block func() any) func() any {
	return func() any {
		v := block()
		if m, ok := v.(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, x := range m {
				if k != "models" {
					out[k] = x
				}
			}
			return out
		}
		return v
	}
}

// openCodeDiscoverKey is the stash's word that the OpenCode config at path
// had magpie's models taken out of it.
func openCodeDiscoverKey(path string) string { return "opencode-discover:" + path }

// openCodeModelList is the models of magpie's block (its "models", an
// orderedJSON) as the plugin reads them.
func openCodeModelList(block any) []gateway.OpenCodeModel {
	b, _ := block.(map[string]any)
	ms, _ := b["models"].(orderedJSON)
	out := make([]gateway.OpenCodeModel, 0, len(ms))
	for _, p := range ms {
		out = append(out, gateway.OpenCodeModel{ID: p.k, Entry: p.v})
	}
	return out
}

func init() {
	gateway.OpenCodeModels = func(two bool) []gateway.OpenCodeModel {
		block := magpieProviderJSONAt("opencode", "opencode", gateway.URL())
		if two {
			block = openCodeNative(block)
		}
		return openCodeModelList(block)
	}
}

// openCodePluginFor is the plugin's source for magpie's block: the
// gateway's address and key, and the models it lists when magpie doesn't
// answer.
func openCodePluginFor(block any) string {
	b, _ := block.(map[string]any)
	o, _ := b["options"].(map[string]any)
	data, _ := json.Marshal(map[string]any{
		"baseURL": o["baseURL"], "apiKey": o["apiKey"],
		"v1": openCodeModelList(block), "v2": openCodeModelList(openCodeNative(block)),
	})
	return strings.Replace(openCodePluginSource, "__MAGPIE__", string(data), 1)
}

// pluginSynced brings magpie's plugin beside the file to what the file
// says: there while the file has magpie's provider and its OpenCode
// discovers, else gone. A file there magpie didn't write is left as it is.
func (f ocFile) pluginSynced() error {
	path := openCodePluginPath(f.path)
	cur, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cur = nil
	case err != nil:
		return err // unknown: neither written over nor removed
	case !bytes.HasPrefix(edit.TrimBOM(cur), []byte(openCodePluginMark)):
		return nil // the user's own
	}
	_, legacy := edit.GetJSON(f.path, "provider."+magpieID)
	_, native := edit.GetJSON(f.path, openCodeNativeKey)
	if !f.discover || !legacy && !native {
		if cur == nil {
			return nil
		}
		return os.Remove(path)
	}
	want := openCodePluginFor(f.block())
	if string(cur) == want {
		return nil
	}
	return edit.WriteAtomic(path, []byte(want))
}

// openCodePluginGone takes magpie's plugin beside the OpenCode config at
// path out, once magpie's provider is out of it.
func openCodePluginGone(path string) error {
	return ocFile{path: path}.pluginSynced()
}

// modelsLeft takes magpie's models out of the file, under key (provider's
// or providers'), leaving what the user said there. kept is the block as a
// sync would have written it, with what of the user's a sync keeps (a
// variant of theirs, a limit.output they typed: openCodeKept,
// openCodeNativeKept); pure is magpie's own. Of each model magpie lists,
// what stays is the fields of kept's that say other than pure's
// (jsonDelta), which the plugin lays over magpie's. A model magpie doesn't
// list was magpie's while magpie wrote the models whole, and goes the first
// time (moved false); after that it is one the user added, and stays.
func (f ocFile) modelsLeft(key string, kept, pure any, moved bool) error {
	raw, ok := edit.GetJSONRaw(f.path, key+".models")
	if !ok {
		return nil
	}
	have := objectPairs([]byte(raw))
	if have == nil {
		return nil // not an object: left as it is
	}
	ours, keptBy := modelsOf(pure), modelsOf(kept)
	out := orderedJSON{}
	for _, p := range have {
		var cur any
		if json.Unmarshal(p.v.(json.RawMessage), &cur) != nil {
			continue
		}
		base, mine := ours[p.k]
		if !mine {
			if moved {
				out = append(out, jsonPair{p.k, cur})
			}
			continue
		}
		if k, ok := keptBy[p.k]; ok {
			cur = k
		}
		d, keep := jsonDelta(cur, base)
		if !keep {
			continue
		}
		// a limit or cost the user changed stays whole: OpenCode 1 refuses
		// one without its context and output (or input and output: "Missing
		// key …limit.context", 1.19.6), and OpenCode 2 a cost item without
		// its output
		if m, ok := d.(map[string]any); ok {
			c, _ := cur.(map[string]any)
			for _, k := range []string{"limit", "cost"} {
				if _, said := m[k]; said && c[k] != nil {
					m[k] = c[k]
				}
			}
		}
		if key == openCodeNativeKey {
			// with no variants to say, OpenCode 2 would make up its own
			// for the model (Variant.resolve), where magpie's has none
			if m, ok := d.(map[string]any); ok && m["variants"] == nil {
				m["variants"] = []any{}
			}
		}
		out = append(out, jsonPair{p.k, d})
	}
	if len(out) == 0 {
		return edit.DelJSON(f.path, key+".models")
	}
	if sameJSON(raw, out) && sameOrder(raw, out) {
		return nil
	}
	return edit.SetJSON(f.path, edit.KV{Path: key + ".models", Value: out})
}

// modelsOf is the models of a provider block, by id, as JSON decodes them.
func modelsOf(block any) map[string]any {
	var b struct {
		Models map[string]any `json:"models"`
	}
	raw, err := json.Marshal(block)
	if err != nil || json.Unmarshal(raw, &b) != nil || b.Models == nil {
		return map[string]any{}
	}
	return b.Models
}

// jsonDelta is what have says other than base: of objects, the keys whose
// values differ, alike; of lists, the items base doesn't have. keep false
// when nothing is left.
func jsonDelta(have, base any) (any, bool) {
	switch h := have.(type) {
	case map[string]any:
		b, ok := base.(map[string]any)
		if !ok {
			break
		}
		out := map[string]any{}
		for k, hv := range h {
			bv, in := b[k]
			if !in {
				out[k] = hv
				continue
			}
			if d, keep := jsonDelta(hv, bv); keep {
				out[k] = d
			}
		}
		return out, len(out) > 0
	case []any:
		b, ok := base.([]any)
		if !ok {
			break
		}
		var out []any
		for _, x := range h {
			if !slicesContainsDeep(b, x) {
				out = append(out, x)
			}
		}
		return out, len(out) > 0
	}
	return have, !reflect.DeepEqual(have, base)
}

func slicesContainsDeep(xs []any, x any) bool {
	for _, y := range xs {
		if reflect.DeepEqual(x, y) {
			return true
		}
	}
	return false
}
