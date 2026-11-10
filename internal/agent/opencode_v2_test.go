package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeAt has the OpenCode on PATH say version v for the test.
func openCodeAt(t *testing.T, v string) {
	t.Helper()
	was := openCodeVersion
	openCodeVersion = func() string { return v }
	t.Cleanup(func() { openCodeVersion = was })
}

// reasonCatalog gives the sandbox think's models (thinkCatalog) and
// rsn/plan, a model that reasons with no levels to pick, which magpie
// marks reasoning for OpenCode 1 (openCodeReasons).
func reasonCatalog(t *testing.T) {
	t.Helper()
	thinkCatalog(t)
	if err := provider.Save(provider.Provider{ID: "rsn", Name: "Rsn", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"plan"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("rsn", "https://example.test/v1", []catalog.Model{{ID: "plan", Reasoning: true, Context: 200000, Output: 64000}}); err != nil {
		t.Fatal(err)
	}
}

// nativeOnly checks magpie's provider under providers is OpenCode 2's,
// with models among them, and none says reasoning.
func nativeOnly(t *testing.T, path string, models ...string) {
	t.Helper()
	raw, ok := edit.GetJSON(path, openCodeNativeKey)
	if !ok {
		t.Fatalf("no %s", openCodeNativeKey)
	}
	var got struct {
		Package  string                     `json:"package"`
		Settings map[string]any             `json:"settings"`
		Models   map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Package != "aisdk:@ai-sdk/openai-compatible" || got.Settings["baseURL"] != gatewayV1() || got.Settings["apiKey"] == nil {
		t.Fatalf("not magpie's provider: %s", raw)
	}
	for _, m := range models {
		if _, ok := got.Models[m]; !ok {
			t.Fatalf("no %s: %s", m, raw)
		}
	}
}

// kaising00 on X: for OpenCode 2 magpie wrote OpenCode 1's words, which
// OpenCode 2 turns into its own as it loads them, and a model's reasoning,
// which it doesn't read, it warned of on every load ("omitted unsupported
// legacy setting", OpenCode 2.2.6). For an OpenCode 2 magpie's provider is
// under providers alone, in OpenCode 2's words, the small model is the
// title agent's, and nothing says reasoning.
func TestOpenCode2WritesItsOwnWords(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "2.2.6")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/rsn/plan"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("small", "magpie/think/new"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	raw := readFile(oc.Path)
	for _, k := range []string{"provider", "small_model"} {
		if v, ok := edit.GetJSON(oc.Path, k); ok {
			t.Fatalf("OpenCode 1's %s written for OpenCode 2: %s\n%s", k, v, raw)
		}
	}
	if strings.Contains(raw, `"reasoning"`) {
		t.Fatalf("reasoning written for OpenCode 2:\n%s", raw)
	}
	nativeOnly(t, oc.Path, "rsn/plan", "think/deep", "think/new")
	if v, _ := edit.GetJSON(oc.Path, openCodeTitleKey); v != "magpie/think/new" {
		t.Fatalf("agents.title.model = %q\n%s", v, raw)
	}
	if v, _ := edit.GetJSON(oc.Path, "model"); v != "magpie/rsn/plan" {
		t.Fatalf("model = %q", v)
	}
	if got := oc.Values()["small"]; got != "magpie/think/new" {
		t.Fatalf("small reads %q", got)
	}
	if off := oc.Check(); off != "" {
		t.Fatalf("Check: %s", off)
	}
	// the next sync has nothing to write
	var wrote []string
	if err := edit.Filtered(func(p string, b []byte) []byte { wrote = append(wrote, p); return b }, oc.Sync); err != nil {
		t.Fatal(err)
	}
	if len(wrote) > 0 {
		t.Fatalf("a second sync wrote %v", wrote)
	}
	// the small model off: its key goes, and the agents it was in
	if err := oc.Apply("small", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(oc.Path, "agents"); ok {
		t.Fatalf("agents left: %s", v)
	}
	// stepped off magpie, its provider goes
	if err := oc.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(oc.Path, openCodeNativeKey); ok {
		t.Fatalf("providers.magpie left: %s", v)
	}
}

// OpenCode 1 is written as before: provider, small_model and reasoning,
// and nothing of OpenCode 2's.
func TestOpenCode1KeepsItsWords(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "1.19.5")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/rsn/plan"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("small", "magpie/think/new"); err != nil {
		t.Fatal(err)
	}
	raw := readFile(oc.Path)
	for _, k := range []string{"providers", "agents"} {
		if v, ok := edit.GetJSON(oc.Path, k); ok {
			t.Fatalf("OpenCode 2's %s written for OpenCode 1: %s", k, v)
		}
	}
	if v, _ := edit.GetJSON(oc.Path, "provider.magpie.models.rsn/plan.reasoning"); v != "true" {
		t.Fatalf("rsn/plan not marked reasoning for OpenCode 1:\n%s", raw)
	}
	if v, _ := edit.GetJSON(oc.Path, "small_model"); v != "magpie/think/new" {
		t.Fatalf("small_model = %q", v)
	}
}

// A file magpie wrote for OpenCode 1 moves to OpenCode 2's words once
// OpenCode 2 is installed: magpie's provider, with what the user added to
// it (a header, a timeout, their setCacheKey, a variant of their own, a
// reply limit they typed), and a small model of magpie's. Their own
// provider under provider, and a key of theirs, stay as they are.
func TestOpenCode2MovesAnOpenCode1File(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "1.19.5")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("small", "magpie/rsn/plan"); err != nil {
		t.Fatal(err)
	}
	mine := `{"npm":"@ai-sdk/anthropic","options":{"apiKey":"theirs"},"models":{"m":{"reasoning":true}}}`
	if err := edit.SetJSON(oc.Path,
		edit.KV{Path: "provider.magpie.options.headers", Value: map[string]string{"X-Mine": "1"}},
		edit.KV{Path: "provider.magpie.options.timeout", Value: 600000},
		edit.KV{Path: "provider.magpie.options.setCacheKey", Value: false},
		edit.KV{Path: "provider.magpie.models.think/deep.variants.mine", Value: map[string]any{"reasoningEffort": "high", "temperature": 0.3}},
		edit.KV{Path: "provider.magpie.models.rsn/plan.limit.output", Value: 1234},
		edit.KV{Path: "provider.other", Value: json.RawMessage(mine)},
		edit.KV{Path: "theme", Value: "tokyonight"},
	); err != nil {
		t.Fatal(err)
	}
	openCodeAt(t, "2.2.6")
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	raw := readFile(oc.Path)
	if v, ok := edit.GetJSON(oc.Path, "provider.magpie"); ok {
		t.Fatalf("provider.magpie left: %s", v)
	}
	if v, _ := edit.GetJSONRaw(oc.Path, "provider.other"); !sameJSON(v, json.RawMessage(mine)) {
		t.Fatalf("the user's own provider changed: %s", v)
	}
	if v, _ := edit.GetJSON(oc.Path, "theme"); v != "tokyonight" {
		t.Fatalf("theme = %q", v)
	}
	for k, want := range map[string]string{
		openCodeNativeKey + ".headers.X-Mine":                                                      "1",
		openCodeNativeKey + ".settings.timeout":                                                    "600000",
		openCodeNativeKey + ".settings.baseURL":                                                    gatewayV1(),
		openCodeNativeKey + ".compatibility.supportsPromptCacheKey":                                "false",
		openCodeNativeKey + ".models.rsn/plan.limit.output":                                        "1234",
		openCodeNativeKey + ".models.think/deep.variants.#(id==\"mine\").settings.reasoningEffort": "high",
		openCodeNativeKey + ".models.think/deep.variants.#(id==\"mine\").body.temperature":         "0.3",
		openCodeTitleKey: "magpie/rsn/plan",
		"model":          "magpie/think/deep",
	} {
		if v, _ := edit.GetJSON(oc.Path, k); v != want {
			t.Fatalf("%s = %q, want %q\n%s", k, v, want, raw)
		}
	}
	if v, ok := edit.GetJSON(oc.Path, "small_model"); ok {
		t.Fatalf("small_model left: %s", v)
	}
	if native, _ := edit.GetJSONRaw(oc.Path, openCodeNativeKey); strings.Contains(native, `"reasoning"`) {
		t.Fatalf("reasoning left in magpie's provider:\n%s", raw)
	}
	if off := oc.Check(); off != "" {
		t.Fatalf("Check: %s", off)
	}
	// with their own provider gone too, no provider is left empty
	if err := edit.DelJSON(oc.Path, "provider.other"); err != nil {
		t.Fatal(err)
	}
	openCodeAt(t, "1.19.5")
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	openCodeAt(t, "2.2.6")
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(oc.Path, "provider"); ok {
		t.Fatalf("an empty provider left: %s", v)
	}
}

// A small_model of the user's own, not magpie's, is theirs: OpenCode 2
// reads it, and it stays where it is.
func TestOpenCode2LeavesTheirSmallModel(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "2.2.6")
	oc := opencode(home, filepath.Join(home, ".config"))
	writeFile(t, oc.Path, `{"small_model":"anthropic/claude-haiku-5"}`)
	if err := oc.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(oc.Path, "small_model"); v != "anthropic/claude-haiku-5" {
		t.Fatalf("small_model = %q", v)
	}
	if v, ok := edit.GetJSON(oc.Path, "agents"); ok {
		t.Fatalf("agents written: %s", v)
	}
}

// OpenChamber writes magpie's provider into the same file: for an
// OpenCode 2 in OpenCode 2's words too, so its sync and OpenCode's never
// write the file back and forth.
func TestOpenChamberAndOpenCode2AgreeOnWords(t *testing.T) {
	home, _ := codexHome(t, "", "")
	reasonCatalog(t)
	openCodeAt(t, "2.2.6")
	writeFile(t, filepath.Join(home, ".config", "openchamber", "settings.json"), `{}`)
	ch := openChamber(home, filepath.Join(home, ".config"))
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := ch.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := oc.Apply("model", "magpie/rsn/plan"); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(oc.Path, "provider"); ok {
		t.Fatalf("provider written for OpenCode 2: %s", v)
	}
	nativeOnly(t, oc.Path, "rsn/plan", "think/deep", "think/new")
	var wrote []string
	if err := edit.Filtered(func(p string, b []byte) []byte { wrote = append(wrote, p); return b }, func() error {
		for range 2 {
			if err := oc.Sync(); err != nil {
				return err
			}
			if err := ch.Sync(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(wrote) > 0 {
		t.Fatalf("syncs wrote %v", wrote)
	}
}

// Which OpenCode a file is for: this machine's on PATH, or a distro's as
// its probe found it; one not known is written for OpenCode 1, which
// every OpenCode reads.
func TestOpenCodeTwo(t *testing.T) {
	for v, want := range map[string]bool{"2.2.6": true, "v2.0.25": true, "10.1.0": true, "1.19.5": false, "": false, "opencode": false} {
		openCodeAt(t, v)
		if got := openCodeTwo(here(t.TempDir())); got != want {
			t.Errorf("here %q: %v, want %v", v, got, want)
		}
		openCodeAt(t, "2.2.6")
		wsl := place{spell: func(s string) string { return s }, version: v}
		if got := openCodeTwo(wsl); got != want {
			t.Errorf("wsl %q: %v, want %v", v, got, want)
		}
	}
}
