package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A profile's patch list written in flow style, as dsh's own writers leave
// the template's [] once they add a row (#445), is edited like a block one:
// every profile gets the model, the user's row stays, and a file magpie
// can't edit is named in the error, not as config.yaml.
func TestDshFlowPatchList(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".dsh")
	web, def := filepath.Join(dir, "profiles", "web", "cordis.patch.yml"), filepath.Join(dir, "profiles", "default", "cordis.patch.yml")
	flow := "# Your patch layer for this dsh profile, applied after every bundle layer:\n# a top-level YAML array of loader patch entries ...\n[ { id: some-plugin, disabled: false } ]\n"
	template := "# Your patch layer for this dsh profile.\n[]\n"
	for p, s := range map[string]string{web: flow, def: template} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	f := dsh(home).Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{web, def} {
		s := read(p)
		for _, want := range []string{"# Your patch layer", "- id: llm-pi-ai # magpie", "- id: agent-default-model # magpie", `model: "deepseek/pro"`} {
			if !strings.Contains(s, want) {
				t.Fatalf("missing %q in %s:\n%s", want, p, s)
			}
		}
	}
	s := read(web)
	if !strings.Contains(s, "- id: some-plugin\n  disabled: false\n") || !strings.HasPrefix(s, "# Your patch layer for this dsh profile, applied after every bundle layer:\n# a top-level YAML array") {
		t.Fatalf("the user's row or comments lost:\n%s", s)
	}
	var rows []map[string]any
	if err := yaml.Unmarshal([]byte(s), &rows); err != nil || len(rows) != 3 || rows[0]["id"] != "some-plugin" || rows[0]["disabled"] != false {
		t.Fatalf("%v %v\n%s", err, rows, s)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}

	// back to dsh as it ships: the user's row, now a block list
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got, want := read(web), "# Your patch layer for this dsh profile, applied after every bundle layer:\n# a top-level YAML array of loader patch entries ...\n- id: some-plugin\n  disabled: false\n"; got != want {
		t.Fatalf("reset:\n%q\nwant\n%q", got, want)
	}

	// a file that is no list is named as itself
	os.WriteFile(web, []byte("some-plugin:\n  disabled: false\n"), 0o644)
	err := f.Set("magpie/deepseek/pro")
	if err == nil || !strings.Contains(err.Error(), web) || strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("error: %v", err)
	}
}

// web's patch list is the first whatever the profiles are called.
func TestDshProfilesWebFirst(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "web", "default", "z", "desktop"} {
		p := filepath.Join(dir, "profiles", n, "cordis.patch.yml")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("[]\n"), 0o644)
	}
	var got []string
	for _, f := range dshProfiles(dir) {
		got = append(got, filepath.Base(filepath.Dir(f)))
	}
	if strings.Join(got, " ") != "web a default desktop z" {
		t.Fatalf("%v", got)
	}
}
