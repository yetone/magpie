package agent

import (
	"os"
	"path/filepath"
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
	// the effort is kept with the model table
	if err := a.Apply("effort", "high"); err != nil {
		t.Fatal(err)
	}
	if v := a.Field("effort").Get(); v != "high" {
		t.Fatalf("effort reads %q", v)
	}
	if tbl, _ := edit.GetTOMLTable(path, atomcodeTable("magpie/fake/m1")); tbl["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort: %v", tbl)
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
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(path)
	if !strings.Contains(string(cfg), `default_model = "AtomGit-glm5.3-flash"`) ||
		!strings.Contains(string(cfg), `default_provider = "AtomGit-qwen3.8-27b"`) {
		t.Fatalf("the user's own defaults did not come back:\n%s", cfg)
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
}
