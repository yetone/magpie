package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// #1775 (peterroe): a plugin of several providers gives each its own
// picture in the auth hook and itself one in package.json's magpie.icon.
// the Plugins page is told the plugin's own, checked and kept as a
// listed plugin's is, apart from its providers'; one that gives none, or
// a picture magpie doesn't take (plain http), is told none.
func TestPluginsStateGivesThePluginsOwnIcon(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("MAGPIE_BUN", "")
	t.Setenv("PATH", "")
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	dir := func(name, icon string) string {
		d := filepath.Join(h, "src", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		pj := map[string]any{"name": name, "version": "1.0.0", "main": "index.js"}
		if icon != "" {
			pj["magpie"] = map[string]any{"icon": icon}
		}
		b, _ := json.Marshal(pj)
		if err := os.WriteFile(filepath.Join(d, "package.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	suite, plain, http := dir("suite", png), dir("plain", ""), dir("http", "http://example.invalid/x.png")
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": suite}, {"spec": plain}, {"spec": http}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range pluginsState(context.Background(), nil).Plugins {
		got[p.Spec] = p.Icon
	}
	if len(got) != 3 {
		t.Fatalf("plugins: %v", got)
	}
	if got[suite] == "" || got[suite] == png {
		t.Errorf("the suite's own picture: %q, want it kept as an icon", got[suite])
	}
	if got[plain] != "" || got[http] != "" {
		t.Errorf("none of its own: %q, http: %q", got[plain], got[http])
	}
}
