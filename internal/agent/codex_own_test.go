package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's own models follow the ones ticked on its ChatGPT subscription.
func TestCodexOwnModelsFollowPicks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-a","display_name":"A","priority":1},
		{"slug":"gpt-b","display_name":"B","priority":2},
		{"slug":"gpt-c","display_name":"C","priority":3}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`), 0o600)

	own := func() []string {
		var out []string
		for _, o := range codex(home).Fields[0].Options(nil) {
			if o.Group == "OpenAI" {
				out = append(out, o.Value)
			}
		}
		return out
	}
	if got := own(); len(got) != 3 {
		t.Fatalf("nothing ticked: %v", got)
	}
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-c", "gpt-a"}}); err != nil {
		t.Fatal(err)
	}
	if got := own(); len(got) != 2 || got[0] != "gpt-a" || got[1] != "gpt-c" {
		t.Fatalf("ticked a and c: %v", got)
	}
	for _, o := range viaMagpie("codex", "") {
		if o.Ref == "" || o.Ref != o.Value {
			t.Fatalf("magpie option without its catalog ref: %+v", o)
		}
	}
}

// The codex provider switched off narrows nothing: Codex's own models are
// all there, its picks kept for when it is switched on again.
func TestCodexOwnModelsNotNarrowedWhileOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-a","display_name":"A","priority":1},
		{"slug":"gpt-b","display_name":"B","priority":2}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`), 0o600)

	own := func() []string {
		var out []string
		for _, o := range codex(home).Fields[0].Options(nil) {
			if o.Group == "OpenAI" {
				out = append(out, o.Value)
			}
		}
		return out
	}
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-b"}, Off: true}); err != nil {
		t.Fatal(err)
	}
	if got := own(); len(got) != 2 {
		t.Errorf("switched off: %v (want both, its picks narrow nothing)", got)
	}
	if err := provider.SetOff("codex", false); err != nil {
		t.Fatal(err)
	}
	if got := own(); len(got) != 1 || got[0] != "gpt-b" {
		t.Errorf("switched on: %v (want just gpt-b)", got)
	}
}
