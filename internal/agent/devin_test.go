package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestDevin(t *testing.T) {
	// without Devin's list, the id is read and written as it is
	old := providerDevinFamilies
	t.Cleanup(func() { providerDevinFamilies = old })
	providerDevinFamilies = func(context.Context) ([]provider.DevinFamily, error) {
		return nil, errors.New("devin is not installed")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg) // where Devin keeps it on Windows

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

// devinFake is a CLI list the way Devin gives it: a family at efforts, fast
// variants of it, a variant at none, and Adaptive.
func devinFake(t *testing.T) {
	old := providerDevinFamilies
	t.Cleanup(func() { providerDevinFamilies = old })
	providerDevinFamilies = func(context.Context) ([]provider.DevinFamily, error) {
		return []provider.DevinFamily{
			{UID: "Adaptive", Label: "Adaptive", Models: []catalog.Model{{ID: "adaptive", Name: "Adaptive"}}},
			{UID: "swe-2", Label: "SWE-2", Aliases: []string{"swe"}, Models: []catalog.Model{
				{ID: "swe-2-high", Name: "SWE-2 High", Context: 262000}, {ID: "swe-2-medium", Name: "SWE-2 Medium"},
				{ID: "swe-2-max", Name: "SWE-2 Max"},
				{ID: "swe-2-low-fast", Name: "SWE-2 Low Fast"}, {ID: "swe-2-high-fast", Name: "SWE-2 High Fast"},
			}},
			{UID: "glm-5.2", Label: "GLM-5.2", Models: []catalog.Model{
				{ID: "glm-5-2", Name: "GLM-5.2"}, {ID: "glm-5-2-max", Name: "GLM-5.2 Max"}, {ID: "glm-5-2-1m", Name: "GLM-5.2 1M"},
			}},
		}, nil
	}
}

// The picker offers the families as the provider lists them — each one
// model, its fast run another — with the effort beside it, not one option
// per variant.
func TestDevinOptionsOfferFamilies(t *testing.T) {
	devinFake(t)
	var got []string
	for _, o := range devinOptions("") {
		got = append(got, o.Value)
		if o.Group != "" {
			t.Fatalf("devin's own models sit flat, like every agent's own list: %v", o)
		}
	}
	if want := "Adaptive swe-2 swe-2-fast glm-5.2 glm-5-2 glm-5-2-1m"; strings.Join(got, " ") != want {
		t.Fatalf("options %q, want %q", strings.Join(got, " "), want)
	}
	for model, want := range map[string]string{
		"swe-2": "medium,high,max", "swe-2-fast": "low,high", "glm-5.2": "max", "glm-5-2-1m": "", "Adaptive": "",
	} {
		if got := strings.Join(devinEfforts(model), ","); got != want {
			t.Errorf("%s efforts %q, want %q", model, got, want)
		}
	}
	// a current value Devin knows but the list predates is still offered
	if opts := devinOptions("gone-model"); opts[0].Value != "gone-model" {
		t.Fatalf("current missing: %v", opts)
	}
}

// Devin's config takes one id, so the model and effort picked are written
// as the family's variant at that effort and read back as the pair; an id
// set before (swe-2-max) shows as its family at its effort.
func TestDevinModelAndEffort(t *testing.T) {
	devinFake(t)
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg) // where Devin keeps it on Windows
	path := filepath.Join(cfg, "devin", "config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"agent": {"model": "swe-2-max"}}`), 0o644)

	a := devin(home, cfg)
	model, effort := a.Field("model"), a.Field("effort")
	saved := jsonGet(path, "agent.model")
	check := func(step, id, m, e string) {
		t.Helper()
		if saved() != id || model.Get() != m || effort.Get() != e {
			t.Fatalf("%s: config %q, picker %q at %q; want %q, %q at %q", step, saved(), model.Get(), effort.Get(), id, m, e)
		}
	}
	check("set before", "swe-2-max", "swe-2", "max")
	if err := effort.Set("high"); err != nil {
		t.Fatal(err)
	}
	check("effort", "swe-2-high", "swe-2", "high")
	// the fast run keeps the effort, as near as its variants have it
	if err := model.Set("swe-2-fast"); err != nil {
		t.Fatal(err)
	}
	check("fast", "swe-2-high-fast", "swe-2-fast", "high")
	if err := effort.Set("medium"); err != nil { // a tie goes up
		t.Fatal(err)
	}
	check("fast effort", "swe-2-high-fast", "swe-2-fast", "high")
	if err := effort.Set("low"); err != nil {
		t.Fatal(err)
	}
	check("fast low", "swe-2-low-fast", "swe-2-fast", "low")
	// the effort's default on a fast run: the family's default effort
	if err := effort.Set(""); err != nil {
		t.Fatal(err)
	}
	check("fast default", "swe-2-high-fast", "swe-2-fast", "high")
	// a family with no effort is its own id, following its newest
	if err := model.Set("glm-5.2"); err != nil {
		t.Fatal(err)
	}
	check("family", "glm-5-2-max", "glm-5.2", "max")
	if err := effort.Set(""); err != nil {
		t.Fatal(err)
	}
	check("family default", "glm-5.2", "glm-5.2", "")
	if err := model.Set("swe-2"); err != nil {
		t.Fatal(err)
	}
	check("family again", "swe-2", "swe-2", "")
	// a variant at none, and one typed in by its own id, go as they are
	if err := model.Set("glm-5-2-1m"); err != nil {
		t.Fatal(err)
	}
	check("at none", "glm-5-2-1m", "glm-5-2-1m", "")
	if err := model.Set("swe-2-medium"); err != nil {
		t.Fatal(err)
	}
	check("variant id", "swe-2-medium", "swe-2", "medium")
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	check("reset", "", "", "")
	if err := effort.Set("high"); err == nil {
		t.Fatal("an effort with no model has nothing to go on")
	}
}
