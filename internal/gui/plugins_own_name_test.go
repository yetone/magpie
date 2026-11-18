package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// #4558 (MYCF711): a plugin installed from git whose package.json says
// magpie.name ("华为云 HUAWEI") read as its dependency key
// (magpie-officeace) in the Plugins page. Bun takes no non-ASCII key, so
// the package's own name is the only readable one it has. The row is told
// it (ownName), as is the sidebar's title for a page that names none; a
// package that gives none is told none, and the community's list still
// names a package it lists.
func TestPluginsStateGivesAGitPackagesOwnName(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("MAGPIE_BUN", "")
	t.Setenv("PATH", "")
	write := func(path string, v any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const (
		office = "github:example/magpie-officeace"
		plain  = "github:example/magpie-loomy-auth"
		bidi   = "github:example/magpie-bidi"
	)
	dir := plugin.Dir()
	// the reporter's: the dependency key bun installed it under, and the
	// installed copy's package.json carrying the field
	write(filepath.Join(dir, "package.json"), map[string]any{"dependencies": map[string]string{
		"magpie-officeace": office, "magpie-loomy-auth": plain, "magpie-bidi": bidi,
	}})
	write(filepath.Join(dir, "node_modules", "magpie-officeace", "package.json"), map[string]any{
		"name": "magpie-officeace", "version": "0.2.4", "main": "index.js",
		"magpie": map[string]any{"name": "华为云 HUAWEI", "summary": "…", "page": map[string]any{"file": "page.html"}},
	})
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "magpie-officeace", "page.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "node_modules", "magpie-loomy-auth", "package.json"), map[string]any{
		"name": "magpie-loomy-auth", "version": "0.1.0", "main": "index.js",
	})
	// a right-to-left override would show it as another name
	write(filepath.Join(dir, "node_modules", "magpie-bidi", "package.json"), map[string]any{
		"name": "magpie-bidi", "version": "0.1.0", "main": "index.js", "magpie": map[string]any{"name": "Qoder‮"},
	})
	write(filepath.Join(settings.Dir(), "plugins.json"), map[string]any{"plugins": []map[string]any{{"spec": office}, {"spec": plain}, {"spec": bidi}}})

	got := map[string]pluginEntryJSON{}
	for _, p := range pluginsState(context.Background(), nil).Plugins {
		got[p.Spec] = p
	}
	if len(got) != 3 {
		t.Fatalf("plugins: %v", got)
	}
	if o := got[office]; o.OwnName != "华为云 HUAWEI" || o.Package != "magpie-officeace" {
		t.Errorf("the reporter's: ownName %q, package %q; want 华为云 HUAWEI, magpie-officeace", o.OwnName, o.Package)
	}
	if n := got[plain].OwnName; n != "" {
		t.Errorf("one that gives no name: ownName %q, want none", n)
	}
	if n := got[bidi].OwnName; n != "" {
		t.Errorf("a name with a bidi override: ownName %q, want none", n)
	}
	pages, _ := windowParts()
	if len(pages) != 1 || pages[0].Title != "华为云 HUAWEI" {
		t.Errorf("its page's sidebar title: %+v, want 华为云 HUAWEI", pages)
	}
}
