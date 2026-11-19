package library

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A project's Pi packages (#7249): added, turned off and on, removed, and a
// declaration the project had is its own until adopted.
func TestProjectPiPackages(t *testing.T) {
	h := sandbox(t)
	_ = h
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ok(t)(AddProject(proj))
	file := filepath.Join(proj, ".pi/settings.json")
	view := func() ProjectView {
		for _, p := range View0(t).Projects {
			if p.Dir == proj {
				return p
			}
		}
		t.Fatal("no project")
		return ProjectView{}
	}

	// add: one entry, and the file is magpie's
	ok(t)(ProjectPi(proj, "npm:pi-demo", "on"))
	if got := strings.Join(strings.Fields(read(t, file)), ""); got != `{"packages":["npm:pi-demo"]}` {
		t.Fatalf("settings: %s", got)
	}
	ok(t)(ProjectPi(proj, "npm:pi-demo", "on"))
	if strings.Count(read(t, file), "pi-demo") != 1 {
		t.Errorf("reapplied twice:\n%s", read(t, file))
	}
	// off but kept, then on again
	ok(t)(ProjectPi(proj, "npm:pi-demo", "off"))
	if s := read(t, file); !strings.Contains(s, `"extensions": []`) && !strings.Contains(strings.Join(strings.Fields(s), ""), `"extensions":[]`) {
		t.Errorf("off writes no empty filters:\n%s", s)
	}
	if v := view(); len(v.Pi) != 1 || v.Pi[0].State != "off" {
		t.Errorf("view: %+v", v.Pi)
	}
	ok(t)(ProjectPi(proj, "npm:pi-demo", "on"))
	if got := strings.Join(strings.Fields(read(t, file)), ""); got != `{"packages":["npm:pi-demo"]}` {
		t.Fatalf("on again: %s", got)
	}
	// removed: the file magpie made goes with it
	ok(t)(ProjectPi(proj, "npm:pi-demo", "removed"))
	if _, err := os.Stat(file); err == nil {
		t.Errorf("settings stay:\n%s", read(t, file))
	}

	// the project's own declaration is never written over or taken out
	own := "{\n  \"theme\": \"dark\",\n  \"packages\": [{\"source\": \"npm:pi-own\", \"skills\": [\"a\"]}, \"npm:pi-plain\"]\n}\n"
	write(t, file, own)
	res, err := ProjectPi(proj, "npm:pi-plain", "on")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Error, "adopted") {
		t.Fatalf("problems: %+v", res.Problems)
	}
	ok(t)(ProjectPi(proj, "npm:pi-plain", "removed"))
	ok(t)(ProjectPi(proj, "npm:pi-new", "on"))
	if s := read(t, file); !strings.Contains(s, "npm:pi-new") || !strings.Contains(s, `"theme"`) || !strings.Contains(s, "npm:pi-own") {
		t.Fatalf("settings:\n%s", s)
	}
	if o := view().Own["pi"]; len(o) != 2 {
		t.Errorf("own: %+v", o)
	}
	ok(t)(ProjectPi(proj, "npm:pi-new", "removed"))
	if squash(read(t, file)) != squash(own) {
		t.Fatalf("the project's own file changed:\n%s", read(t, file))
	}

	// adopt: switched, never rewritten until asked, put back on letting go;
	// a filtered one is refused
	if _, err := ProjectPiAdopt(proj, "npm:pi-own"); err == nil {
		t.Errorf("a filtered entry was adopted")
	}
	ok(t)(ProjectPiAdopt(proj, "npm:pi-plain"))
	if squash(read(t, file)) != squash(own) {
		t.Fatalf("adopting rewrote the file:\n%s", read(t, file))
	}
	ok(t)(ProjectPi(proj, "npm:pi-plain", "off"))
	if s := read(t, file); !strings.Contains(s, `"npm:pi-own"`) || strings.Contains(squash(s), `["npm:pi-plain"`) || !strings.Contains(strings.Join(strings.Fields(s), ""), `"source":"npm:pi-plain"`) {
		t.Fatalf("off:\n%s", s)
	}
	ok(t)(ProjectPi(proj, "npm:pi-plain", "removed"))
	if got := strings.Join(strings.Fields(read(t, file)), ""); got != strings.Join(strings.Fields(own), "") {
		t.Fatalf("letting go did not put the entry back:\n%s", read(t, file))
	}
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// A plugin that is a Pi package, given to a project for Pi (#7243), is one
// entry of its .pi/settings.json: its folder. Applied again it stays one;
// withdrawn, only that entry goes; a declaration the project has of the same
// package is left byte for byte.
func TestProjectPluginPiPackage(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	if err := os.WriteFile(filepath.Join(src, "package.json"), []byte(`{"name":"acme-kit","pi":{"skills":["./skills"],"extensions":["./extension.ts"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok(t)(InstallPlugins(src, nil, []string{}))
	dir := pluginDir("acme-kit")
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, d := range []string{a, b} {
		gitInit(t, d)
		ok(t)(AddProject(d))
	}
	file := filepath.Join(a, ".pi/settings.json")
	count := func(p string) int { return strings.Count(read(t, p), dir) }

	// not for Pi: no package
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"}))
	gone(t, file)
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"pi"}))
	if count(file) != 1 {
		t.Fatalf("one entry for the plugin:\n%s", read(t, file))
	}
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"pi"}))
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"pi", "claude"}))
	if count(file) != 1 {
		t.Fatalf("applied again:\n%s", read(t, file))
	}
	gone(t, filepath.Join(b, ".pi"))
	ok(t)(ProjectPlugin(a, "acme-kit", nil))
	gone(t, file)

	// the project's own filtered declaration of the same folder
	own := "{\n  \"theme\": \"dark\",\n  \"packages\": [{\"source\": " + strconv.Quote(dir) + ", \"skills\": [\"demo\"]}]\n}\n"
	write(t, file, own)
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"pi"}))
	if read(t, file) != own {
		t.Fatalf("assignment changed the native entry:\n%s", read(t, file))
	}
	ok(t)(ProjectPlugin(a, "acme-kit", nil))
	if read(t, file) != own {
		t.Fatalf("withdrawal changed the native entry:\n%s", read(t, file))
	}

	// beside the project's other packages: only the managed entry goes
	write(t, file, "{\"packages\": [\"npm:pi-own\"]}\n")
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"pi"}))
	if s := read(t, file); count(file) != 1 || !strings.Contains(s, "npm:pi-own") {
		t.Fatalf("beside:\n%s", s)
	}
	ok(t)(ProjectPlugin(a, "acme-kit", nil))
	if s := read(t, file); strings.Contains(s, dir) || !strings.Contains(s, "npm:pi-own") {
		t.Fatalf("withdrawn:\n%s", s)
	}
}
