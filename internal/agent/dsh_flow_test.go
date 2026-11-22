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
	if err := yaml.Unmarshal([]byte(s), &rows); err != nil || len(rows) != 5 || rows[0]["id"] != "some-plugin" || rows[0]["disabled"] != false {
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

// A patch list that starts with a YAML document start marker (---), as one
// written by hand or by another tool may be, is edited like one without it,
// and the marker stays at the head of the file (#2667).
func TestDshDocStart(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".dsh")
	web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	// the reporter's file (#2667), down to its quoting and trailing spaces
	os.WriteFile(web, []byte("---\n- \n  id: \"ui-settings-general\"\n  name: \"@deepseek-ai/dsh-client-ui-settings-general\"\n  config: \n    welcomeNoticeVersion: \"2026-09-28.1\"\n"), 0o644)
	f := dsh(home).Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(web)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("the document start marker lost:\n%s", s)
	}
	if !strings.Contains(s, "id: \"ui-settings-general\"") {
		t.Fatalf("the user's entry lost:\n%s", s)
	}
	for _, want := range []string{"- id: llm-pi-ai # magpie", "- id: agent-default-model # magpie", `model: "deepseek/pro"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s:\n%s", want, web, s)
		}
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
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

// Only one "---" at the left edge, before anything but comments, opens the
// list (#2667). One anywhere else, or a second document, still fails the
// file instead of being kept as if it were the list's.
func TestDshDocStartOnlyFirst(t *testing.T) {
	for _, raw := range []string{
		"---\n- id: a\n",
		"--- \n- id: a\n",
		"--- # patches\n- id: a\n",
		"# patches\n---\n- id: a\n",
		"---\n# patches\n\n- id: a\n",
		"---\n[ { id: a } ]\n",
		"---\n[]\n",
	} {
		head, items, err := dshParse("p.yml", raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if len(head) == 0 || !strings.HasPrefix(strings.Join(head, "\n"), strings.SplitN(raw, "\n", 2)[0]) {
			t.Fatalf("%q: head %q lost the opening lines", raw, head)
		}
		if raw != "---\n[]\n" && (len(items) != 1 || items[0].id != "a") {
			t.Fatalf("%q: items %+v", raw, items)
		}
	}
	for _, raw := range []string{
		"---\n---\n- id: a\n",
		"- id: a\n---\n- id: b\n",
		"---\n- id: a\n---\n- id: b\n",
		"  ---\n- id: a\n",
		"----\n- id: a\n",
		"--- x\n- id: a\n",
		"[]\n---\n- id: a\n",
	} {
		if _, _, err := dshParse("p.yml", raw); err == nil || !strings.Contains(err.Error(), "not a list of entries magpie can edit") {
			t.Fatalf("%q: want the file refused, got %v", raw, err)
		}
	}
}
