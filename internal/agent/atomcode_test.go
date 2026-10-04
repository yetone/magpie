package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func atomcodeSaveFake(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
}

// Picking a model for AtomCode writes the account "magpie" and one model
// table per catalog model — as its own CodingPlan sign-in writes them — and
// points both defaults at the picked one; the table's model is the catalog
// reference the gateway resolves.
func TestAtomcodeWiring(t *testing.T) {
	home := t.TempDir()
	atomcodeSaveFake(t)
	a := atomcode(home)
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".atomcode", "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	for _, want := range []string{
		`default_provider = "magpie/fake/m1"`,
		`default_model = "magpie/fake/m1"`,
		`[provider_accounts."magpie"]`,
		`provider = "openai-compatible"`,
		`base_url = "` + gatewayV1() + `"`,
		`api_key = "` + gateway.TokenFor("atomcode") + `"`,
		`[models."magpie/fake/m1"]`,
		`account = "magpie"`,
		`model = "fake/m1"`,
		`context_window = 128000`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("want %s in:\n%s", want, cfg)
		}
	}
	if v := a.Field("model").Get(); v != "magpie/fake/m1" {
		t.Fatalf("model reads %q", v)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift right after a set: %+v\n%s", d, cfg)
	}
	// an effort picked in magpie is written to the model table, and one
	// AtomCode does not know is turned away
	if err := provider.SetModelEfforts("fake/m1", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("effort", "high"); err != nil {
		t.Fatal(err)
	}
	if tbl, _ := edit.GetTOMLTable(path, atomcodeTable("magpie/fake/m1")); tbl["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort: %v", tbl)
	}
	if err := a.Apply("effort", "ultra"); err == nil {
		t.Fatal("an effort AtomCode does not know was accepted")
	}
	// an AtomCode-owned effort is kept with the model table
	if err := edit.SetTOMLKey(path, atomcodeTable("magpie/fake/m1"), "reasoning_effort", "high"); err != nil {
		t.Fatal(err)
	}
	if v := a.Field("effort").Get(); v != "high" {
		t.Fatalf("effort reads %q", v)
	}
	if err := a.Apply("effort", ""); err != nil {
		t.Fatal(err)
	}
	if v := a.Field("effort").Get(); v != "" {
		t.Fatalf("effort reads %q after clearing", v)
	}
}

// Clearing AtomCode's model takes magpie's account and model tables out and
// puts the defaults the user had back — the shape its own sign-in writes.
func TestAtomcodeRestoresOwnDefaults(t *testing.T) {
	home := t.TempDir()
	atomcodeSaveFake(t)
	path := filepath.Join(home, ".atomcode", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`default_provider = "AtomGit-qwen3.8-27b"
default_model = "AtomGit-glm5.3-flash"

[provider_accounts.AtomGit]
provider = "openai"
base_url = "https://llm-api.atomgit.com/v1"

[models."AtomGit-qwen3.8-27b"]
account = "AtomGit"
model = "qwen3.8-27b"
context_window = 262144

[models."AtomGit-glm5.3-flash"]
account = "AtomGit"
model = "glm5.3-flash"
context_window = 512000
`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := atomcode(home)
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(path)
	for _, want := range []string{`[provider_accounts.AtomGit]`, `[models."AtomGit-glm5.3-flash"]`, `[models."magpie/fake/m1"]`} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("want %s kept or written in:\n%s", want, cfg)
		}
	}
	if err := edit.DelTOMLTable(path, `models."AtomGit-qwen3.8-27b"`); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(path)
	if !strings.Contains(string(cfg), `default_model = "AtomGit-glm5.3-flash"`) {
		t.Fatalf("the user's own defaults did not come back:\n%s", cfg)
	}
	if v, _ := edit.GetTOMLTop(path, "default_provider"); v != "" {
		t.Fatalf("deleted provider table was restored as default_provider: %q\n%s", v, cfg)
	}
	if ts, _ := edit.TOMLTables(path); strings.Contains(strings.Join(ts, "\n"), atomcodeAccount) {
		t.Fatalf("magpie's tables stayed:\n%s", cfg)
	}
	if v := a.Field("model").Get(); v != "AtomGit-glm5.3-flash" {
		t.Fatalf("model reads %q, want the user's own default_model", v)
	}
	// a second clear, nothing of magpie's in use, takes both defaults out
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(path, "default_model"); v != "" {
		t.Fatalf("default_model is %q", v)
	}
	if v, _ := edit.GetTOMLTop(path, "default_provider"); v != "" {
		t.Fatalf("default_provider is %q", v)
	}
}

// The catalog as it is now reaches AtomCode's config when Sync runs, and a
// file magpie wrote nothing into stays as it is.
func TestAtomcodeSync(t *testing.T) {
	home := t.TempDir()
	atomcodeSaveFake(t)
	a := atomcode(home)
	path := filepath.Join(home, ".atomcode", "config.toml")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("Sync wrote a config where magpie had nothing")
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLKey(path, atomcodeTable("magpie/fake/m1"), "reasoning_effort", "high"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	config := strings.ReplaceAll(string(b), `[provider_accounts."magpie"]`, `[provider_accounts.magpie]`)
	config = strings.ReplaceAll(config, `[models."magpie/fake/m1"]`, `[models.'magpie/fake/m1']`)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("valid unquoted account/single-quoted model reported drift: %+v", d)
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(path)
	if !strings.Contains(string(cfg), `[models."magpie/fake/m3"]`) {
		t.Fatalf("the new model did not reach the config:\n%s", cfg)
	}
	if v, _ := edit.GetTOMLTop(path, "default_model"); v != "magpie/fake/m1" {
		t.Fatalf("Sync moved the default: %q", v)
	}
	if !strings.Contains(string(cfg), `reasoning_effort = "high"`) {
		t.Fatalf("Sync dropped the selected effort:\n%s", cfg)
	}
	if strings.Count(string(cfg), "[provider_accounts.magpie]") != 0 || strings.Count(string(cfg), `[provider_accounts."magpie"]`) != 1 {
		t.Fatalf("Sync did not canonicalize the exact magpie account:\n%s", cfg)
	}
	config = strings.ReplaceAll(string(cfg), `[provider_accounts."magpie"]`, `[provider_accounts.magpie]`)
	config = strings.ReplaceAll(config, `[models."magpie/fake/m1"]`, `[models.'magpie/fake/m1']`)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(path)
	if strings.Contains(string(cfg), "[provider_accounts.magpie]") || strings.Contains(string(cfg), gateway.TokenFor("atomcode")) {
		t.Fatalf("clearing left the unquoted magpie account or key behind:\n%s", cfg)
	}
}

func TestAtomcodeLegacyProvidersArePickableAndRestored(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".atomcode", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`default_provider = "legacy"
default_model = "legacy"

[providers.legacy]
type = "openai"
model = "legacy-model"
base_url = "https://example.com/v1"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	atomcodeSaveFake(t)
	a := atomcode(home)
	if !slices.ContainsFunc(a.Field("model").Options(a.Values()), func(o Option) bool { return o.Value == "legacy" }) {
		t.Fatal("legacy provider missing from model picker")
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(path, "default_model"); v != "legacy" {
		t.Fatalf("legacy default_model not restored: %q", v)
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "legacy"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(path, "default_provider"); v != "legacy" {
		t.Fatalf("legacy provider was not selected: %q", v)
	}
	if v, _ := edit.GetTOMLTop(path, "default_model"); v != "" {
		t.Fatalf("legacy selection left a default_model override: %q", v)
	}
}

func TestAtomcodeEffortLevelsMatchAgentSupport(t *testing.T) {
	got := atomcodeSupportedEfforts([]string{"none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"})
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !slices.Equal(got, want) {
		t.Fatalf("supported effort levels = %v, want %v", got, want)
	}
	if got := atomcodeSupportedEfforts(nil); len(got) != 0 {
		t.Fatalf("non-reasoning model has effort options: %v", got)
	}
}

// An effort set while the model table is gone is refused: a fresh table of
// nothing but a reasoning_effort would be an orphan.
func TestAtomcodeEffortWithoutModelTableIsRefused(t *testing.T) {
	home := t.TempDir()
	atomcodeSaveFake(t)
	a := atomcode(home)
	if err := provider.SetModelEfforts("fake/m1", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".atomcode", "config.toml")
	if err := edit.DelTOMLTable(path, atomcodeTable("magpie/fake/m1")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := a.Apply("effort", "high"); err == nil {
		t.Fatal("an effort was set on a missing model table")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("the refused effort changed the config:\n%s", after)
	}
	// clearing a gone table's effort is a no-op, not an error
	if err := a.Apply("effort", ""); err != nil {
		t.Fatalf("clearing on a missing model table: %v", err)
	}
	after, _ = os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("the cleared effort changed the config:\n%s", after)
	}
}
