package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

func TestOpenCodeDiscoversFrom(t *testing.T) {
	for v, want := range map[string]bool{
		"": false, "junk": false, "0.15.0": false, "1.2.0": false, "1.3.9": false,
		"1.4.0": true, "1.19.6": true, "v1.19.6": true, "2.0.0": false, "2.0.4": false,
		"2.0.5": true, "2.3.2": true, "2.10.0": true, "3.0.0": true,
	} {
		if got := openCodeDiscovers(v); got != want {
			t.Errorf("openCodeDiscovers(%q) = %v, want %v", v, got, want)
		}
	}
}

// pluginOf is magpie's plugin beside the OpenCode config at path, "" with
// none.
func pluginOf(path string) string {
	b, _ := os.ReadFile(openCodePluginPath(path))
	return string(b)
}

// modelsUnder is the file's models under key, by id.
func modelsUnder(t *testing.T, path, key string) map[string]any {
	t.Helper()
	raw, ok := edit.GetJSON(path, key+".models")
	if !ok {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// chawyehsu on X: OpenCode finds magpie's models itself, as
// yuhp/opencode-models-discovery does, so opencode.json has no list of
// them. For an OpenCode that loads plugins so, magpie's provider is in
// the file without models, and magpie's plugin, with the list it falls
// back on, beside it; both go when magpie's provider does.
func TestOpenCodeFindsMagpiesModels(t *testing.T) {
	for _, c := range []struct{ version, key string }{{"1.19.6", "provider." + magpieID}, {"2.3.2", openCodeNativeKey}} {
		t.Run(c.version, func(t *testing.T) {
			home, _ := codexHome(t, "", "")
			reasonCatalog(t)
			openCodeAt(t, c.version)
			oc := opencode(home, filepath.Join(home, ".config"))
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			raw := readFile(oc.Path)
			if _, ok := edit.GetJSON(oc.Path, c.key); !ok {
				t.Fatalf("no %s:\n%s", c.key, raw)
			}
			if ms, ok := edit.GetJSON(oc.Path, c.key+".models"); ok {
				t.Fatalf("models listed in the file: %s", ms)
			}
			p := pluginOf(oc.Path)
			if !strings.HasPrefix(p, openCodePluginMark) {
				t.Fatalf("no plugin of magpie's: %q", p)
			}
			for _, w := range []string{`"think/deep"`, `"think/new"`, `"rsn/plan"`, gatewayV1(), `"v1":`, `"v2":`} {
				if !strings.Contains(p, w) {
					t.Fatalf("the plugin has no %s:\n%s", w, p)
				}
			}
			if strings.Contains(p, "__MAGPIE__") {
				t.Fatal("the plugin's list was never filled in")
			}
			if off := oc.Check(); off != "" {
				t.Fatalf("Check: %s", off)
			}
			var wrote []string
			if err := edit.Filtered(func(p string, b []byte) []byte { wrote = append(wrote, p); return b }, oc.Sync); err != nil {
				t.Fatal(err)
			}
			if len(wrote) > 0 {
				t.Fatalf("a second sync wrote %v", wrote)
			}
			if err := oc.Apply("model", ""); err != nil {
				t.Fatal(err)
			}
			if v, ok := edit.GetJSON(oc.Path, c.key); ok {
				t.Fatalf("%s left: %s", c.key, v)
			}
			if _, err := os.Stat(openCodePluginPath(oc.Path)); !os.IsNotExist(err) {
				t.Fatalf("the plugin outlived magpie's provider: %v", err)
			}
		})
	}
}

// An OpenCode that doesn't load such a plugin (before 1.4, 2.0.0 to
// 2.0.4), or of a version magpie can't tell, has the list in the file as
// before; and one moving back to such an OpenCode gets the list back and
// magpie's plugin taken away.
func TestOpenCodeBeforePluginsKeepsTheList(t *testing.T) {
	for _, v := range []string{"", "1.3.9", "2.0.4"} {
		t.Run("v"+v, func(t *testing.T) {
			home, _ := codexHome(t, "", "")
			reasonCatalog(t)
			openCodeAt(t, "2.3.2")
			oc := opencode(home, filepath.Join(home, ".config"))
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			if pluginOf(oc.Path) == "" {
				t.Fatal("no plugin for 2.3.2")
			}
			openCodeAt(t, v)
			if err := oc.Sync(); err != nil {
				t.Fatal(err)
			}
			key := "provider." + magpieID
			if strings.HasPrefix(v, "2.") {
				key = openCodeNativeKey
			}
			ms := modelsUnder(t, oc.Path, key)
			for _, id := range []string{"think/deep", "think/new", "rsn/plan"} {
				if ms[id] == nil {
					t.Fatalf("no %s under %s:\n%s", id, key, readFile(oc.Path))
				}
			}
			if p := pluginOf(oc.Path); p != "" {
				t.Fatalf("the plugin stayed for %q", v)
			}
		})
	}
}

// A config magpie wrote the list into moves: magpie's models go, and of
// them only what the user said stays (a variant of theirs, a reply limit
// they typed, with the rest of that limit), with a model magpie no
// longer lists gone, the user's other providers and options untouched. A
// model the user adds afterwards is theirs.
func TestOpenCodeListMovesToThePlugin(t *testing.T) {
	for _, c := range []struct{ version, key string }{{"1.19.6", "provider." + magpieID}, {"2.3.2", openCodeNativeKey}} {
		t.Run(c.version, func(t *testing.T) {
			home, _ := codexHome(t, "", "")
			reasonCatalog(t)
			old := "1.3.9"
			if strings.HasPrefix(c.version, "2.") {
				old = "2.0.4"
			}
			openCodeAt(t, old)
			oc := opencode(home, filepath.Join(home, ".config"))
			if err := oc.Apply("model", "magpie/think/deep"); err != nil {
				t.Fatal(err)
			}
			// the user's own: a provider, a timeout, a variant, a reply
			// limit; and a model of magpie's from before
			mine := map[string]any{"reasoningEffort": "low", "temperature": 0.3}
			var variant any = mine
			if strings.HasPrefix(c.version, "2.") {
				variant = map[string]any{"id": "mine", "settings": mine}
				ms := modelsUnder(t, oc.Path, c.key)
				deep := ms["think/deep"].(map[string]any)
				deep["variants"] = append(deep["variants"].([]any), variant)
				if err := edit.SetJSON(oc.Path, edit.KV{Path: c.key + ".models", Value: ms}); err != nil {
					t.Fatal(err)
				}
			} else if err := edit.SetJSON(oc.Path, edit.KV{Path: c.key + ".models.think/deep.variants.mine", Value: variant}); err != nil {
				t.Fatal(err)
			}
			settings := "options"
			if strings.HasPrefix(c.version, "2.") {
				settings = "settings"
			}
			if err := edit.SetJSON(oc.Path,
				edit.KV{Path: c.key + ".models.rsn/plan.limit.output", Value: 1234},
				edit.KV{Path: c.key + ".models.gone/old", Value: map[string]any{"name": "old"}},
				edit.KV{Path: c.key + "." + settings + ".timeout", Value: 600000},
				edit.KV{Path: "provider.other", Value: map[string]any{"npm": "@ai-sdk/openai-compatible", "models": map[string]any{"x": map[string]any{}}}},
			); err != nil {
				t.Fatal(err)
			}
			openCodeAt(t, c.version)
			if err := oc.Sync(); err != nil {
				t.Fatal(err)
			}
			raw := readFile(oc.Path)
			ms := modelsUnder(t, oc.Path, c.key)
			if ms["gone/old"] != nil || ms["think/new"] != nil {
				t.Fatalf("magpie's models stayed:\n%s", raw)
			}
			deep, _ := ms["think/deep"].(map[string]any)
			if _, has := deep["name"]; has {
				t.Fatalf("magpie's fields stayed on think/deep:\n%s", raw)
			}
			if strings.HasPrefix(c.version, "2.") {
				vs, _ := deep["variants"].([]any)
				if len(vs) != 1 || !sameJSONValue(vs[0], variant) {
					t.Fatalf("think/deep's variants %v, want the user's alone:\n%s", vs, raw)
				}
			} else {
				vs, _ := deep["variants"].(map[string]any)
				if len(vs) != 1 || !sameJSONValue(vs["mine"], variant) {
					t.Fatalf("think/deep's variants %v, want the user's alone:\n%s", vs, raw)
				}
			}
			plan, _ := ms["rsn/plan"].(map[string]any)
			// the limit stays whole, which OpenCode 1 needs ("Missing key
			// …limit.context" for one without its context, 1.19.6)
			if l, _ := plan["limit"].(map[string]any); l == nil || l["output"] != float64(1234) || l["context"] != float64(200000) {
				t.Fatalf("rsn/plan's own limit.output lost, or its limit not whole:\n%s", raw)
			}
			if v, _ := edit.GetJSON(oc.Path, c.key+"."+settings+".timeout"); v != "600000" {
				t.Fatalf("the user's timeout = %q:\n%s", v, raw)
			}
			if _, ok := edit.GetJSON(oc.Path, "provider.other.models.x"); !ok {
				t.Fatalf("the user's provider changed:\n%s", raw)
			}
			if pluginOf(oc.Path) == "" {
				t.Fatal("no plugin")
			}
			// a model the user adds now is theirs
			if err := edit.SetJSON(oc.Path, edit.KV{Path: c.key + ".models.local/own", Value: map[string]any{"name": "own"}}); err != nil {
				t.Fatal(err)
			}
			if err := oc.Sync(); err != nil {
				t.Fatal(err)
			}
			if modelsUnder(t, oc.Path, c.key)["local/own"] == nil {
				t.Fatalf("the user's model went:\n%s", readFile(oc.Path))
			}
		})
	}
}

// A file at the plugin's path magpie didn't write is the user's: never
// written over, and never removed.
func TestOpenCodeUsersOwnPluginStays(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "1.19.6")
	oc := opencode(home, filepath.Join(home, ".config"))
	path := openCodePluginPath(oc.Path)
	os.MkdirAll(filepath.Dir(path), 0o755)
	const own = "export default {}\n"
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if got := pluginOf(oc.Path); got != own {
		t.Fatalf("the user's file written over: %q", got)
	}
	if err := oc.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := pluginOf(oc.Path); got != own {
		t.Fatalf("the user's file removed: %q", got)
	}
}

// The plugin as OpenCode runs it (opencode_models.js, under node): for
// OpenCode 1 its config hook fills magpie's provider from the gateway,
// the user's fields over magpie's and a model of theirs kept; for OpenCode
// 2 its transform does, every model with variants; with the gateway down,
// both fall back on the list in the file.
func TestOpenCodePluginRuns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node to run the plugin")
	}
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	src := openCodePluginFor(magpieProviderJSONAt("opencode", "opencode", "http://127.0.0.1:1"))
	plugin := filepath.Join(home, "magpie-models.mjs")
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	served, _ := json.Marshal(map[string]any{"opencode": map[string]any{"models": []any{
		map[string]any{"id": "live/one", "entry": map[string]any{"name": "One", "limit": map[string]any{"context": 1000, "output": 100}, "variants": map[string]any{"high": map[string]any{"reasoningEffort": "high"}}}},
	}}, "opencode2": map[string]any{"models": []any{
		map[string]any{"id": "live/one", "entry": map[string]any{"name": "One", "limit": map[string]any{"context": 1000, "output": 100}, "variants": []any{}}},
	}}})
	run := func(mode string) map[string]any {
		t.Helper()
		abs, _ := filepath.Abs(filepath.Join("testdata", "opencode", "drive-plugin.mjs"))
		cmd := exec.Command(node, abs, plugin, mode)
		cmd.Env = append(os.Environ(), "MAGPIE_SERVED="+string(served))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		var r map[string]any
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		return r
	}
	get := func(v any, path ...string) any {
		for _, k := range path {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		return v
	}
	// OpenCode 1, gateway up
	r := run("v1-up")
	if get(r, "live/one", "name") != "One" || get(r, "live/one", "limit", "output") != float64(4321) ||
		get(r, "live/one", "limit", "context") != float64(1000) || get(r, "live/one", "variants", "high", "reasoningEffort") != "high" ||
		get(r, "live/one", "variants", "mine", "temperature") != 0.5 || get(r, "local/own", "name") != "own" || r["think/deep"] != nil {
		t.Fatalf("OpenCode 1, magpie up: %v", r)
	}
	// down: the list the file has
	r = run("v1-down")
	if get(r, "think/deep", "name") == nil || get(r, "rsn/plan", "name") == nil || get(r, "local/own", "name") != "own" || get(r, "live/one", "name") != nil {
		t.Fatalf("OpenCode 1, magpie down: %v", r)
	}
	r = run("v2-up")
	if get(r, "live/one", "name") != "One" || get(r, "live/one", "limit", "context") != float64(1000) || r["think/deep"] != nil {
		t.Fatalf("OpenCode 2, magpie up: %v", r)
	}
	if vs, ok := get(r, "live/one", "variants").([]any); !ok || len(vs) != 0 {
		t.Fatalf("OpenCode 2's variants: %v", r)
	}
	r = run("v2-down")
	if get(r, "think/deep", "name") == nil || get(r, "rsn/plan", "name") == nil {
		t.Fatalf("OpenCode 2, magpie down: %v", r)
	}
	if vs, ok := get(r, "think/deep", "variants").([]any); !ok || len(vs) == 0 {
		t.Fatalf("OpenCode 2's think/deep has no variants: %v", r)
	}
}
