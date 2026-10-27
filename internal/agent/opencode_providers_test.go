package agent

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// magpie's provider under providers says what OpenCode 2 reads magpie's
// provider under provider as: want is what OpenCode 2.1.4's opencode debug
// config printed under providers.magpie for this block in an opencode.json
// (its apiKey, shown there as ***, put back), levels, fast variants, an
// image model and one without levels.
func TestOpenCodeNativeIsOpenCode2s(t *testing.T) {
	block := map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "magpie",
		"options": map[string]any{"baseURL": "http://127.0.0.1:3425/v1", "apiKey": "mk-test"},
		"models": orderedJSON{
			{"think/deep", map[string]any{"name": "Deep", "modalities": map[string]any{"input": []string{"text", "image"}, "output": []string{"text"}},
				"limit": map[string]any{"context": 1000000, "output": 64000}, "reasoning": true, "variants": openCodeVariants([]string{"none", "high", "max"}, true)}},
			{"think/plain", map[string]any{"name": "Plain", "variants": openCodeVariants(nil, false)}},
			{"gpt/fast", map[string]any{"name": "Fast", "variants": openCodeVariants(nil, true)}},
		}}
	const want = `{"name":"magpie","package":"aisdk:@ai-sdk/openai-compatible","settings":{"apiKey":"mk-test","baseURL":"http://127.0.0.1:3425/v1"},"models":{` +
		`"gpt/fast":{"name":"Fast","variants":[{"id":"fast","settings":{"extraBody":{"service_tier":"priority"}},"body":{"service_tier":"priority"}}]},` +
		`"think/deep":{"name":"Deep","capabilities":{"tools":true,"input":["text","image"],"output":["text"]},"variants":[` +
		`{"id":"none","settings":{"reasoningEffort":"none"}},{"id":"high","settings":{"reasoningEffort":"high"}},{"id":"max","settings":{"reasoningEffort":"max"}},` +
		`{"id":"none-fast","settings":{"extraBody":{"service_tier":"priority"},"reasoningEffort":"none"},"body":{"service_tier":"priority"}},` +
		`{"id":"high-fast","settings":{"extraBody":{"service_tier":"priority"},"reasoningEffort":"high"},"body":{"service_tier":"priority"}},` +
		`{"id":"max-fast","settings":{"extraBody":{"service_tier":"priority"},"reasoningEffort":"max"},"body":{"service_tier":"priority"}}],` +
		`"limit":{"context":1000000,"output":64000}},` +
		`"think/plain":{"name":"Plain","variants":[]}}}`
	if got := openCodeNative(block); !sameJSON(want, got) {
		b, _ := json.Marshal(got)
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

// thinkCatalog gives the sandbox a provider whose models are think/deep,
// with levels and images, and think/new, with neither.
func thinkCatalog(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"deep", "new"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "deep", Efforts: []string{"none", "high", "max"}, Images: true},
		{ID: "new"},
	}); err != nil {
		t.Fatal(err)
	}
}

// openChamberSaved does to the OpenCode config at path what OpenChamber's
// form does to a provider it saves (config-v2.js, writeSectionEntry): the
// one under provider goes, and the block under providers is the form's,
// from before think/new: a stale baseURL and key, a header and a setting
// of the user's own, magpie's variants (some of levels deep no longer has,
// one in OpenChamber's split), one the user wrote over and one of their
// own, a modelID, as #3883's block has, and a model the catalog no longer
// has.
func openChamberSaved(t *testing.T, path string) {
	t.Helper()
	const stale = `{"name":"magpie","package":"aisdk:@ai-sdk/openai-compatible",
		"settings":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"old","timeout":600000},
		"headers":{"X-Mine":"1"},
		"models":{
			"think/deep":{"modelID":"think/deep","name":"Deep (old)","variants":[
				{"id":"none","settings":{"reasoningEffort":"none"}},
				{"id":"medium","settings":{"reasoningEffort":"medium"}},
				{"id":"high","settings":{"reasoningEffort":"high","temperature":0.2}},
				{"id":"high-fast","settings":{"reasoningEffort":"high","service_tier":"priority","extraBody":{"service_tier":"priority"}}},
				{"id":"mine","settings":{"reasoningEffort":"low"},"body":{"top_k":5}}]},
			"think/old":{"name":"Old","variants":[]}}}`
	if err := edit.DelJSON(path, "provider."+magpieID); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path, edit.KV{Path: openCodeNativeKey, Value: json.RawMessage(stale)}); err != nil {
		t.Fatal(err)
	}
}

// providersCurrent checks that magpie's provider under providers in the
// OpenCode config at path says what the one under provider says, with what
// openChamberSaved left of the user's kept.
func providersCurrent(t *testing.T, path string) {
	t.Helper()
	raw, ok := edit.GetJSON(path, openCodeNativeKey)
	if !ok {
		t.Fatalf("no %s", openCodeNativeKey)
	}
	var got struct {
		Package  string            `json:"package"`
		Settings map[string]any    `json:"settings"`
		Headers  map[string]string `json:"headers"`
		Models   map[string]struct {
			Name         string            `json:"name"`
			Capabilities map[string]any    `json:"capabilities"`
			Variants     []json.RawMessage `json:"variants"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	var v1 struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if raw1, _ := edit.GetJSON(path, "provider."+magpieID); json.Unmarshal([]byte(raw1), &v1) != nil {
		t.Fatalf("provider: %s", raw1)
	}
	// think/old, gone from the catalog, goes; think/new is there
	ids, want := slices.Sorted(maps.Keys(got.Models)), slices.Sorted(maps.Keys(v1.Models))
	if !slices.Contains(ids, "think/new") || !reflect.DeepEqual(ids, want) {
		t.Fatalf("models %v, provider's %v: %s", ids, want, raw)
	}
	// in the order magpie lists them under provider (#4575)
	if order, v1order := jsonKeyOrder(t, raw, "models"), jsonKeyOrder(t, raw1Of(path), "models"); !slices.Equal(order, v1order) {
		t.Fatalf("models in the order %v, provider's %v", order, v1order)
	}
	base, _ := edit.GetJSON(path, "provider."+magpieID+".options.baseURL")
	key, _ := edit.GetJSON(path, "provider."+magpieID+".options.apiKey")
	if base == "" || got.Settings["baseURL"] != base || got.Settings["apiKey"] != key || got.Package != "aisdk:@ai-sdk/openai-compatible" {
		t.Fatalf("settings %v, package %q; provider's %s %s", got.Settings, got.Package, base, key)
	}
	if got.Settings["timeout"] != float64(600000) || got.Headers["X-Mine"] != "1" {
		t.Fatalf("the user's setting or header is gone: %s", raw)
	}
	deep := got.Models["think/deep"]
	if name, _ := edit.GetJSON(path, "provider."+magpieID+".models.think/deep.name"); deep.Name != name {
		t.Fatalf("deep's name %q, provider's %q", deep.Name, name)
	}
	if in, _ := deep.Capabilities["input"].([]any); !slices.Contains(in, any("image")) {
		t.Fatalf("deep's capabilities %v", deep.Capabilities)
	}
	var vids []string
	for _, v := range deep.Variants {
		id, _ := openCodeOwnVariant(v)
		vids = append(vids, id)
	}
	if !reflect.DeepEqual(vids, []string{"none", "high", "max", "mine"}) {
		t.Fatalf("deep's variants %v: %s", vids, raw)
	}
	if !sameJSON(string(deep.Variants[1]), map[string]any{"id": "high", "settings": map[string]any{"reasoningEffort": "high", "temperature": 0.2}}) {
		t.Fatalf("the user's high is gone: %s", deep.Variants[1])
	}
	if !sameJSON(string(deep.Variants[3]), map[string]any{"id": "mine", "settings": map[string]any{"reasoningEffort": "low"}, "body": map[string]any{"top_k": 5}}) {
		t.Fatalf("the user's own variant is gone: %s", deep.Variants[3])
	}
}

// #3883: once OpenChamber's form had saved magpie's provider under
// providers, which OpenCode 2 reads instead of provider, magpie's sync put
// its provider back under provider and OpenCode 2 never saw think/new.
// The sync brings the block under providers up to magpie's models too;
// a config without one doesn't get one.
func TestOpenCodeProvidersKeptCurrent(t *testing.T) {
	home, _ := codexHome(t, "", "")
	thinkCatalog(t)
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(oc.Path, "providers"); ok {
		t.Fatal("providers written into a config that had none")
	}
	openChamberSaved(t, oc.Path)
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	providersCurrent(t, oc.Path)
	// and the next sync has nothing to write
	var wrote []string
	if err := edit.Filtered(func(p string, b []byte) []byte { wrote = append(wrote, p); return b }, oc.Sync); err != nil {
		t.Fatal(err)
	}
	if len(wrote) > 0 {
		t.Fatalf("a second sync wrote %v", wrote)
	}
	// a model picked again writes it there as well
	openChamberSaved(t, oc.Path)
	if err := oc.Apply("model", "magpie/think/new"); err != nil {
		t.Fatal(err)
	}
	providersCurrent(t, oc.Path)
	// stepped off magpie, its provider goes under both
	if err := oc.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"provider." + magpieID, openCodeNativeKey} {
		if v, ok := edit.GetJSON(oc.Path, k); ok {
			t.Fatalf("%s left: %s", k, v)
		}
	}
}

// OpenChamber, magpie's agent for the OpenCode it runs, writes magpie's
// provider into the same config: its sync, model and step off reach the
// block under providers as well.
func TestOpenChamberProvidersKeptCurrent(t *testing.T) {
	home, _ := codexHome(t, "", "")
	thinkCatalog(t)
	writeFile(t, filepath.Join(home, ".config", "openchamber", "settings.json"), `{"defaultModel":"anthropic/claude-sonnet-5"}`)
	ch := openChamber(home, filepath.Join(home, ".config"))
	if err := ch.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	path := opencode(home, filepath.Join(home, ".config")).Path
	openChamberSaved(t, path)
	if err := ch.Sync(); err != nil {
		t.Fatal(err)
	}
	providersCurrent(t, path)
	openChamberSaved(t, path)
	if err := ch.Apply("model", "magpie/think/new"); err != nil {
		t.Fatal(err)
	}
	providersCurrent(t, path)
	if err := ch.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"provider." + magpieID, openCodeNativeKey} {
		if v, ok := edit.GetJSON(path, k); ok {
			t.Fatalf("%s left: %s", k, v)
		}
	}
}

// What says whether OpenCode reaches magpie reads the block under
// providers too, which OpenCode 2 sends by: one at another address is off
// until a sync brings it back.
func TestOpenCodeCheckReadsProviders(t *testing.T) {
	home, _ := codexHome(t, "", "")
	thinkCatalog(t)
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if off := oc.Check(); off != "" {
		t.Fatalf("off on magpie's own: %s", off)
	}
	if err := edit.SetJSON(oc.Path, edit.KV{Path: openCodeNativeKey, Value: map[string]any{
		"settings": map[string]any{"baseURL": "http://127.0.0.1:1/v1", "apiKey": "old"}}}); err != nil {
		t.Fatal(err)
	}
	if off := oc.Check(); !strings.Contains(off, "baseURL") || !strings.Contains(off, "127.0.0.1:1/v1") {
		t.Fatalf("a block under providers at another address: %q", off)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if off := oc.Check(); off != "" {
		t.Fatalf("after a sync: %s", off)
	}
}

func raw1Of(path string) string {
	raw, _ := edit.GetJSON(path, "provider."+magpieID)
	return raw
}

// jsonKeyOrder is the order of the keys of the object under key in the
// JSON object raw.
func jsonKeyOrder(t *testing.T, raw, key string) []string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(top[key]))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is no object: %s", key, top[key])
	}
	var keys []string
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := d.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// magpie's models go under providers in the order magpie lists them, as
// under provider (#4575), and a model's cost in OpenCode 2's words: a list
// of tiers, its cache prices under cache (migrateModel's costs at v2.2.5).
func TestOpenCodeNativeKeepsOrderAndCost(t *testing.T) {
	block := map[string]any{"name": "magpie", "npm": "@ai-sdk/openai-compatible", "models": orderedJSON{
		{"zeta/z", map[string]any{"name": "Z", "cost": map[string]any{"input": 1.0, "output": 2.0, "cache_read": 0.1, "cache_write": 1.25}}},
		{"alpha/a", map[string]any{"name": "A"}},
	}}
	b, err := json.Marshal(openCodeNative(block))
	if err != nil {
		t.Fatal(err)
	}
	if order := jsonKeyOrder(t, string(b), "models"); !reflect.DeepEqual(order, []string{"zeta/z", "alpha/a"}) {
		t.Fatalf("order %v: %s", order, b)
	}
	var got struct {
		Models map[string]struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := `[{"cache":{"read":0.1,"write":1.25},"input":1,"output":2}]`; string(got.Models["zeta/z"].Cost) != want {
		t.Fatalf("cost %s, want %s", got.Models["zeta/z"].Cost, want)
	}
	if got.Models["alpha/a"].Cost != nil {
		t.Fatalf("a model with no price got a cost: %s", got.Models["alpha/a"].Cost)
	}
}
