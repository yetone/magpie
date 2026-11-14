package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestFx(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".fx", "settings.json")
	read := func() map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &m)
		return m
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"dark","model":"anthropic/claude-sonnet-5","providers":{"local":{"protocol":"openai-chat-completions","base_url":"http://localhost:11434/v1","auth":{"type":"none"}}}}`), 0o644)

	a := fx(home)
	f := a.Field("model")
	if f.Get() != "anthropic/claude-sonnet-5" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	s := read()
	ps := s["providers"].(map[string]any)
	m, ok := ps["magpie"].(map[string]any)
	if !ok || ps["local"] == nil || s["provider"] != "magpie" || s["models"].(map[string]any)["magpie"] != "deepseek/flash" || s["theme"] != "dark" {
		t.Fatalf("settings: %v", s)
	}
	if m["protocol"] != "openai-chat-completions" || m["base_url"] != gatewayV1() || m["auth"].(map[string]any)["type"] != "none" {
		t.Fatalf("provider: %v", m)
	}
	if ms := m["model_metadata"].(map[string]any); ms["deepseek/pro"] == nil || ms["deepseek/flash"] == nil {
		t.Fatalf("models: %v", ms)
	}
	if f.Get() != "magpie/deepseek/flash" || a.Check() != "" {
		t.Fatalf("get: %q, check: %q", f.Get(), a.Check())
	}

	// its own model: magpie steps out, and the gateway's model is set
	if err := f.Set("openai/gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s["provider"] != nil || s["providers"].(map[string]any)["magpie"] != nil || s["providers"].(map[string]any)["local"] == nil ||
		s["models"].(map[string]any)["gateway"] != "openai/gpt-6-sol" || f.Get() != "openai/gpt-6-sol" {
		t.Fatalf("own: %v", s)
	}

	// one of the user's own providers keeps its model
	os.WriteFile(path, []byte(`{"provider":"local","models":{"local":"qwen"}}`), 0o644)
	if f.Get() != "qwen" {
		t.Fatalf("local: %q", f.Get())
	}

	// through magpie, then reset
	f.Set("magpie/deepseek/pro")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s["provider"] != nil || s["providers"].(map[string]any)["magpie"] != nil || s["models"].(map[string]any)["magpie"] != nil {
		t.Fatalf("reset: %v", s)
	}
}

// fx is also the JSON viewer's name: a command of that name on PATH is no
// sign of the agent, its folder is.
func TestFxDetected(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	testenv.Program(t, filepath.Join(bin, "fx"), "#!/bin/sh\n")
	if fx(home).Detected() {
		t.Error("an fx command alone is taken for the agent")
	}
	if err := os.WriteFile(filepath.Join(home, ".fx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if fx(home).Detected() {
		t.Error("a file named .fx is taken for the agent's folder")
	}
	os.Remove(filepath.Join(home, ".fx"))
	if err := os.MkdirAll(filepath.Join(home, ".fx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !fx(home).Detected() {
		t.Error("~/.fx is not taken for the agent")
	}
}

// What the user set on magpie's provider in fx's settings stays through
// every sync and pick, as T3 Code's did after #2030: the reviewer model fx's
// auto permission mode asks, and a tool_choice_mode turned to omit. The
// address, auth and model list stay magpie's; an auth the user changed goes
// back whole, as fx refuses an env beside type none.
func TestFxKeepsTheUsersSettings(t *testing.T) {
	home := syncHome(t)
	path := filepath.Join(home, ".fx", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"dark","providers":{"local":{"protocol":"openai-chat-completions","base_url":"http://localhost:11434/v1","auth":{"type":"none"}}}}`), 0o644)
	a := fx(home)
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path,
		edit.KV{Path: "providers.magpie.reviewer_model", Value: "relay/glm-4.6"},
		edit.KV{Path: "providers.magpie.tool_choice_mode", Value: "omit"},
		edit.KV{Path: "providers.magpie.auth", Value: map[string]any{"type": "bearer", "env": "MY_KEY"}},
		edit.KV{Path: "providers.magpie.base_url", Value: "http://127.0.0.1:1/v1"},
		edit.KV{Path: "providers.magpie.model_metadata", Value: map[string]any{"gone/old": map[string]any{}}},
	); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	check := func(when string) {
		t.Helper()
		var s struct {
			Theme     string `json:"theme"`
			Providers map[string]struct {
				ReviewerModel  string         `json:"reviewer_model"`
				ToolChoiceMode string         `json:"tool_choice_mode"`
				BaseURL        string         `json:"base_url"`
				Auth           map[string]any `json:"auth"`
				ModelMetadata  map[string]any `json:"model_metadata"`
			} `json:"providers"`
		}
		raw := readFile(path)
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("%v\n%s", err, raw)
		}
		p := s.Providers[magpieID]
		if p.ReviewerModel != "relay/glm-4.6" || p.ToolChoiceMode != "omit" {
			t.Errorf("%s: the user's settings not kept:\n%s", when, raw)
		}
		if p.BaseURL == "http://127.0.0.1:1/v1" || p.BaseURL == "" || len(p.Auth) != 1 || p.Auth["type"] != "none" || p.ModelMetadata["gone/old"] != nil || p.ModelMetadata["deepseek/pro"] == nil {
			t.Errorf("%s: magpie's own parts not written:\n%s", when, raw)
		}
		if s.Theme != "dark" || s.Providers["local"].BaseURL == "" {
			t.Errorf("%s: the user's other settings changed:\n%s", when, raw)
		}
	}
	check("sync")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	check("pick")
}

// @beillusyn on X: models through magpie had no effort to pick in fx, so
// each was asked at its vendor's default. An fx that reads a model's
// reasoning_efforts (0.0.17 on) gets each model's levels, weakest first,
// asked as reasoning_effort; an older fx refuses a key it doesn't know and
// with it the whole settings file, so one older or of no known version
// gets none. fx's own effort setting, the user's, is left alone.
func TestFxEfforts(t *testing.T) {
	home := keptEffortHome(t)
	path := filepath.Join(home, ".fx", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"effort":"low","workspaces":{"/w":{"effort":"high"}}}`), 0o644)
	version := "0.0.17"
	was := fxVersion
	fxVersion = func(string) string { return version }
	t.Cleanup(func() { fxVersion = was })

	a := fx(home)
	if err := a.Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	levels := func(model string) (string, bool) {
		return edit.GetJSONRaw(path, "providers.magpie.model_metadata."+strings.ReplaceAll(model, ".", `\.`)+".reasoning_efforts")
	}
	if got, _ := levels("think/deep"); got != `{"none":{"reasoning_effort":"none"},"low":{"reasoning_effort":"low"},"high":{"reasoning_effort":"high"}}` {
		t.Errorf("deep's levels: %s\n%s", got, readFile(path))
	}
	if got, _ := levels("think/wide"); got != `{"low":{"reasoning_effort":"low"},"medium":{"reasoning_effort":"medium"},"high":{"reasoning_effort":"high"}}` {
		t.Errorf("wide's levels: %s", got)
	}
	if e, _ := edit.GetJSON(path, "effort"); e != "low" {
		t.Errorf("the user's effort: %q", e)
	}
	if e, _ := edit.GetJSON(path, "workspaces./w.effort"); e != "high" {
		t.Errorf("the user's workspace effort: %q", e)
	}

	for _, v := range []string{"0.0.16", ""} {
		version = v
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if got, ok := levels("think/deep"); ok {
			t.Errorf("fx %q given levels it refuses: %s", v, got)
		}
		if raw := readFile(path); strings.Contains(raw, "reasoning_efforts") || !strings.Contains(raw, `"think/deep"`) {
			t.Errorf("fx %q:\n%s", v, raw)
		}
	}
	version = "0.0.18"
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, ok := levels("think/deep"); !ok {
		t.Errorf("a later fx given no levels:\n%s", readFile(path))
	}
}

// A level's name fx refuses (its words for no level, a space) is left out,
// and so is each past its sixteen; a model without levels gets none.
func TestFxEffortNames(t *testing.T) {
	got, _ := json.Marshal(fxEfforts([]string{"high", "auto", "Default", "very high", "low", "high"}))
	if string(got) != `{"low":{"reasoning_effort":"low"},"high":{"reasoning_effort":"high"}}` {
		t.Errorf("levels: %s", got)
	}
	var many []string
	for i := range 20 {
		many = append(many, "l"+strconv.Itoa(i))
	}
	if n := len(fxEfforts(many)); n != fxMaxEfforts {
		t.Errorf("%d levels", n)
	}
	if fxEfforts(nil) != nil {
		t.Error("levels for a model without them")
	}
}

// The fx found is the agent only when it says a 0.x version: the JSON
// viewer, also fx, is 30 on, and an fx the installer put in ~/.local/bin
// comes before the one on PATH.
func TestFxVersionIsTheAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fx runs on macOS and Linux")
	}
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	local := filepath.Join(home, ".local", "bin", "fx")
	viewer := filepath.Join(bin, "fx")
	testenv.Program(t, viewer, "#!/bin/sh\n")
	says := map[string]string{}
	wasRun := runVersion
	runVersion = func(b string) string { return says[filepath.Base(filepath.Dir(b))+"/"+filepath.Base(b)] }
	t.Cleanup(func() { runVersion = wasRun })
	says[filepath.Base(bin)+"/fx"] = "39.2.0"
	if v := fxVersion(home); v != "" {
		t.Errorf("the JSON viewer taken for fx: %q", v)
	}
	os.MkdirAll(filepath.Dir(local), 0o755)
	testenv.Program(t, local, "#!/bin/sh\n#local\n")
	says["bin/fx"] = "0.0.17"
	if v := fxVersion(home); v != "0.0.17" {
		t.Errorf("the installer's fx: %q", v)
	}
}
