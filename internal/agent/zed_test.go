package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestZed(t *testing.T) {
	home := syncHome(t)
	credential := zedCredential
	t.Cleanup(func() { zedCredential = credential })
	credentialURL := ""
	zedCredential = func(url string) error { credentialURL = url; return nil }
	a := zedAt(filepath.Join(home, ".config", "zed"))
	original := `{
  // My editor settings stay intact.
  "theme": "One Dark",
  "agent": {"default_model": {"provider": "zed.dev", "model": "claude-sonnet", "temperature": 0.7}, "profiles": {"ask": {"name": "Ask"}}},
  "language_models": {"openai": {"api_url": "https://my-openai/v1"}, "openai_compatible": {"other": {"api_url": "https://other/v1", "available_models": []}}}
}`
	writeFile(t, a.Path, original)
	f := a.Field("model")
	if f.Get() != "zed.dev/claude-sonnet" || !a.Detected() {
		t.Fatalf("original: %q", f.Get())
	}
	v, err := a.Spell("model", "relay/glm-4.6")
	if err != nil || v != "magpie/relay/glm-4.6" {
		t.Fatalf("spelling: %q %v", v, err)
	}
	if err := a.Apply("model", v); err != nil {
		t.Fatal(err)
	}
	if f.Get() != v || a.Check() != "" || credentialURL != gatewayV1() {
		t.Fatalf("applied: %q %q", f.Get(), a.Check())
	}
	for key, want := range map[string]string{
		zedProvider + ".api_url":                                             gatewayV1(),
		zedProvider + ".available_models.0.name":                             "relay/glm-4.6",
		zedProvider + ".available_models.0.max_tokens":                       "204800",
		zedProvider + ".available_models.0.capabilities.tools":               "true",
		zedProvider + ".available_models.0.capabilities.parallel_tool_calls": "false",
		zedProvider + ".available_models.0.capabilities.prompt_cache_key":    "false",
		"language_models.openai.api_url":                                     "https://my-openai/v1",
		"language_models.openai_compatible.other.api_url":                    "https://other/v1",
		"agent.profiles.ask.name":                                            "Ask",
	} {
		if got, _ := edit.GetJSON(a.Path, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(readFile(a.Path), "// My editor settings stay intact.") {
		t.Fatal("lost comments")
	}
	before := readFile(a.Path)
	if err := f.Set("invalid"); err == nil || readFile(a.Path) != before {
		t.Fatal("invalid selection changed settings")
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "unknown"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if n, _ := edit.GetJSON(a.Path, zedProvider+".available_models.#"); n != "2" {
		t.Fatalf("catalog did not sync: %s", readFile(a.Path))
	}
	if err := f.Set("magpie/relay/unknown"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got != "zed.dev/claude-sonnet" {
		t.Fatalf("reset: %q", got)
	}
	if temp, _ := edit.GetJSON(a.Path, zedModel+".temperature"); temp != "0.7" {
		t.Fatal("did not restore whole default model")
	}
	if _, ok := edit.GetJSON(a.Path, zedProvider); ok {
		t.Fatal("magpie provider left after reset")
	}
}

func TestZedCredentialFailureLeavesSettings(t *testing.T) {
	home := syncHome(t)
	a := zedAt(filepath.Join(home, "zed"))
	writeFile(t, a.Path, `{"agent":{"default_model":{"provider":"openai","model":"own"}}}`)
	before := readFile(a.Path)
	credential := zedCredential
	t.Cleanup(func() { zedCredential = credential })
	zedCredential = func(string) error { return errors.New("keychain locked") }
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("missing credential failure: %v", err)
	}
	if readFile(a.Path) != before {
		t.Fatal("changed settings despite credential failure")
	}
}

func TestZedFreshAndOwn(t *testing.T) {
	home := syncHome(t)
	a := zedAt(filepath.Join(home, "zed"))
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("sync created settings without a pick")
	}
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("reset fresh: %q", f.Get())
	}
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("openai/gpt-custom"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "openai/gpt-custom" {
		t.Fatalf("native model: %q", f.Get())
	}
	if _, ok := edit.GetJSON(a.Path, zedProvider); ok {
		t.Fatal("magpie provider left on native selection")
	}
}

// Zed's max_tokens is the window a prompt and its reply share, and it lets
// a prompt fill max_tokens - max_output_tokens before it compacts (its
// thread's input_token_capacity). That room is the prompt magpie's catalog
// says the model takes (#850): GPT-5's input 272000 with its 128000 reply,
// a 400000 window as OpenAI gives it, and a model with no output limit
// gets its context alone.
func TestZedWindowHoldsPromptAndReply(t *testing.T) {
	home := syncHome(t)
	credential := zedCredential
	t.Cleanup(func() { zedCredential = credential; catalog.Reset() })
	zedCredential = func(string) error { return nil }
	writeFile(t, catalog.CachePath(), `{"openai":{"models":{
		"gpt-5":{"id":"gpt-5","name":"GPT-5","limit":{"context":400000,"input":272000,"output":128000}},
		"gpt-5-pro":{"id":"gpt-5-pro","limit":{"context":400000,"input":128000,"output":272000}},
		"plain":{"id":"plain","name":"Plain","limit":{"context":64000}},
		"unknown":{"id":"unknown","limit":{"output":256000}}}},
		"zai":{"models":{"glm-4.6":{"id":"glm-4.6","limit":{"context":204800,"output":131072}}}}}`)
	catalog.Reset()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5", "gpt-5-pro", "plain", "unknown", "glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	a := zedAt(filepath.Join(home, "zed"))
	if err := a.Field("model").Set("magpie/relay/gpt-5"); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Models []struct {
			Name   string `json:"name"`
			Max    int    `json:"max_tokens"`
			Output *int   `json:"max_output_tokens"`
		} `json:"available_models"`
	}
	raw, _ := edit.GetJSON(a.Path, zedProvider)
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range p.Models {
		s := strconv.Itoa(m.Max)
		if m.Output != nil {
			s += " " + strconv.Itoa(*m.Output) + " prompt " + strconv.Itoa(m.Max-*m.Output)
		}
		got[m.Name] = s
	}
	for name, want := range map[string]string{
		"relay/gpt-5":     "400000 128000 prompt 272000",
		"relay/gpt-5-pro": "400000 272000 prompt 128000",
		"relay/plain":     "64000",
		"relay/unknown":   "256000 128000 prompt 128000",
		"relay/glm-4.6":   "335872 131072 prompt 204800",
	} {
		if got[name] != want {
			t.Errorf("%s: max_tokens, max_output_tokens = %q, want %q (all %v)", name, got[name], want, got)
		}
	}
}

func TestZedTokenLimitOverrides(t *testing.T) {
	home := syncHome(t)
	credential := zedCredential
	t.Cleanup(func() { zedCredential = credential })
	zedCredential = func(string) error { return nil }
	writeFile(t, catalog.CachePath(), `{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","limit":{"context":204800,"output":128000}}}}}`)
	catalog.Reset()
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetContext(*p, "glm-4.6", 64000); err != nil {
		t.Fatal(err)
	}
	a := zedAt(filepath.Join(home, "zed"))
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	check := func(output int) {
		t.Helper()
		var m struct {
			Max    int `json:"max_tokens"`
			Output int `json:"max_output_tokens"`
		}
		raw, _ := edit.GetJSON(a.Path, zedProvider+".available_models.0")
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		if m.Output != output || m.Max-m.Output != 64000 {
			t.Errorf("max_tokens=%d, max_output_tokens=%d: want output %d and prompt 64000", m.Max, m.Output, output)
		}
	}
	check(128000)
	if err := provider.SetModelOutput("relay/glm-4.6", 256000); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	check(256000)
}

func TestZedRestoresProviderAndModelLimits(t *testing.T) {
	home := syncHome(t)
	writeFile(t, catalog.CachePath(), `{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM","modalities":{"input":["text","image"]},"limit":{"context":204800,"output":300000}}}}}`)
	catalog.Reset()
	a := zedAt(filepath.Join(home, "zed"))
	writeFile(t, a.Path, `{"agent":{"default_model":{"provider":"openai","model":"own"}},"language_models":{"openai_compatible":{"magpie":{"api_url":"https://my-proxy/v1","available_models":[]}}}}`)
	before := readFile(a.Path)
	if err := a.Sync(); err != nil || readFile(a.Path) != before {
		t.Fatalf("sync changed a user's provider: %v", err)
	}
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	// The independent reply limit keeps its allowance beside the prompt's
	// 204800 tokens, even when the reply may be longer than the prompt.
	for key, want := range map[string]string{"max_tokens": "504800", "max_output_tokens": "300000", "capabilities.images": "true"} {
		if got, _ := edit.GetJSON(a.Path, zedProvider+".available_models.0."+key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetJSON(a.Path, zedProvider+".api_url"); got != "https://my-proxy/v1" || a.Field("model").Get() != "openai/own" {
		t.Fatalf("did not restore original provider and model: %s", readFile(a.Path))
	}
}

func TestZedReselectAfterNativePicker(t *testing.T) {
	home := syncHome(t)
	a := zedAt(filepath.Join(home, "zed"))
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	// Zed's picker changes only the model, leaving our provider installed.
	if err := edit.SetJSON(a.Path, edit.KV{Path: zedModel, Value: map[string]string{"provider": "openai", "model": "gpt-custom"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(a.Path, zedProvider); ok {
		t.Fatal("reset restored magpie's own provider")
	}
	if got := f.Get(); got != "openai/gpt-custom" {
		t.Fatalf("reset model: %q", got)
	}
}

func TestZedReselectPreservesProvider(t *testing.T) {
	home := syncHome(t)
	a := zedAt(filepath.Join(home, "zed"))
	original := `{"api_url":"https://my-proxy/v1","available_models":[{"name":"custom","max_tokens":8192}],"headers":{"X-Custom":"keep"}}`
	writeFile(t, a.Path, `{"agent":{"default_model":{"provider":"openai","model":"own"}},"language_models":{"openai_compatible":{"magpie":`+original+`}}}`)
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	native := `{"provider":"zed.dev","model":"claude-sonnet","temperature":0.3}`
	if err := edit.SetJSON(a.Path, edit.KV{Path: zedModel, Value: json.RawMessage(native)}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetJSON(a.Path, zedProvider); got != original {
		t.Fatalf("reselection lost the user's provider: %s", got)
	}
	if got, _ := edit.GetJSON(a.Path, zedModel); got != native {
		t.Fatalf("reset did not restore the latest native model: %s", got)
	}
}

func TestZedResetAfterNativePicker(t *testing.T) {
	home := syncHome(t)
	a := zedAt(filepath.Join(home, "zed"))
	writeFile(t, a.Path, `{"agent":{"default_model":{"provider":"openai","model":"own"}},"language_models":{"openai_compatible":{"magpie":{"api_url":"https://my-proxy/v1","available_models":[]}}}}`)
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	// Zed's picker changes the model without removing magpie's provider.
	native := `{"provider":"zed.dev","model":"claude-sonnet","temperature":0.3}`
	if err := edit.SetJSON(a.Path, edit.KV{Path: zedModel, Value: json.RawMessage(native)}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetJSON(a.Path, zedModel); got != native {
		t.Fatalf("reset overwrote the native model: %s", got)
	}
	if got, _ := edit.GetJSON(a.Path, zedProvider+".api_url"); got != "https://my-proxy/v1" {
		t.Fatalf("reset did not restore the user's provider: %q", got)
	}
	if was := stashLoad()["zed:"+a.Path+":model"]; was != "" {
		t.Fatalf("reset left stale model stash: %s", was)
	}
}

func TestZedXDGPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux config path")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "config")
	if a := zed(home, cfg); a.Path != filepath.Join(cfg, "zed", "settings.json") {
		t.Fatalf("XDG: %s", a.Path)
	}
}

func TestZedCustomPaths(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "zedg-config")
	t.Setenv("MAGPIE_ZED_BIN", "/opt/zedg/bin/zedg")
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", config)
	t.Setenv("MAGPIE_ZED_PROCESS_NAMES", "zedg,ZedG")
	a := zed(home, filepath.Join(home, ".config"))
	want := filepath.Join(config, "settings.json")
	if a.Bin != "/opt/zedg/bin/zedg" {
		t.Fatalf("custom binary: %q", a.Bin)
	}
	if a.Path != want || a.Dir != filepath.Dir(want) {
		t.Fatalf("custom config path: path=%q dir=%q", a.Path, a.Dir)
	}
}

func TestZedDefaultsStayStable(t *testing.T) {
	t.Setenv("MAGPIE_ZED_BIN", "")
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", "")
	t.Setenv("MAGPIE_ZED_PROCESS_NAMES", "")
	a := zed(t.TempDir(), filepath.Join(t.TempDir(), ".config"))
	if a.Bin != "zed" {
		t.Fatalf("default binary: %q", a.Bin)
	}
	want := `(^|/)(zed|zeditor|zed-editor)( |$)`
	if got := zedProcessNames(); len(got) != 1 || got[0] != want {
		t.Fatalf("default process names: %#v", got)
	}
}

func TestZedCustomProcessNames(t *testing.T) {
	t.Setenv("MAGPIE_ZED_PROCESS_NAMES", "zedg,ZedG")
	want := []string{`(^|/)zedg( |$)`, `(^|/)ZedG( |$)`}
	if got := zedProcessNames(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("custom process names: %#v", got)
	}
}

func TestZedRelativeConfigDirUsesDefault(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", "zedg-config")
	a := zed(home, cfg)
	want := filepath.Join(cfg, "zed", "settings.json")
	if runtime.GOOS == "windows" {
		// Zed's folder on Windows is %APPDATA%\Zed, whatever cfg magpie was given
		want = filepath.Join(home, "AppData", "Roaming", "Zed", "settings.json")
	}
	if a.Path != want {
		t.Fatalf("relative config directory escaped default: %q, want %q", a.Path, want)
	}
}
