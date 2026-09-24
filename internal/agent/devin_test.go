package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestDevin(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)

	dir := filepath.Join(cfg, "devin")
	path := filepath.Join(dir, "config.json")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte("{\n  // mine\n  \"theme_mode\": \"dark\",\n  \"agent\": {\"model\": \"swe-2-max\"}\n}\n"), 0o644)

	a := devin(home, cfg)
	if a.Path != path {
		t.Fatalf("path: %q", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "swe-2-max" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("claude-opus-5-5-high"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	raw := string(b)
	if !strings.Contains(raw, "// mine") || !strings.Contains(raw, `"theme_mode": "dark"`) ||
		!strings.Contains(raw, `"model": "claude-opus-5-5-high"`) {
		t.Fatalf("config:\n%s", raw)
	}
	if f.Get() != "claude-opus-5-5-high" {
		t.Fatalf("get after set: %q", f.Get())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("get after reset: %q", f.Get())
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), `"model"`) || !strings.Contains(string(b), "// mine") {
		t.Fatalf("config after reset:\n%s", b)
	}
}

func TestDevinOptionsListFamiliesThenVariants(t *testing.T) {
	old := providerDevinFamilies
	defer func() { providerDevinFamilies = old }()
	providerDevinFamilies = func(context.Context) ([]provider.DevinFamily, error) {
		return []provider.DevinFamily{{
			UID: "swe-2", Label: "SWE-2", Aliases: []string{"swe"},
			Models: []catalog.Model{{ID: "swe-2-max", Name: "SWE-2 Max"}, {ID: "swe-2-min", Name: "SWE-2 Min"}},
		}}, nil
	}
	opts := devinOptions("")
	if len(opts) != 3 || opts[0].Value != "swe-2" || opts[1].Value != "swe-2-max" {
		t.Fatalf("options: %v", opts)
	}
	if opts[0].Group != "SWE-2" || opts[2].Group != "SWE-2" {
		t.Fatalf("groups: %v", opts)
	}
	// a current value Devin knows but the list predates is still offered
	opts = devinOptions("gone-model")
	if opts[0].Value != "gone-model" {
		t.Fatalf("current missing: %v", opts)
	}
}
