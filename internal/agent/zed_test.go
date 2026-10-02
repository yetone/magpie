package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	for key, want := range map[string]string{"max_tokens": "204800", "max_output_tokens": "204800", "capabilities.images": "true"} {
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

func TestZedFlatpakPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux config path")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "config")
	t.Setenv("FLATPAK_XDG_CONFIG_HOME", "")
	if a := zed(home, cfg); a.Path != filepath.Join(cfg, "zed", "settings.json") {
		t.Fatalf("XDG: %s", a.Path)
	}
	flatpak := filepath.Join(home, "flatpak-config")
	t.Setenv("FLATPAK_XDG_CONFIG_HOME", flatpak)
	if a := zed(home, cfg); a.Path != filepath.Join(flatpak, "zed", "settings.json") {
		t.Fatalf("Flatpak: %s", a.Path)
	}
}
