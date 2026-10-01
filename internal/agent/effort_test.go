package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// effortHome is a sandbox HOME with a provider for magpie to serve.
func effortHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// where Windows keeps Goose's, Crush's and Devin's: the sandbox's, never the machine's
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("HERMES_HOME", "")
	t.Setenv("MIMOCODE_HOME", "")
	t.Setenv("HANA_HOME", "")
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

// setEffort sets, reads back and resets an agent's effort, checking what
// else is in its file stays.
func setEffort(t *testing.T, a *Agent, v, keep string) {
	t.Helper()
	f := a.Field("effort")
	if f == nil {
		t.Fatalf("%s has no effort field", a.ID)
	}
	if err := f.Set(v); err != nil {
		t.Fatalf("%s: %v", a.ID, err)
	}
	if got := f.Get(); got != v {
		t.Fatalf("%s: effort %q, want %q\n%s", a.ID, got, v, readFile(a.Path))
	}
	found := false
	for _, o := range f.Options(map[string]string{}) {
		found = found || o.Value == v
	}
	if !found {
		t.Fatalf("%s: %q not among the options", a.ID, v)
	}
	if !strings.Contains(readFile(a.Path), keep) {
		t.Fatalf("%s lost %q:\n%s", a.ID, keep, readFile(a.Path))
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got != "" {
		t.Fatalf("%s: reset left %q\n%s", a.ID, got, readFile(a.Path))
	}
	if !strings.Contains(readFile(a.Path), keep) {
		t.Fatalf("%s lost %q on reset:\n%s", a.ID, keep, readFile(a.Path))
	}
}

func TestEffortFields(t *testing.T) {
	home := effortHome(t)
	cfg := filepath.Join(home, ".config")

	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"theme":"dark","model":"opus"}`)
	c := claude(home)
	setEffort(t, c, "xhigh", `"theme"`)
	// max lasts a session in Claude Code's /effort; magpie keeps it as its
	// env (TestClaudeEffortMax)
	setEffort(t, c, "max", `"theme"`)

	writeFile(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  default: x\nagent:\n  max_turns: 9\n")
	h := hermes(home)
	setEffort(t, h, "none", "max_turns: 9")
	if v, _ := edit.GetYAML(h.Path, "model.default"); v != "x" {
		t.Fatalf("hermes model: %q", v)
	}

	writeFile(t, filepath.Join(home, ".omp", "agent", "config.yml"), "modelRoles:\n  default: a/b\n")
	setEffort(t, omp(home), "xhigh", "default: a/b")

	g := goose(home, cfg)
	writeFile(t, g.Path, "GOOSE_MODEL: m\nGOOSE_PROVIDER: p\n")
	setEffort(t, g, "max", "GOOSE_MODEL: m")
	if v, _ := edit.GetYAMLTop(g.Path, "GOOSE_PROVIDER"); v != "p" {
		t.Fatalf("goose provider: %q", v)
	}

	writeFile(t, filepath.Join(home, ".copilot", "settings.json"), `{"model":"gpt-5.5","contextTier":"long"}`)
	setEffort(t, copilot(home), "xhigh", `"contextTier"`)

	cr := crush(home, cfg)
	os.MkdirAll(filepath.Dir(cr.Path), 0o755)
	os.WriteFile(cr.Path, []byte(`{"options":{"debug":true}}`), 0o644)
	// Crush keeps the effort with the large model
	if err := cr.Field("effort").Set("high"); err == nil {
		t.Fatal("crush took an effort without a large model")
	}
	if err := cr.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	setEffort(t, cr, "medium", `"debug"`)
	if cr.Field("model").Get() != "magpie/deepseek/pro" {
		t.Fatalf("crush model: %s", readFile(cr.Path))
	}
}

func TestCommandCodeEffort(t *testing.T) {
	home := effortHome(t)
	path := filepath.Join(home, ".commandcode", "settings.json")
	writeFile(t, path, `{"theme":"dark","reasoningEffort":{"gpt-5.5":"low"}}`)
	a := commandCode(home)
	f := a.Field("effort")
	if err := f.Set("high"); err == nil {
		t.Fatal("took an effort without a model")
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	setEffort(t, a, "max", `"theme"`)
	// each model keeps its own; the other one's stays
	if m := ccEffortMap(path); m["gpt-5.5"] != "low" || len(m) != 1 {
		t.Fatalf("efforts: %v\n%s", m, readFile(path))
	}
	// a model id with dots is one key, not a path
	if err := a.Field("model").Set("anthropic/claude-4.5"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if m := ccEffortMap(path); m["anthropic/claude-4.5"] != "xhigh" || m["gpt-5.5"] != "low" {
		t.Fatalf("efforts: %v\n%s", m, readFile(path))
	}
	// the last one gone, the map goes too
	os.WriteFile(path, []byte(`{"model":"x","reasoningEffort":{"x":"low"}}`), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(path), "reasoningEffort") {
		t.Fatalf("left the map:\n%s", readFile(path))
	}
}

// #473: a model through magpie listing no levels (LongCat-2.5-Preview) had
// dsh's own off, low, high and max offered on the Agents page, and each was
// turned away with "takes an effort of  for this model". It has none in dsh:
// none is offered, and one asked for says so.
func TestDshEffortModelWithoutLevels(t *testing.T) {
	home := effortHome(t)
	if err := catalog.SaveLive("deepseek", "https://api.deepseek.com/v1", []catalog.Model{
		{ID: "pro", Name: "pro", Efforts: []string{"none", "low", "high", "max"}},
		{ID: "plain", Name: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
	writeFile(t, patch, "[]\n")
	a := dsh(home)
	f := a.Field("effort")
	if err := a.Field("model").Set("magpie/deepseek/plain"); err != nil {
		t.Fatal(err)
	}
	cur := map[string]string{"model": "magpie/deepseek/plain"}
	if got := f.Options(cur); len(got) != 0 {
		t.Fatalf("offered %+v for a model with no levels", got)
	}
	err := f.Set("low")
	if err == nil || strings.Contains(err.Error(), "of  for") || !strings.Contains(err.Error(), "deepseek/plain") {
		t.Fatalf("low: %v", err)
	}
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("default: %v %q", err, f.Get())
	}
	// every level offered for any model is one dsh takes
	for _, m := range []string{"magpie/deepseek/pro", "magpie/deepseek/plain", "deepseek-v4-pro"} {
		if err := a.Field("model").Set(m); err != nil {
			t.Fatal(err)
		}
		for _, o := range f.Options(map[string]string{"model": m}) {
			if err := f.Set(o.Value); err != nil {
				t.Fatalf("%s offered %q and turned it away: %v", m, o.Value, err)
			}
		}
	}
}

func TestDshEffort(t *testing.T) {
	home := effortHome(t)
	if err := catalog.SaveLive("deepseek", "https://api.deepseek.com/v1", []catalog.Model{
		{ID: "pro", Name: "pro", Efforts: []string{"none", "low", "high", "max"}},
		{ID: "flash", Name: "flash", Efforts: []string{"high"}},
	}); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
	writeFile(t, patch, "[]\n")
	a := dsh(home)
	f := a.Field("effort")
	if err := f.Set("max"); err == nil {
		t.Fatal("took an effort with no model to go with")
	}
	// dsh's own row takes off, low, high and max (low since #269)
	if err := a.Field("model").Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("medium"); err == nil {
		t.Fatal("took medium on dsh's own row")
	}
	if err := f.Set("max"); err != nil || f.Get() != "max" {
		t.Fatalf("max: %v, effort %q\n%s", err, f.Get(), readFile(patch))
	}
	// a model through magpie offers its own levels, off for its none
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "max" {
		t.Fatalf("effort: %q\n%s", f.Get(), readFile(patch))
	}
	if got := f.Options(map[string]string{"model": "magpie/deepseek/pro"}); len(got) != 4 || got[0].Value != "off" || got[3].Value != "max" {
		t.Fatalf("levels: %+v", got)
	}
	if err := f.Set("medium"); err == nil {
		t.Fatal("took medium")
	}
	if err := f.Set("off"); err != nil || f.Get() != "off" {
		t.Fatalf("off: %v, effort %q\n%s", err, f.Get(), readFile(patch))
	}
	if raw := readFile(patch); !strings.Contains(raw, "reasoningEfforts:") || !strings.Contains(raw, `"off": none`) {
		t.Fatalf("pro's levels:\n%s", raw)
	}
	if err := f.Set("high"); err != nil {
		t.Fatal(err)
	}
	// kept when the model changes and when the catalog is synced
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	raw := readFile(patch)
	if f.Get() != "high" || !strings.Contains(raw, `reasoningEffort: "high"`) || !strings.Contains(raw, `model: "deepseek/flash"`) {
		t.Fatalf("patch:\n%s", raw)
	}
	// one the model hasn't goes when it changes
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("low"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil || f.Get() != "" {
		t.Fatalf("flash has no low: %v %q", err, f.Get())
	}
	if err := f.Set("high"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("reset: %v %q", err, f.Get())
	}
}
